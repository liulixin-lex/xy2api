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
local bindingKeys = tonumber(ARGV[7])
local owner = redis.call('GET', KEYS[1])
local observed = redis.call('GET', KEYS[2]) or ''
if ARGV[1] == '0' then
  if owner then return {'binding_changed', observed, ''} end
  if bindingKeys == 4 and redis.call('EXISTS', KEYS[3]) == 1 then
    return {'binding_changed', redis.call('GET', KEYS[4]) or '', ''}
  end
elseif owner ~= ARGV[1] or observed ~= ARGV[2] then
  return {'binding_changed', observed, ''}
end
if ARGV[9] == '1' then
  local expiry = tonumber(redis.call('ZSCORE', KEYS[#KEYS-1], ARGV[3]))
  if expiry then
    local now = tonumber(ARGV[11])
    local reset = tonumber(ARGV[10])
    local written = tonumber(redis.call('HGET', KEYS[#KEYS], ARGV[3])) or 0
    if expiry >= now and written >= reset then return {'target_avoided', observed, ''} end
  end
end
if ARGV[8] == '1' then
  local ztype = redis.call('TYPE', KEYS[bindingKeys + 1]).ok
  local htype = redis.call('TYPE', KEYS[bindingKeys + 2]).ok
  if (ztype ~= 'none' and ztype ~= 'zset') or (htype ~= 'none' and htype ~= 'hash') then
    return redis.error_reply('invalid sticky failure scope type')
  end
end
redis.call('SET', KEYS[1], ARGV[3], 'PX', ARGV[5])
redis.call('SET', KEYS[2], ARGV[4], 'PX', ARGV[5])
if bindingKeys == 4 and redis.call('EXISTS', KEYS[3]) == 1 then
  redis.call('SET', KEYS[3], ARGV[3], 'PX', ARGV[6])
  redis.call('SET', KEYS[4], ARGV[4], 'PX', ARGV[6])
end
if ARGV[8] == '1' then
  redis.call('ZREM', KEYS[bindingKeys + 1], ARGV[3])
  redis.call('HDEL', KEYS[bindingKeys + 2], ARGV[3])
end
return {'committed', observed, ARGV[4]}
`)

func (c *gatewayCache) CommitSessionBinding(ctx context.Context, groupID int64, sessionHash string, expected service.StickySessionBinding, target int64, ttl time.Duration, options service.BindingCommitOptions) (service.BindingCommitResult, error) {
	if expected.AccountID < 0 || (expected.AccountID == 0) != (expected.Generation == "") || target <= 0 || sessionHash == "" || ttl.Milliseconds() <= 0 {
		return service.BindingCommitResult{Status: service.BindingCommitError}, errors.New("invalid versioned sticky binding replacement")
	}
	keys := sessionBindingKeys(groupID, sessionHash)
	if expected.LegacyKey != "" && expected.LegacyKey != sessionHash {
		keys = append(keys, sessionBindingKeys(groupID, expected.LegacyKey)...)
	}
	bindingKeys := len(keys)
	for _, scope := range []string{options.FailureScope, options.RejectScope} {
		if scope != "" {
			keys = append(keys, sessionKey(resilienceSessionAvoidPrefix, groupID, scope), sessionKey(resilienceSessionAvoidWrittenPrefix, groupID, scope))
		}
	}
	values, err := commitSessionBindingScript.Run(ctx, c.rdb, keys, expected.AccountID, expected.Generation, target, uuid.NewString(),
		ttl.Milliseconds(), min(ttl, 10*time.Minute).Milliseconds(), bindingKeys, options.FailureScope != "", options.RejectScope != "", avoidResetMillis([]time.Time{options.TargetResetAt}), time.Now().UnixMilli()).StringSlice()
	if err != nil {
		return service.BindingCommitResult{Status: service.BindingCommitError}, err
	}
	return service.BindingCommitResult{Status: service.BindingCommitStatus(values[0]), ObservedGen: values[1], NewGen: values[2]}, nil
}

var _ service.OpenAIStickyVersionedCache = (*gatewayCache)(nil)

// The failure and the binding check share one Redis operation, so a late
// failure cannot resurrect avoidance after a newer binding generation.
var recordSessionBindingFailureScript = redis.NewScript(`
if redis.call('GET', KEYS[2]) ~= ARGV[4] or redis.call('GET', KEYS[3]) ~= ARGV[5] then return 0 end
` + resilienceAvoidAddLua)

func (c *gatewayCache) RecordSessionBindingFailure(ctx context.Context, groupID int64, bindingKey, avoidSession string, expected service.StickySessionBinding, target int64, ttl time.Duration, resetAt ...time.Time) (int64, error) {
	if expected.AccountID <= 0 || expected.Generation == "" || target <= 0 || bindingKey == "" || avoidSession == "" || ttl <= 0 {
		return 0, errors.New("invalid versioned sticky binding failure")
	}
	keys := append([]string{sessionKey(resilienceSessionAvoidPrefix, groupID, avoidSession)}, sessionBindingKeys(groupID, bindingKey)...)
	keys = append(keys, sessionKey(resilienceSessionAvoidWrittenPrefix, groupID, avoidSession))
	now := time.Now()
	return recordSessionBindingFailureScript.Run(ctx, c.rdb, keys, target, now.Add(ttl).UnixMilli(), now.UnixMilli(), expected.AccountID, expected.Generation, avoidResetMillis(resetAt)).Int64()
}
