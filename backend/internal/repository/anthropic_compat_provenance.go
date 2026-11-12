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
local ttl = tonumber(ARGV[2])
local capacity = tonumber(ARGV[3])
if not ttl or ttl < 1 or ttl ~= math.floor(ttl) or not capacity or capacity < 1 or ARGV[1] == '' then
  return redis.error_reply('PROVENANCE_INVALID invalid compatibility provenance registration')
end
local now = redis.call('TIME')
local ms = now[1] * 1000 + math.floor(now[2] / 1000)
-- Redis scripts do not roll back writes on error. Validate every key and the
-- live capacity before even pruning expired index members.
local live = redis.call('ZCARD', KEYS[1]) - redis.call('ZCOUNT', KEYS[1], '-inf', ms)
local added = 0
local seen = {}
local unique = {}
for i = 2, #KEYS do
  local key = KEYS[i]
  if key == '' or key == KEYS[1] then
    return redis.error_reply('PROVENANCE_INVALID invalid compatibility provenance registration')
  end
  if not seen[key] then
    seen[key] = true
    unique[#unique + 1] = key
    local current = redis.call('GET', key)
    if current and current ~= ARGV[1] then return redis.error_reply('PROVENANCE_CONFLICT provenance constraint conflict') end
    local score = redis.call('ZSCORE', KEYS[1], key)
    if not score or tonumber(score) <= ms then added = added + 1 end
  end
end
if live + added > capacity then return redis.error_reply('PROVENANCE_CAPACITY provenance capacity exceeded') end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ms)
for _, key in ipairs(unique) do
  redis.call('SET', key, ARGV[1], 'PX', ARGV[2])
  redis.call('ZADD', KEYS[1], ms + ttl, key)
end
-- A short-lived registration must not erase capacity accounting for other,
-- longer-lived records. The latest member determines the index lifetime.
local latest = redis.call('ZREVRANGE', KEYS[1], 0, 0, 'WITHSCORES')
if #latest > 0 then redis.call('PEXPIREAT', KEYS[1], tonumber(latest[2])) end
return 1
`)

func (c *gatewayCache) Register(ctx context.Context, keys []string, value []byte, ttl time.Duration) error {
	if len(keys) == 0 {
		return nil
	}
	if len(keys) > 512 || ttl.Milliseconds() < 1 || len(value) == 0 {
		return service.AnthropicCompatRegistrationInvalid
	}
	for _, key := range keys {
		if key == "" || key == anthropicCompatProvenanceIndex {
			return service.AnthropicCompatRegistrationInvalid
		}
	}
	all := make([]string, 1, len(keys)+1)
	all[0] = anthropicCompatProvenanceIndex
	all = append(all, keys...)
	err := registerAnthropicCompatProvenance.Run(ctx, c.rdb, all, string(value), ttl.Milliseconds(), anthropicCompatProvenanceCapacity).Err()
	switch {
	case redis.HasErrorPrefix(err, "PROVENANCE_CONFLICT"):
		return service.AnthropicCompatRegistrationConflict
	case redis.HasErrorPrefix(err, "PROVENANCE_CAPACITY"):
		return service.AnthropicCompatRegistrationCapacity
	case redis.HasErrorPrefix(err, "PROVENANCE_INVALID"):
		return service.AnthropicCompatRegistrationInvalid
	default:
		return err
	}
}

var _ service.AnthropicCompatProvenanceStore = (*gatewayCache)(nil)
