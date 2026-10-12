package repository

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

var shareUpdateBindingIfOwner = redis.NewScript(`
local owner = redis.call('GET', KEYS[1])
if owner and owner ~= ARGV[1] then return tonumber(owner) end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
return tonumber(ARGV[2])
`)

// UpdateSessionAccountIDIfOwner fences delayed refreshes and migrations so
// they cannot replace or extend a concurrent winning owner's binding.
func (c *gatewayCache) UpdateSessionAccountIDIfOwner(ctx context.Context, groupID int64, key string, expected, accountID int64, ttl time.Duration) (int64, error) {
	return shareUpdateBindingIfOwner.Run(ctx, c.rdb, []string{buildSessionKey(groupID, key)}, expected, accountID, ttl.Milliseconds()).Int64()
}
