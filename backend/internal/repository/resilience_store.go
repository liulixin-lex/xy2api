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
	resilienceBalanceLockPrefix    = "xy:resil:balance_probe:"
	resilienceGenProbePrefix       = "xy:resil:gen_probe:"
	resilienceRecoveringKey        = "xy:resil:recovering"
	resilienceGroupStablePrefix    = "xy:resil:group_stable:"
	resilienceUndeliveredPrefix    = "xy:resil:undelivered:"
	resilienceCacheRatioPrefix     = "xy:resil:cache_ratio:"
	resilienceFallbackKeyPrefix    = "xy:resil:fb_key:"

	// resilienceGroupStableTTL bounds a stale stability marker; the marker is
	// refreshed whenever the group is evaluated healthy.
	resilienceGroupStableTTL = time.Hour
)

var resilienceIncrHashScript = redis.NewScript(`
	local v = redis.call('HINCRBY', KEYS[1], ARGV[1], 1)
	redis.call('PEXPIRE', KEYS[1], ARGV[2])
	return v
`)

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
}

// NewResilienceStore creates the Redis store of the resilience layer.
func NewResilienceStore(rdb *redis.Client) service.ResilienceStore {
	if rdb == nil {
		return nil
	}
	return &resilienceStore{rdb: rdb}
}

func sessionKey(prefix string, groupID int64, sessionHash string) string {
	return prefix + strconv.FormatInt(groupID, 10) + ":" + sessionHash
}

func (s *resilienceStore) IncrSessionFailure(ctx context.Context, groupID int64, sessionHash string, accountID int64, window time.Duration) (int64, error) {
	key := sessionKey(resilienceSessionFailurePrefix, groupID, sessionHash)
	v, err := resilienceIncrHashScript.Run(ctx, s.rdb, []string{key}, strconv.FormatInt(accountID, 10), window.Milliseconds()).Int64()
	if err != nil {
		return 0, fmt.Errorf("incr session failure: %w", err)
	}
	return v, nil
}

func (s *resilienceStore) ClearSessionFailure(ctx context.Context, groupID int64, sessionHash string, accountID int64) error {
	key := sessionKey(resilienceSessionFailurePrefix, groupID, sessionHash)
	return s.rdb.HDel(ctx, key, strconv.FormatInt(accountID, 10)).Err()
}

func (s *resilienceStore) SessionFailures(ctx context.Context, groupID int64, sessionHash string) (map[int64]int64, error) {
	key := sessionKey(resilienceSessionFailurePrefix, groupID, sessionHash)
	raw, err := s.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(raw))
	for k, v := range raw {
		id, err1 := strconv.ParseInt(k, 10, 64)
		n, err2 := strconv.ParseInt(v, 10, 64)
		if err1 == nil && err2 == nil {
			out[id] = n
		}
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

func (s *resilienceStore) TryLockBalanceProbe(ctx context.Context, accountID int64, ttl time.Duration) (bool, error) {
	return s.rdb.SetNX(ctx, resilienceBalanceLockPrefix+strconv.FormatInt(accountID, 10), "1", ttl).Result()
}

func (s *resilienceStore) UnlockBalanceProbe(ctx context.Context, accountID int64) error {
	return s.rdb.Del(ctx, resilienceBalanceLockPrefix+strconv.FormatInt(accountID, 10)).Err()
}

func (s *resilienceStore) IncrGenerationProbe(ctx context.Context, accountID int64, day string) (int64, error) {
	key := resilienceGenProbePrefix + day + ":" + strconv.FormatInt(accountID, 10)
	return resilienceIncrExpireScript.Run(ctx, s.rdb, []string{key}, int64(48*time.Hour/time.Second)).Int64()
}

func (s *resilienceStore) SetRecovering(ctx context.Context, accountID int64, until time.Time) error {
	return s.rdb.ZAdd(ctx, resilienceRecoveringKey, redis.Z{Score: float64(until.UnixMilli()), Member: strconv.FormatInt(accountID, 10)}).Err()
}

func (s *resilienceStore) ClearRecovering(ctx context.Context, accountID int64) error {
	return s.rdb.ZRem(ctx, resilienceRecoveringKey, strconv.FormatInt(accountID, 10)).Err()
}

func (s *resilienceStore) Recovering(ctx context.Context, now time.Time) (map[int64]time.Time, error) {
	nowMs := now.UnixMilli()
	// Drop ended windows opportunistically.
	_ = s.rdb.ZRemRangeByScore(ctx, resilienceRecoveringKey, "-inf", strconv.FormatInt(nowMs, 10)).Err()
	items, err := s.rdb.ZRangeByScoreWithScores(ctx, resilienceRecoveringKey, &redis.ZRangeBy{Min: strconv.FormatInt(nowMs, 10), Max: "+inf"}).Result()
	if err != nil {
		return nil, err
	}
	out := make(map[int64]time.Time, len(items))
	for _, z := range items {
		member, _ := z.Member.(string)
		if id, err := strconv.ParseInt(member, 10, 64); err == nil {
			out[id] = time.UnixMilli(int64(z.Score))
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
