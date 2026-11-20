package repository

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Redis keys of the resilience layer (design §4, §5, §8). All keys carry a
// TTL so an abandoned session or group never leaves state behind.
const (
	resilienceSessionFailurePrefix      = "xy:resil:sf:"
	resilienceSessionAvoidPrefix        = "xy:resil:avoid:"
	resilienceSessionAvoidWrittenPrefix = "xy:resil:avoid_written:"
	resilienceGroupStablePrefix         = "xy:resil:group_stable:"
	resilienceUndeliveredPrefix         = "xy:resil:undelivered:"
	resilienceCacheRatioPrefix          = "xy:resil:cache_ratio:"
	resilienceFallbackKeyPrefix         = "xy:resil:fb_key:"

	// resilienceGroupStableTTL bounds a stale stability marker; the marker is
	// refreshed whenever the group is evaluated healthy.
	resilienceGroupStableTTL = time.Hour
)

// resilienceSessionFailureScript counts a failure of one account of a
// session. Each account keeps its own window end in field "e:<account>"
// (review R06): a failure of another account must not keep an old count
// alive. The window of an account restarts once it ended and is extended by
// each of its failures; the key lives as long as the newest window.
// KEYS[1] session hash, ARGV[1] account, ARGV[2] window ms, ARGV[3] now ms, ARGV[4] writer instance, ARGV[5] process monotonic ms (-1 if unavailable).
var resilienceSessionFailureScript = redis.NewScript(`
	local now = tonumber(ARGV[3])
	local win = tonumber(ARGV[2])
	local ek = 'e:' .. ARGV[1]
	local fk = 'f:' .. ARGV[1]
	local e = tonumber(redis.call('HGET', KEYS[1], ek) or '0')
	local v
	if e <= now then
		redis.call('HSET', KEYS[1], ARGV[1], 1)
		v = 1
	else
		v = redis.call('HINCRBY', KEYS[1], ARGV[1], 1)
	end
	local last = math.max(now, tonumber(redis.call('HGET', KEYS[1], fk)) or now)
	local wk, mk = 'w:' .. ARGV[1], 'm:' .. ARGV[1]
	local mono = tonumber(ARGV[5])
	if v == 1 then
		-- Clearing or expiry starts a new account window.
		redis.call('HSET', KEYS[1], wk, ARGV[4], mk, mono)
	elseif redis.call('HGET', KEYS[1], wk) ~= ARGV[4] then
		-- No owner on an existing count means mixed/unknown until the count resets.
		redis.call('HDEL', KEYS[1], wk, mk)
	else
		local prev = tonumber(redis.call('HGET', KEYS[1], mk)) or -1
		if prev >= 0 then
			-- A single untimed failure makes the whole window wall-clock only.
			if mono >= 0 then mono = math.max(prev, mono) end
			redis.call('HSET', KEYS[1], mk, mono)
		end
	end
	redis.call('HSET', KEYS[1], ek, last + win, fk, last)
	redis.call('PEXPIRE', KEYS[1], win)
	return v
`)

// Clear only a positively identified failure strictly older than this dispatch.
var resilienceClearPriorFailuresScript = redis.NewScript(`
	local id = ARGV[1]
	local last = tonumber(redis.call('HGET', KEYS[1], 'f:' .. id))
	if not last or last <= 0 then return 0 end
	local wk, mk = 'w:' .. id, 'm:' .. id
	local cutoff = tonumber(ARGV[2]) - tonumber(ARGV[4])
	local sendMono = tonumber(ARGV[5])
	if sendMono >= 0 and ARGV[3] ~= '' and redis.call('HGET', KEYS[1], wk) == ARGV[3] then
		local mono = tonumber(redis.call('HGET', KEYS[1], mk))
		if mono and mono >= 0 then last, cutoff = mono, sendMono end
	end
	if last < cutoff then
		return redis.call('HDEL', KEYS[1], id, 'e:' .. id, 'f:' .. id, wk, mk)
	end
	return 0
`)

// sessionFailureEndField names the window end of an account's count.
const sessionFailureEndField = "e:"

const resilienceAvoidArgsLua = `
 local stamp = tonumber(ARGV[3])
 local written = tonumber(redis.call('HGET', KEYS[#KEYS], ARGV[1]) or '0')
 local reset = tonumber(ARGV[#ARGV])
`

const resilienceAvoidStoreLua = `
 local existing = tonumber(redis.call('ZSCORE', KEYS[1], ARGV[1]) or '0')
 if reset > 0 and written < reset then existing = 0 end
 local deadline = math.max(existing, tonumber(ARGV[2]))
 redis.call('ZADD', KEYS[1], deadline, ARGV[1])
	redis.call('HSET', KEYS[#KEYS], ARGV[1], stamp)
	redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', '(' .. ARGV[3])
	local latest = redis.call('ZREVRANGE', KEYS[1], 0, 0, 'WITHSCORES')
	if #latest > 0 then
		redis.call('PEXPIREAT', KEYS[1], latest[2])
		redis.call('PEXPIREAT', KEYS[#KEYS], latest[2])
	end
	return deadline
`

var resilienceAvoidAddScript = redis.NewScript(resilienceAvoidArgsLua + resilienceAvoidStoreLua)

var resilienceIncrExpireScript = redis.NewScript(`
	local v = redis.call('INCR', KEYS[1])
	if v == 1 then
		redis.call('EXPIRE', KEYS[1], ARGV[1])
	end
	return v
`)

var resilienceMarkStableScript = redis.NewScript(`
	local cur = redis.call('GET', KEYS[1])
	if cur then
		redis.call('EXPIRE', KEYS[1], ARGV[2])
		return cur
	end
	redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
	return ARGV[1]
`)

var sessionFailureProcessStart = time.Now()

func sessionMonotonicMillis(at time.Time) int64 {
	// Round(0) strips only the monotonic reading. Sub would silently use the
	// wall clock for a decoded timestamp, so mark that reading unavailable.
	if at == at.Round(0) || at.Before(sessionFailureProcessStart) { //nolint:staticcheck // QF1009: Equal compares instants and cannot detect the stripped monotonic reading.
		return -1
	}
	return at.Sub(sessionFailureProcessStart).Milliseconds()
}

type resilienceStore struct {
	rdb             *redis.Client
	now             func() time.Time
	instanceID      string
	monotonicMillis func(time.Time) int64
}

// NewResilienceStore creates the Redis store of the resilience layer.
func NewResilienceStore(rdb *redis.Client) service.ResilienceStore {
	if rdb == nil {
		return nil
	}
	return &resilienceStore{rdb: rdb, now: time.Now, instanceID: uuid.NewString(), monotonicMillis: sessionMonotonicMillis}
}

func sessionKey(prefix string, groupID int64, sessionHash string) string {
	return prefix + strconv.FormatInt(groupID, 10) + ":" + sessionHash
}

func (s *resilienceStore) IncrSessionFailure(ctx context.Context, groupID int64, sessionHash string, accountID int64, window time.Duration) (int64, error) {
	key := sessionKey(resilienceSessionFailurePrefix, groupID, sessionHash)
	at := s.now()
	v, err := resilienceSessionFailureScript.Run(ctx, s.rdb, []string{key}, strconv.FormatInt(accountID, 10),
		window.Milliseconds(), at.UnixMilli(), s.instanceID, s.monotonicMillis(at)).Int64()
	if err != nil {
		return 0, fmt.Errorf("incr session failure: %w", err)
	}
	return v, nil
}

func (s *resilienceStore) ClearSessionFailure(ctx context.Context, groupID int64, sessionHash string, accountID int64) error {
	key := sessionKey(resilienceSessionFailurePrefix, groupID, sessionHash)
	id := strconv.FormatInt(accountID, 10)
	return s.rdb.HDel(ctx, key, id, sessionFailureEndField+id, "f:"+id, "w:"+id, "m:"+id).Err()
}

func (s *resilienceStore) ClearSessionFailuresBefore(ctx context.Context, groupID int64, sessionHash string, accountID int64, start time.Time) error {
	if start.IsZero() {
		return nil
	}
	key := sessionKey(resilienceSessionFailurePrefix, groupID, sessionHash)
	return resilienceClearPriorFailuresScript.Run(ctx, s.rdb, []string{key}, accountID, start.UnixMilli(), s.instanceID, service.SessionFailureClockSkewAllowance.Milliseconds(), s.monotonicMillis(start)).Err()
}

// SessionFailures returns the counts whose window has not ended; a count
// written before window ends were stored has no end and lives with the key.
func (s *resilienceStore) SessionFailures(ctx context.Context, groupID int64, sessionHash string) (map[int64]int64, error) {
	key := sessionKey(resilienceSessionFailurePrefix, groupID, sessionHash)
	raw, err := s.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	now := s.now().UnixMilli()
	out := make(map[int64]int64, len(raw))
	for k, v := range raw {
		id, err1 := strconv.ParseInt(k, 10, 64)
		n, err2 := strconv.ParseInt(v, 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		if end, ok := raw[sessionFailureEndField+k]; ok {
			if e, err := strconv.ParseInt(end, 10, 64); err == nil && e <= now {
				continue
			}
		}
		out[id] = n
	}
	return out, nil
}

func (s *resilienceStore) AddSessionAvoid(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration, resetAt ...time.Time) error {
	key := sessionKey(resilienceSessionAvoidPrefix, groupID, sessionHash)
	now := s.now()
	return resilienceAvoidAddScript.Run(ctx, s.rdb, []string{key, sessionKey(resilienceSessionAvoidWrittenPrefix, groupID, sessionHash)},
		strconv.FormatInt(accountID, 10),
		now.Add(ttl).UnixMilli(),
		now.UnixMilli(),
		avoidResetMillis(resetAt),
	).Err()
}

func avoidResetMillis(resetAt []time.Time) int64 {
	if len(resetAt) == 0 || resetAt[0].IsZero() {
		return 0
	}
	cutoff := resetAt[0].UnixMilli()
	if !time.UnixMilli(cutoff).Equal(resetAt[0]) {
		cutoff++ // Match the read-side strict comparison against a microsecond reset.
	}
	return cutoff
}

func (s *resilienceStore) SessionAvoid(ctx context.Context, groupID int64, sessionHash string) (map[int64]struct{}, error) {
	records, err := s.SessionAvoidWrittenAt(ctx, groupID, sessionHash)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]struct{}, len(records))
	for id := range records {
		out[id] = struct{}{}
	}
	return out, nil
}

var resilienceAvoidReadScript = redis.NewScript(`
local members = redis.call('ZRANGEBYSCORE', KEYS[1], ARGV[1], '+inf')
local result = {}
for _, member in ipairs(members) do
  table.insert(result, member)
  table.insert(result, redis.call('HGET', KEYS[2], member) or '0')
end
return result
`)

func (s *resilienceStore) SessionAvoidWrittenAt(ctx context.Context, groupID int64, sessionHash string) (map[int64]time.Time, error) {
	keys := []string{sessionKey(resilienceSessionAvoidPrefix, groupID, sessionHash), sessionKey(resilienceSessionAvoidWrittenPrefix, groupID, sessionHash)}
	values, err := resilienceAvoidReadScript.Run(ctx, s.rdb, keys, s.now().UnixMilli()).StringSlice()
	if err != nil {
		return nil, err
	}
	out := make(map[int64]time.Time, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		id, err := strconv.ParseInt(values[i], 10, 64)
		if err != nil {
			continue
		}
		var at time.Time
		if millis, err := strconv.ParseInt(values[i+1], 10, 64); err == nil && millis > 0 {
			at = time.UnixMilli(millis)
		}
		out[id] = at
	}
	return out, nil
}

func (s *resilienceStore) MarkGroupStable(ctx context.Context, groupID int64, now time.Time) (time.Time, error) {
	key := resilienceGroupStablePrefix + strconv.FormatInt(groupID, 10)
	v, err := resilienceMarkStableScript.Run(ctx, s.rdb, []string{key}, strconv.FormatInt(now.UnixMilli(), 10), int64(resilienceGroupStableTTL/time.Second)).Text()
	if err != nil {
		return time.Time{}, err
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms), nil
}

func (s *resilienceStore) ClearGroupStable(ctx context.Context, groupID int64) error {
	return s.rdb.Del(ctx, resilienceGroupStablePrefix+strconv.FormatInt(groupID, 10)).Err()
}

func (s *resilienceStore) IncrUndelivered(ctx context.Context, apiKeyID int64, hour string) (int64, error) {
	key := resilienceUndeliveredPrefix + strconv.FormatInt(apiKeyID, 10) + ":" + hour
	return resilienceIncrExpireScript.Run(ctx, s.rdb, []string{key}, int64(2*time.Hour/time.Second)).Int64()
}

func (s *resilienceStore) SetSessionCacheRatio(ctx context.Context, key string, ratio float64, ttl time.Duration) error {
	return s.rdb.Set(ctx, resilienceCacheRatioPrefix+key, strconv.FormatFloat(ratio, 'f', 4, 64), ttl).Err()
}

func (s *resilienceStore) SessionCacheRatio(ctx context.Context, key string) (float64, bool, error) {
	v, err := s.rdb.Get(ctx, resilienceCacheRatioPrefix+key).Result()
	if err == redis.Nil {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false, nil
	}
	return f, true, nil
}

func resilienceFallbackKey(groupID, apiKeyID int64) string {
	return resilienceFallbackKeyPrefix + strconv.FormatInt(groupID, 10) + ":" + strconv.FormatInt(apiKeyID, 10)
}

func (s *resilienceStore) MarkFallbackKey(ctx context.Context, groupID, apiKeyID int64, ttl time.Duration) error {
	return s.rdb.Set(ctx, resilienceFallbackKey(groupID, apiKeyID), "1", ttl).Err()
}

func (s *resilienceStore) FallbackKey(ctx context.Context, groupID, apiKeyID int64) (bool, error) {
	n, err := s.rdb.Exists(ctx, resilienceFallbackKey(groupID, apiKeyID)).Result()
	return n > 0, err
}

func (s *resilienceStore) ClearFallbackKey(ctx context.Context, groupID, apiKeyID int64) error {
	return s.rdb.Del(ctx, resilienceFallbackKey(groupID, apiKeyID)).Err()
}
