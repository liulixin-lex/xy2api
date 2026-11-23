package repository

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Run the ordinary scripts against a thread's effective binding. Only binding
// reads inherit; every write targets the thread. The parent may receive
// EXPIRE only. Legacy keys never participate. The wrapper and the original
// script are one Redis call.
const stickyThreadPrelude = `
local native = redis
local parent, parentGen, deleted = unpack(KEYS, #KEYS-2)
for i=1,3 do table.remove(KEYS) end
local index = tonumber(ARGV[#ARGV-2])
local noneTTL = tonumber(ARGV[#ARGV-1])
local refreshTTL = tonumber(ARGV[#ARGV])
for i=1,3 do table.remove(ARGV) end
local key, gen = KEYS[index], KEYS[index+1]
local own = native.call('GET', key)
local detached = own == '-1' or native.call('EXISTS', deleted) == 1
local source, sourceGen = key, gen
if not own and not detached then source, sourceGen = parent, parentGen end
local changed = false
local redis = {error_reply = native.error_reply}
redis.call = function(command, ...)
  local args = {...}
  local k = args[1]
  if k ~= key and k ~= gen then return native.call(command, unpack(args)) end
  if command == 'GET' then
    if own == '-1' then return false end
    if k == key then return native.call('GET', source) end
    local value = native.call('GET', sourceGen)
    if not own and not value then
      local owner = native.call('GET', source)
      if owner then return 'inherited:' .. owner end
    end
    return value
  elseif command == 'EXISTS' then
    if own == '-1' then return 0 end
    return native.call('EXISTS', k == key and source or sourceGen)
  elseif command == 'PTTL' then
    return native.call('PTTL', k == key and source or sourceGen)
  elseif command == 'SET' then
    if k == key then
      -- An explicit-none marker is absence to the ordinary claim script.
      if own == '-1' then native.call('DEL', key, gen) end
      own, source, sourceGen, changed = tostring(args[2]), key, gen, true
    end
    return native.call(command, unpack(args))
  elseif command == 'PEXPIRE' then
    if k == key then changed = true end
    if not own then return 1 end
    return native.call(command, unpack(args))
  elseif command == 'DEL' then
    local existed = redis.call('EXISTS', key)
    native.call('SET', key, '-1', 'PX', noneTTL)
    native.call('DEL', gen)
    own, source, sourceGen, changed = '-1', key, gen, true
    return existed
  end
  return native.call(command, unpack(args))
end
local function operation()
`

const stickyThreadEpilogue = `
end
local result = operation()
if type(result) == 'table' and result.err then return result end
if changed and refreshTTL > 0 then
  native.call('PEXPIRE', parent, refreshTTL)
  native.call('PEXPIRE', parentGen, refreshTTL)
end
-- A binding's expiry must not restore an owner that this thread replaced or
-- deleted. Keep that boundary separately from the ordinary provisional TTL.
-- It also covers the parent's remaining lifetime when promotion refreshes it.
if changed and own then
  local ttl = math.max(noneTTL, native.call('PTTL', key), native.call('PTTL', parent), native.call('PTTL', deleted))
  native.call('SET', deleted, '1', 'PX', ttl)
  detached = true
end
local value = native.call('GET', key)
local ttl = native.call('PTTL', key)
if not value and detached then
  value, ttl = '-1', native.call('PTTL', deleted)
end
local pairMissing = value == '-1' or (not value and not native.call('GET', parent))
return {result or false, value or false, ttl, pairMissing and 1 or 0}
`

type stickyBindingScript struct {
	*redis.Script
	thread *redis.Script
}

func newStickyBindingScript(source string) *stickyBindingScript {
	return &stickyBindingScript{Script: redis.NewScript(source), thread: redis.NewScript(stickyThreadPrelude + source + stickyThreadEpilogue)}
}

func (c *gatewayCache) runStickyBinding(ctx context.Context, script *stickyBindingScript, keys []string, args ...any) *redis.Cmd {
	b, ok := service.StickyThreadBindingFrom(ctx)
	if !ok {
		return script.Run(ctx, c.rdb, keys, args...)
	}
	// The failure script puts its avoid key before the binding pair.
	index := 1
	if script == recordSessionBindingFailureScript || script == recordFirstOutputAvoidScript {
		index = 2
	}
	// Derive the group-prefixed parent keys from this operation's binding key.
	prefix := keys[index-1][:len(keys[index-1])-len(b.Key)]
	genPrefix := keys[index][:len(keys[index])-len(b.Key)]
	keys = append(keys, prefix+b.Parent, genPrefix+b.Parent, prefix+b.Key+":deleted")
	args = append(args, index, b.TTL.Milliseconds(), b.RefreshTTL.Milliseconds())
	cmd := script.thread.Run(ctx, c.rdb, keys, args...)
	values, err := cmd.Slice()
	if err != nil {
		return cmd
	}
	if b.Observe != nil {
		var account int64
		if values[1] != nil {
			value := redis.NewCmd(ctx)
			value.SetVal(values[1])
			account, _ = value.Int64()
		}
		b.Observe(account, time.Duration(values[2].(int64))*time.Millisecond, values[1] != nil, values[3].(int64) == 1)
	}
	cmd.SetVal(values[0])
	if values[0] == nil {
		cmd.SetErr(redis.Nil)
	}
	return cmd
}
