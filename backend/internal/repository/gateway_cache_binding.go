package repository

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

var readSessionBindingScript = redis.NewScript(`
local owner = redis.call('GET', KEYS[1])
if not owner then return nil end
local generation = redis.call('GET', KEYS[2])
if not generation then
  generation = ARGV[1]
  redis.call('SET', KEYS[2], generation)
  local ttl = redis.call('PTTL', KEYS[1])
  if ttl >= 0 then redis.call('PEXPIRE', KEYS[2], ttl) end
end
return {owner, generation}
`)

func (c *gatewayCache) GetSessionBinding(ctx context.Context, groupID int64, sessionHash string) (service.StickySessionBinding, error) {
	values, err := readSessionBindingScript.Run(ctx, c.rdb, sessionBindingKeys(groupID, sessionHash), uuid.NewString()).StringSlice()
	if errors.Is(err, redis.Nil) {
		return service.StickySessionBinding{}, service.ErrStickySessionNotFound
	}
	if err != nil {
		return service.StickySessionBinding{}, err
	}
	owner, err := strconv.ParseInt(values[0], 10, 64)
	return service.StickySessionBinding{AccountID: owner, Generation: values[1]}, err
}

var commitSessionBindingScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] or redis.call('GET', KEYS[2]) ~= ARGV[2] then return 0 end
redis.call('SET', KEYS[1], ARGV[3], 'PX', ARGV[5])
redis.call('SET', KEYS[2], ARGV[4], 'PX', ARGV[5])
if #KEYS == 4 and redis.call('EXISTS', KEYS[3]) == 1 then
  redis.call('SET', KEYS[3], ARGV[3], 'PX', ARGV[6])
  redis.call('SET', KEYS[4], ARGV[4], 'PX', ARGV[6])
end
return 1
`)

func (c *gatewayCache) CommitSessionBinding(ctx context.Context, groupID int64, sessionHash string, expected service.StickySessionBinding, target int64, ttl time.Duration) (bool, error) {
	if expected.AccountID <= 0 || expected.Generation == "" || target <= 0 || sessionHash == "" || ttl <= 0 {
		return false, errors.New("invalid versioned sticky binding replacement")
	}
	keys := sessionBindingKeys(groupID, sessionHash)
	if expected.LegacyKey != "" && expected.LegacyKey != sessionHash {
		keys = append(keys, sessionBindingKeys(groupID, expected.LegacyKey)...)
	}
	n, err := commitSessionBindingScript.Run(ctx, c.rdb, keys, expected.AccountID, expected.Generation, target, uuid.NewString(), ttl.Milliseconds(), min(ttl, 10*time.Minute).Milliseconds()).Int64()
	return n == 1, err
}

var _ service.OpenAIStickyVersionedCache = (*gatewayCache)(nil)

// The failure and the binding check share one Redis operation, so a late
// failure cannot resurrect avoidance after a newer binding generation.
var recordSessionBindingFailureScript = redis.NewScript(`
if redis.call('GET', KEYS[2]) ~= ARGV[4] or redis.call('GET', KEYS[3]) ~= ARGV[5] then return 0 end
` + resilienceAvoidAddLua)

func (c *gatewayCache) RecordSessionBindingFailure(ctx context.Context, groupID int64, bindingKey, avoidSession string, expected service.StickySessionBinding, target int64, ttl time.Duration) (bool, error) {
	if expected.AccountID <= 0 || expected.Generation == "" || target <= 0 || bindingKey == "" || avoidSession == "" || ttl <= 0 {
		return false, errors.New("invalid versioned sticky binding failure")
	}
	keys := append([]string{sessionKey(resilienceSessionAvoidPrefix, groupID, avoidSession)}, sessionBindingKeys(groupID, bindingKey)...)
	now := time.Now()
	n, err := recordSessionBindingFailureScript.Run(ctx, c.rdb, keys, target, now.Add(ttl).UnixMilli(), now.UnixMilli(), expected.AccountID, expected.Generation).Int64()
	return n == 1, err
}
