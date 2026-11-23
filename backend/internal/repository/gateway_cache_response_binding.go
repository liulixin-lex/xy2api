package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Existing bindings and deletion tombstones both fence delayed publications.
var createResponseBindingScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 or redis.call('EXISTS', KEYS[2]) == 1 then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[4])
return 1
`)
var deleteResponseBindingScript = redis.NewScript(`
redis.call('DEL', KEYS[1])
redis.call('SET', KEYS[2], ARGV[1], 'PX', ARGV[2])
return 1
`)

func (c *gatewayCache) CreateResponseBinding(ctx context.Context, group int64, key string, account int64, ttl time.Duration) error {
	return createResponseBindingScript.Run(ctx, c.rdb, sessionBindingKeys(group, key), account, uuid.NewString(), ttl.Milliseconds(), max(ttl, service.CollabBindingPublicationLifetime).Milliseconds()).Err()
}
func (c *gatewayCache) DeleteResponseBinding(ctx context.Context, group int64, key string) error {
	return deleteResponseBindingScript.Run(ctx, c.rdb, sessionBindingKeys(group, key), uuid.NewString(), service.CollabBindingPublicationLifetime.Milliseconds()).Err()
}

var _ service.CollabResponseBindingCache = (*gatewayCache)(nil)

var setResponseBindingScript = redis.NewScript(`
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[4])
return 1
`)

func (c *gatewayCache) SetResponseBinding(ctx context.Context, group int64, key string, account int64, ttl time.Duration) error {
	return setResponseBindingScript.Run(ctx, c.rdb, sessionBindingKeys(group, key), account, uuid.NewString(), ttl.Milliseconds(), max(ttl, service.CollabBindingPublicationLifetime).Milliseconds()).Err()
}
