package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// The per-attempt state key is also a tombstone. A release that overtakes a
// registration (lost ACK/cancelled connection) prevents its late resurrection.
var shareRegisterWait = redis.NewScript(`
redis.replicate_commands()
local t = redis.call('TIME')
local now = tonumber(t[1])*1000 + math.floor(tonumber(t[2])/1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
local state = redis.call('GET', KEYS[2])
if state == 'released' then return 0 end
if state == 'registered' then
  return redis.call('ZSCORE', KEYS[1], ARGV[1]) ~= false and 1 or 0
end
local legacy = tonumber(redis.call('GET', KEYS[3]) or '0')
if redis.call('ZCARD', KEYS[1]) + math.max(legacy, 0) >= tonumber(ARGV[2]) then return 0 end
redis.call('SET', KEYS[2], 'registered', 'PX', ARGV[3])
redis.call('ZADD', KEYS[1], now + tonumber(ARGV[3]), ARGV[1])
redis.call('PEXPIRE', KEYS[1], math.max(tonumber(ARGV[3]), redis.call('PTTL', KEYS[1])))
return 1
`)

var shareReleaseWait = redis.NewScript(`
redis.call('SET', KEYS[2], 'released', 'PX', ARGV[2])
return redis.call('ZREM', KEYS[1], ARGV[1])
`)

func shareWaitKeys(accountID int64, attempt string) []string {
	base := fmt.Sprintf("wait:share:{%d}", accountID)
	return []string{base, base + ":" + attempt, fmt.Sprintf("%s%d", accountWaitKeyPrefix, accountID)}
}

func (c *concurrencyCache) RegisterAccountWait(ctx context.Context, accountID int64, attempt string, maxWaiting int, ttl time.Duration) (bool, error) {
	n, err := shareRegisterWait.Run(ctx, c.rdb, shareWaitKeys(accountID, attempt), attempt, maxWaiting, ttl.Milliseconds()).Int64()
	return n == 1, err
}

func (c *concurrencyCache) ReleaseAccountWait(ctx context.Context, accountID int64, attempt string, ttl time.Duration) error {
	return shareReleaseWait.Run(ctx, c.rdb, shareWaitKeys(accountID, attempt)[:2], attempt, ttl.Milliseconds()).Err()
}
