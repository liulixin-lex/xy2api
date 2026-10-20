package repository

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Redis keys of the resilience layer (design §4, §5, §8). All keys carry a
// TTL so an abandoned session or group never leaves state behind.
const (
	resilienceSessionFailurePrefix = "xy:resil:sf:"
	resilienceSessionAvoidPrefix   = "xy:resil:avoid:"
	resilienceGroupStablePrefix    = "xy:resil:group_stable:"
	resilienceUndeliveredPrefix    = "xy:resil:undelivered:"
	resilienceCacheRatioPrefix     = "xy:resil:cache_ratio:"
	resilienceFallbackKeyPrefix    = "xy:resil:fb_key:"

	// resilienceGroupStableTTL bounds a stale stability marker; the marker is
	// refreshed whenever the group is evaluated healthy.
	resilienceGroupStableTTL = time.Hour
)

// resilienceSessionFailureScript counts a failure of one account of a
// session. Each account keeps its own window end in field "e:<account>"
// (review R06): a failure of another account must not keep an old count
// alive. The window of an account restarts once it ended and is extended by
// each of its failures; the key lives as long as the newest window.
// KEYS[1] session hash, ARGV[1] account, ARGV[2] window ms, ARGV[3] now ms.
var resilienceSessionFailureScript = redis.NewScript(`
	local now = tonumber(ARGV[3])
	local win = tonumber(ARGV[2])
	local ek = 'e:' .. ARGV[1]
	local e = tonumber(redis.call('HGET', KEYS[1], ek) or '0')
	local v
	if e <= now then
		redis.call('HSET', KEYS[1], ARGV[1], 1)
		v = 1
	else
		v = redis.call('HINCRBY', KEYS[1], ARGV[1], 1)
	end
	redis.call('HSET', KEYS[1], ek, now + win)
	redis.call('PEXPIRE', KEYS[1], win)
	return v
`)

// sessionFailureEndField names the window end of an account's count.
const sessionFailureEndField = "e:"

var resilienceAvoidAddScript = redis.NewScript(`
	redis.call('ZADD', KEYS[1], ARGV[2], ARGV[1])
	redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[3])
	redis.call('PEXPIRE', KEYS[1], ARGV[4])
	return 1
`)

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

type resilienceStore struct {
	rdb *redis.Client
	now func() time.Time
}

// NewResilienceStore creates the Redis store of the resilience layer.
func NewResilienceStore(rdb *redis.Client) service.ResilienceStore {
	if rdb == nil {
		return nil
	}
	return &resilienceStore{rdb: rdb, now: time.Now}
}

func sessionKey(prefix string, groupID int64, sessionHash string) string {
	return prefix + strconv.FormatInt(groupID, 10) + ":" + sessionHash
}

func (s *resilienceStore) IncrSessionFailure(ctx context.Context, groupID int64, sessionHash string, accountID int64, window time.Duration) (int64, error) {
	key := sessionKey(resilienceSessionFailurePrefix, groupID, sessionHash)
	v, err := resilienceSessionFailureScript.Run(ctx, s.rdb, []string{key}, strconv.FormatInt(accountID, 10),
		window.Milliseconds(), s.now().UnixMilli()).Int64()
	if err != nil {
		return 0, fmt.Errorf("incr session failure: %w", err)
	}
	return v, nil
}

func (s *resilienceStore) ClearSessionFailure(ctx context.Context, groupID int64, sessionHash string, accountID int64) error {
	key := sessionKey(resilienceSessionFailurePrefix, groupID, sessionHash)
	id := strconv.FormatInt(accountID, 10)
	return s.rdb.HDel(ctx, key, id, sessionFailureEndField+id).Err()
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

func (s *resilienceStore) AddSessionAvoid(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	key := sessionKey(resilienceSessionAvoidPrefix, groupID, sessionHash)
	now := time.Now()
	return resilienceAvoidAddScript.Run(ctx, s.rdb, []string{key},
		strconv.FormatInt(accountID, 10),
		now.Add(ttl).UnixMilli(),
		now.UnixMilli(),
		ttl.Milliseconds(),
	).Err()
}

func (s *resilienceStore) SessionAvoid(ctx context.Context, groupID int64, sessionHash string) (map[int64]struct{}, error) {
	key := sessionKey(resilienceSessionAvoidPrefix, groupID, sessionHash)
	members, err := s.rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:     key,
		Start:   strconv.FormatInt(time.Now().UnixMilli(), 10),
		Stop:    "+inf",
		ByScore: true,
	}).Result()
	if err != nil {
		return nil, err
	}
	out := make(map[int64]struct{}, len(members))
	for _, m := range members {
		if id, err := strconv.ParseInt(m, 10, 64); err == nil {
			out[id] = struct{}{}
		}
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
