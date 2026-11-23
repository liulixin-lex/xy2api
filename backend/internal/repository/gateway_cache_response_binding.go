package repository

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// The reservation uses the same metadata key that every ordinary set replaces
// and every delete removes. Expiry, deletion and replacement all invalidate it.
var reserveResponseBindingsScript = redis.NewScript(`
for _, key in ipairs(KEYS) do
 redis.call('SET', key, ARGV[1], 'PX', ARGV[2])
end
return 1
`)

func (c *gatewayCache) ReserveResponseBindings(ctx context.Context, group int64, keys []string, token string) error {
	metadata := make([]string, len(keys))
	for i, key := range keys {
		metadata[i] = buildSessionGenerationKey(group, key)
	}
	return reserveResponseBindingsScript.Run(ctx, c.rdb, metadata, "pending:"+token, service.CollabBindingPublicationLifetime.Milliseconds()).Err()
}

var createResponseBindingScript = redis.NewScript(`
if redis.call('GET', KEYS[2]) ~= ARGV[2] then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
redis.call('SET', KEYS[2], ARGV[4], 'PX', ARGV[3])
return 1
`)

func (c *gatewayCache) CreateResponseBinding(ctx context.Context, group int64, key string, account int64, ttl time.Duration) error {
	publication := service.CollabBindingPublicationFrom(ctx)
	if publication.Token == "" {
		return nil
	}
	return createResponseBindingScript.Run(ctx, c.rdb, sessionBindingKeys(group, key), account, "pending:"+publication.Token, ttl.Milliseconds(), "published:"+publication.Token).Err()
}
func (c *gatewayCache) DeleteResponseBinding(ctx context.Context, group int64, key string) error {
	return c.DeleteSessionAccountID(ctx, group, key)
}
func (c *gatewayCache) SetResponseBinding(ctx context.Context, group int64, key string, account int64, ttl time.Duration) error {
	return c.SetSessionAccountID(ctx, group, key, account, ttl)
}

var _ service.CollabResponseBindingCache = (*gatewayCache)(nil)
