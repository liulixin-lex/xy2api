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

// Read/adopt the primary and reconcile an existing alias in one operation.
// Adoption preserves the remaining lifetime, so reading is not an idle reset.
var resolveSessionBindingScript = redis.NewScript(`
local owner = redis.call('GET', KEYS[1])
local legacy = redis.call('GET', KEYS[3])
if not owner then
  if ARGV[2] ~= '1' or not legacy then return nil end
  owner = legacy
  local ttl = redis.call('PTTL', KEYS[3])
  redis.call('SET', KEYS[1], owner)
  redis.call('SET', KEYS[2], ARGV[1])
  if ttl >= 0 then
    redis.call('PEXPIRE', KEYS[1], ttl)
    redis.call('PEXPIRE', KEYS[2], ttl)
  end
end
local generation = redis.call('GET', KEYS[2])
local ttl = redis.call('PTTL', KEYS[1])
if not generation then
  generation = ARGV[1]
  redis.call('SET', KEYS[2], generation)
  if ttl >= 0 then redis.call('PEXPIRE', KEYS[2], ttl) end
end
if legacy then
  local aliasTTL = redis.call('PTTL', KEYS[3])
  if ttl >= 0 and (aliasTTL < 0 or ttl < aliasTTL) then aliasTTL = ttl end
  redis.call('SET', KEYS[3], owner)
  redis.call('SET', KEYS[4], generation)
  if aliasTTL >= 0 then
    redis.call('PEXPIRE', KEYS[3], aliasTTL)
    redis.call('PEXPIRE', KEYS[4], aliasTTL)
  end
end
return {owner, generation}
`)

func (c *gatewayCache) ResolveSessionBinding(ctx context.Context, group int64, primary, legacy string, fallback bool) (service.StickySessionBinding, error) {
	flag := 0
	if fallback {
		flag = 1
	}
	keys := append(sessionBindingKeys(group, primary), sessionBindingKeys(group, legacy)...)
	values, err := resolveSessionBindingScript.Run(ctx, c.rdb, keys, uuid.NewString(), flag).StringSlice()
	if errors.Is(err, redis.Nil) {
		return service.StickySessionBinding{}, service.ErrStickySessionNotFound
	}
	if err != nil {
		return service.StickySessionBinding{}, err
	}
	owner, err := strconv.ParseInt(values[0], 10, 64)
	binding := service.StickySessionBinding{AccountID: owner, Generation: values[1], LegacyKey: legacy}
	return binding, err
}

// The two service writes may straddle a newer successful CAS. Copy only the
// current primary and its generation; an old writer cannot restore its owner.
var syncSessionBindingAliasScript = redis.NewScript(`
local owner = redis.call('GET', KEYS[1])
if not owner or (ARGV[1] ~= '0' and owner ~= ARGV[1]) then return 0 end
if ARGV[3] ~= '1' and redis.call('EXISTS', KEYS[3]) == 0 then return 0 end
local generation = redis.call('GET', KEYS[2])
local primaryTTL = redis.call('PTTL', KEYS[1])
if not generation then
  generation = ARGV[4]
  redis.call('SET', KEYS[2], generation)
  if primaryTTL >= 0 then redis.call('PEXPIRE', KEYS[2], primaryTTL) end
end
local ttl = tonumber(ARGV[2])
if primaryTTL >= 0 then ttl = math.min(ttl, primaryTTL) end
redis.call('SET', KEYS[3], owner)
redis.call('SET', KEYS[4], generation)
redis.call('PEXPIRE', KEYS[3], ttl)
redis.call('PEXPIRE', KEYS[4], ttl)
return 1
`)

func (c *gatewayCache) SyncSessionBindingAlias(ctx context.Context, group int64, primary, legacy string, owner int64, ttl time.Duration, create bool) error {
	flag := 0
	if create {
		flag = 1
	}
	keys := append(sessionBindingKeys(group, primary), sessionBindingKeys(group, legacy)...)
	return syncSessionBindingAliasScript.Run(ctx, c.rdb, keys, owner, ttl.Milliseconds(), flag, uuid.NewString()).Err()
}

var _ service.OpenAIStickyAliasCache = (*gatewayCache)(nil)

var deleteSessionBindingAliasesScript = redis.NewScript(`
if ARGV[1] ~= '0' then
  local owner = redis.call('GET', KEYS[1]) or redis.call('GET', KEYS[3])
  if owner ~= ARGV[1] then return 0 end
end
return redis.call('DEL', unpack(KEYS))
`)

func (c *gatewayCache) DeleteSessionBindingAliases(ctx context.Context, group int64, primary, legacy string, owner int64) (bool, error) {
	keys := append(sessionBindingKeys(group, primary), sessionBindingKeys(group, legacy)...)
	n, err := deleteSessionBindingAliasesScript.Run(ctx, c.rdb, keys, owner).Int64()
	return n > 0, err
}
