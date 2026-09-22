package repository

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const qualityRoutingPrefix = "openai_quality_routing:"

var _ service.OpenAIQualityRoutingStore = (*gatewayCache)(nil)

func (c *gatewayCache) ReadOpenAIQualityRouting(ctx context.Context, scope string, group int64, sticky []string) (*service.OpenAIQualityState, []int64, error) {
	keys := []string{qualityRoutingPrefix + scope}
	for _, key := range sticky {
		keys = append(keys, buildSessionKey(group, key))
	}
	values, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, nil, err
	}
	var state *service.OpenAIQualityState
	if value, ok := values[0].(string); ok {
		state = &service.OpenAIQualityState{}
		if err := json.Unmarshal([]byte(value), state); err != nil {
			return nil, nil, err
		}
	}
	ids := make([]int64, len(sticky))
	for i, v := range values[1:] {
		if value, ok := v.(string); ok {
			ids[i], _ = strconv.ParseInt(value, 10, 64)
		}
	}
	return state, ids, nil
}

// Generation checks discard both duplicate evidence and callbacks from older
// requests. Binding uses compare-and-set so concurrent replacements cannot
// overwrite the first successful replacement in the same generation.
var qualityRoutingUpdate = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
local c = cjson.decode(ARGV[1])
local s = {revision=0, generation=0, binding=0, avoid={}, rotation='', expires=0}
if raw then s = cjson.decode(raw) end
if s.expires <= c.now then s = {revision=0, generation=0, binding=0, avoid={}, rotation='', expires=0} end
if s.generation ~= c.generation then return cjson.encode(s) end
if c.kind == 'evidence' then
  s.owner = c.account_id
  s.generation = s.generation + 1
  s.binding = 0
  s.rotation = c.rotation
  s.avoid[tostring(c.account_id)] = {provider=c.provider, at=c.now, ['until']=c['until']}
  s.expires = c.expires
elseif c.kind == 'bind' and s.generation > 0 and (s.binding == c.previous_binding or s.binding == c.account_id) then
  s.binding = c.account_id
  s.expires = c.expires
else
  return cjson.encode(s)
end
s.revision = (s.revision or 0) + 1
local result = cjson.encode(s)
redis.call('SET', KEYS[1], result, 'PX', ARGV[2])
return result
`)

func (c *gatewayCache) UpdateOpenAIQualityRouting(ctx context.Context, scope string, change service.OpenAIQualityChange, ttl time.Duration) (*service.OpenAIQualityState, error) {
	payload, err := json.Marshal(change)
	if err != nil {
		return nil, err
	}
	result, err := qualityRoutingUpdate.Run(ctx, c.rdb.WithTimeout(50*time.Millisecond), []string{qualityRoutingPrefix + scope}, payload, ttl.Milliseconds()).Text()
	if err != nil {
		return nil, err
	}
	state := &service.OpenAIQualityState{}
	if err := json.Unmarshal([]byte(result), state); err != nil {
		return nil, err
	}
	return state, nil
}
