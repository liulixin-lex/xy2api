package repository

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Compare at execution time, after any transport delay. Even if a fence has
// expired, a publication whose deadline elapsed cannot recreate the binding.
var createResponseBindingScript = redis.NewScript(`
local now = redis.call('TIME')
if tonumber(now[1]) * 1000000 + tonumber(now[2]) >= tonumber(ARGV[5]) then return 0 end
local previous = redis.call('GET', KEYS[2])
if previous and (not tonumber(previous) or tonumber(previous) >= tonumber(ARGV[2])) then return 0 end
if not previous and redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[4])
return 1
`)
var deleteResponseBindingScript = redis.NewScript(`
local prior = tonumber(redis.call('GET', KEYS[2])) or 0
local generation = math.max(prior + 1, tonumber(ARGV[1]))
local ttl = math.max(tonumber(ARGV[2]), redis.call('PTTL', KEYS[1]), redis.call('PTTL', KEYS[2]))
redis.call('DEL', KEYS[1])
redis.call('SET', KEYS[2], string.format('%.0f', generation), 'PX', ttl)
return 1
`)

func (c *gatewayCache) CreateResponseBinding(ctx context.Context, group int64, key string, account int64, ttl time.Duration) error {
	publication := service.CollabBindingPublicationFrom(ctx)
	return createResponseBindingScript.Run(ctx, c.rdb, sessionBindingKeys(group, key), account, publication.Generation, ttl.Milliseconds(), max(ttl, service.CollabBindingPublicationLifetime).Milliseconds(), publication.Expires.UnixMicro()).Err()
}
func (c *gatewayCache) DeleteResponseBinding(ctx context.Context, group int64, key string) error {
	publication := service.NewCollabBindingPublication()
	return deleteResponseBindingScript.Run(ctx, c.rdb, sessionBindingKeys(group, key), publication.Generation, time.Hour.Milliseconds()).Err()
}

var _ service.CollabResponseBindingCache = (*gatewayCache)(nil)

var setResponseBindingScript = redis.NewScript(`
local prior = tonumber(redis.call('GET', KEYS[2])) or 0
local generation = math.max(prior + 1, tonumber(ARGV[2]))
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
redis.call('SET', KEYS[2], string.format('%.0f', generation), 'PX', ARGV[4])
return 1
`)

func (c *gatewayCache) SetResponseBinding(ctx context.Context, group int64, key string, account int64, ttl time.Duration) error {
	publication := service.NewCollabBindingPublication()
	return setResponseBindingScript.Run(ctx, c.rdb, sessionBindingKeys(group, key), account, publication.Generation, ttl.Milliseconds(), max(ttl, service.CollabBindingPublicationLifetime).Milliseconds()).Err()
}
