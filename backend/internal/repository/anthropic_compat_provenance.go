package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// A single bounded index is shared by all gateway instances. Values contain no
// raw signatures or request bodies. Expiry and capacity changes are atomic.
const anthropicCompatProvenanceIndex = "anthropic_compat:provenance:v1:index"
const anthropicCompatProvenanceCapacity = 1000000

func (c *gatewayCache) HMACSeed() []byte { return c.anthropicCompatSeed }
func (c *gatewayCache) HMACSeeds() [][]byte {
	seeds := [][]byte{c.anthropicCompatSeed}
	if len(c.anthropicCompatPreviousSeed) >= 32 {
		seeds = append(seeds, c.anthropicCompatPreviousSeed)
	}
	return seeds
}

func (c *gatewayCache) Lookup(ctx context.Context, keys []string) ([][]byte, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if len(keys) > 256 {
		return nil, fmt.Errorf("compatibility provenance lookup limit exceeded")
	}
	values, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	result := make([][]byte, len(values))
	for i, value := range values {
		if value == nil {
			continue
		}
		raw, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("invalid compatibility provenance value")
		}
		result[i] = []byte(raw)
	}
	return result, nil
}

var registerAnthropicCompatProvenance = redis.NewScript(`
local now = redis.call('TIME')
local ms = now[1] * 1000 + math.floor(now[2] / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ms)
local added = 0
for i = 2, #KEYS do
  local current = redis.call('GET', KEYS[i])
  if current and current ~= ARGV[1] then return redis.error_reply('provenance constraint conflict') end
  if not redis.call('ZSCORE', KEYS[1], KEYS[i]) then added = added + 1 end
end
if redis.call('ZCARD', KEYS[1]) + added > tonumber(ARGV[3]) then return redis.error_reply('provenance capacity exceeded') end
for i = 2, #KEYS do
  redis.call('SET', KEYS[i], ARGV[1], 'PX', ARGV[2])
  redis.call('ZADD', KEYS[1], ms + tonumber(ARGV[2]), KEYS[i])
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)

func (c *gatewayCache) Register(ctx context.Context, keys []string, value []byte, ttl time.Duration) error {
	if len(keys) == 0 {
		return nil
	}
	if len(keys) > 512 || ttl <= 0 || len(value) == 0 {
		return fmt.Errorf("invalid compatibility provenance registration")
	}
	all := make([]string, 1, len(keys)+1)
	all[0] = anthropicCompatProvenanceIndex
	all = append(all, keys...)
	return registerAnthropicCompatProvenance.Run(ctx, c.rdb, all, string(value), ttl.Milliseconds(), anthropicCompatProvenanceCapacity).Err()
}

var _ service.AnthropicCompatProvenanceStore = (*gatewayCache)(nil)
