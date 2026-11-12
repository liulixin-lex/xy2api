package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Chat continuations have their own bounded namespace. The stored assistant
// blocks are authenticated ciphertext; aliases contain only a record key.
const anthropicCompatChatIndex = service.AnthropicCompatChatKeyPrefix + "index"
const anthropicCompatChatSizes = service.AnthropicCompatChatKeyPrefix + "sizes"
const anthropicCompatChatBytes = service.AnthropicCompatChatKeyPrefix + "bytes"
const anthropicCompatChatCapacity = 10000
const anthropicCompatChatByteCapacity = 256 << 20

func validAnthropicCompatChatKey(key, kind string) bool {
	prefix := service.AnthropicCompatChatKeyPrefix + kind + ":"
	if !strings.HasPrefix(key, prefix) || len(key) != len(prefix)+64 {
		return false
	}
	value := strings.TrimPrefix(key, prefix)
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func validAnthropicCompatChatAlias(key string) bool {
	return validAnthropicCompatChatKey(key, "h") || validAnthropicCompatChatKey(key, "c")
}

func (c *gatewayCache) LookupChat(ctx context.Context, keys []string) ([][]byte, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if len(keys) > 256 {
		return nil, fmt.Errorf("chat continuation lookup limit exceeded")
	}
	for _, key := range keys {
		if !validAnthropicCompatChatAlias(key) {
			return nil, service.AnthropicCompatRegistrationInvalid
		}
	}
	aliases, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	turnKeys := make([]string, 0, len(keys))
	positions := make(map[string]int)
	for _, value := range aliases {
		if value == nil {
			continue
		}
		key, ok := value.(string)
		if !ok || !validAnthropicCompatChatKey(key, "t") {
			return nil, fmt.Errorf("invalid Chat continuation alias")
		}
		if _, found := positions[key]; !found {
			positions[key] = len(turnKeys)
			turnKeys = append(turnKeys, key)
		}
	}
	out := make([][]byte, len(keys))
	if len(turnKeys) == 0 {
		return out, nil
	}
	// Fetch bounded batches and share each immutable byte slice among aliases.
	// A 128-call parallel turn must not decrypt/copy its 1 MiB record 128 times.
	values := make([][]byte, len(turnKeys))
	totalBytes := 0
	for start := 0; start < len(turnKeys); start += 8 {
		batch, err := c.rdb.MGet(ctx, turnKeys[start:min(start+8, len(turnKeys))]...).Result()
		if err != nil {
			return nil, err
		}
		for i, value := range batch {
			if value == nil {
				continue
			}
			raw, ok := value.(string)
			if !ok || len(raw) > service.AnthropicCompatChatRecordLimit {
				return nil, fmt.Errorf("invalid encrypted Chat continuation")
			}
			totalBytes += len(raw)
			if totalBytes > 8*service.AnthropicCompatChatRecordLimit {
				return nil, fmt.Errorf("chat restored history exceeds byte limit")
			}
			values[start+i] = []byte(raw)
		}
	}
	for i, alias := range aliases {
		if alias == nil {
			continue
		}
		key, ok := alias.(string)
		if !ok {
			return nil, fmt.Errorf("invalid Chat continuation alias")
		}
		out[i] = values[positions[key]]
	}
	return out, nil
}

// Validate conflicts and live capacity before every mutation: Redis scripts do
// not roll back partial writes on failure. Each parallel tool aliases one turn.
var registerAnthropicCompatChat = redis.NewScript(`
local ttl = tonumber(ARGV[2])
local capacity = tonumber(ARGV[3])
local byteCapacity = tonumber(ARGV[4])
local size = tonumber(ARGV[5])
if not ttl or ttl < 1 or not capacity or not byteCapacity or not size or size < 1 or ARGV[1] == '' or ARGV[6] == '' then
  return redis.error_reply('CHAT_INVALID invalid Chat continuation')
end
local function keyType(key)
  local value = redis.call('TYPE', key)
  if type(value) == 'table' then return value.ok end
  return value
end
local expected = {'zset', 'hash', 'string'}
for i = 1, 3 do
  local typ = keyType(KEYS[i])
  if typ ~= 'none' and typ ~= expected[i] then return redis.error_reply('CHAT_INVALID invalid Chat index type') end
end
local now = redis.call('TIME')
local ms = now[1] * 1000 + math.floor(now[2] / 1000)
local expired = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ms)
local expiredBytes = 0
for _, key in ipairs(expired) do expiredBytes = expiredBytes + tonumber(redis.call('HGET', KEYS[2], key) or '0') end
local live = redis.call('ZCARD', KEYS[1]) - #expired
local bytes = tonumber(redis.call('GET', KEYS[3]) or '0') - expiredBytes
local old = redis.call('GET', KEYS[4])
if old and old ~= ARGV[1] then return redis.error_reply('CHAT_CONFLICT immutable Chat turn changed') end
local score = tonumber(redis.call('ZSCORE', KEYS[1], KEYS[4]) or '0')
local added = 1
if score > ms then
  added = 0
  if tonumber(redis.call('HGET', KEYS[2], KEYS[4]) or '0') ~= size or redis.call('HGET', KEYS[2], KEYS[4] .. ':aliases') ~= ARGV[6] then return redis.error_reply('CHAT_CONFLICT Chat alias set changed') end
end
local seen = {}
for i = 5, #KEYS do
  if seen[KEYS[i]] then return redis.error_reply('CHAT_INVALID duplicate Chat handle') end
  seen[KEYS[i]] = true
  local current = redis.call('GET', KEYS[i])
  if current and current ~= KEYS[4] then return redis.error_reply('CHAT_CONFLICT Chat handle collision') end
end
if live + added > capacity or bytes + added * size > byteCapacity then return redis.error_reply('CHAT_CAPACITY Chat continuation capacity exceeded') end
for _, key in ipairs(expired) do redis.call('HDEL', KEYS[2], key, key .. ':aliases') end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ms)
redis.call('SET', KEYS[4], ARGV[1], 'PX', ttl)
for i = 5, #KEYS do redis.call('SET', KEYS[i], KEYS[4], 'PX', ttl) end
redis.call('HSET', KEYS[2], KEYS[4], size)
redis.call('HSET', KEYS[2], KEYS[4] .. ':aliases', ARGV[6])
redis.call('ZADD', KEYS[1], ms + ttl, KEYS[4])
redis.call('SET', KEYS[3], bytes + added * size)
local latest = redis.call('ZREVRANGE', KEYS[1], 0, 0, 'WITHSCORES')
if #latest > 0 then
  for i = 1, 3 do redis.call('PEXPIREAT', KEYS[i], tonumber(latest[2])) end
end
return 1
`)

func (c *gatewayCache) RegisterChat(ctx context.Context, turnKey string, handles []string, value []byte, ttl time.Duration) error {
	if !validAnthropicCompatChatKey(turnKey, "t") || len(handles) == 0 || len(handles) > 128 || len(value) == 0 || len(value) > service.AnthropicCompatChatRecordLimit || ttl.Milliseconds() < 1 || ttl > 24*time.Hour {
		return service.AnthropicCompatRegistrationInvalid
	}
	keys := []string{anthropicCompatChatIndex, anthropicCompatChatSizes, anthropicCompatChatBytes, turnKey}
	size := len(value) + len(turnKey)
	for _, handle := range handles {
		if !validAnthropicCompatChatAlias(handle) {
			return service.AnthropicCompatRegistrationInvalid
		}
		keys = append(keys, handle)
		size += len(handle) + len(turnKey)
	}
	ordered := append([]string(nil), handles...)
	sort.Strings(ordered)
	aliasDigest := sha256.Sum256([]byte(strings.Join(ordered, "\n")))
	err := registerAnthropicCompatChat.Run(ctx, c.rdb, keys, string(value), ttl.Milliseconds(), anthropicCompatChatCapacity, anthropicCompatChatByteCapacity, size, hex.EncodeToString(aliasDigest[:])).Err()
	switch {
	case redis.HasErrorPrefix(err, "CHAT_CONFLICT"):
		return service.AnthropicCompatRegistrationConflict
	case redis.HasErrorPrefix(err, "CHAT_CAPACITY"):
		return service.AnthropicCompatRegistrationCapacity
	case redis.HasErrorPrefix(err, "CHAT_INVALID"):
		return service.AnthropicCompatRegistrationInvalid
	default:
		return err
	}
}

var _ service.AnthropicCompatChatStore = (*gatewayCache)(nil)
