package repository

import (
	"context"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// The reservation uses the same metadata key that every ordinary set replaces
// and every delete removes. Expiry, deletion and replacement all invalidate it.
var reserveResponseBindingsScript = redis.NewScript(`
for i = 1, #KEYS, 2 do
 local metadata = redis.call('GET', KEYS[i+1])
 if metadata then
  local owner = string.match(metadata, '^pending:([^:]+):') or string.match(metadata, '^published:([^:]+):')
  if owner ~= ARGV[3] then return 0 end
 elseif redis.call('EXISTS', KEYS[i]) == 1 then
  return 0
 end
end
for i = 2, #KEYS, 2 do
 redis.call('SET', KEYS[i], ARGV[1], 'PX', ARGV[2])
end
return 1
`)

func (c *gatewayCache) ReserveResponseBindings(ctx context.Context, group int64, keys []string, token string) error {
	metadata := make([]string, 0, 2*len(keys))
	for _, key := range keys {
		metadata = append(metadata, sessionBindingKeys(group, key)...)
	}
	publisher, _, _ := strings.Cut(token, ":")
	return reserveResponseBindingsScript.Run(ctx, c.rdb, metadata, "pending:"+token, service.CollabBindingPublicationLifetime.Milliseconds(), publisher).Err()
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
