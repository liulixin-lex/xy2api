package repository

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/redis/go-redis/v9"
)

// Optional capability: GatewayCache mocks do not need any additional methods.
func (c *gatewayCache) CollabRecords() resilience.CollabSharedRecordsStore {
	return &collabSharedCache{rdb: c.rdb}
}

type collabSharedCache struct{ rdb *redis.Client }

const collabCachePrefix = "collab:records:v2:"

func collabCacheKeys(scope resilience.CollabRecordScope) []string {
	b, _ := json.Marshal(scope)
	key := collabCachePrefix + string(b)
	tenant := collabCachePrefix + "tenant:" + string(scope.Tenant)
	return []string{collabCachePrefix + "trees", key, key + ":expiry", collabCachePrefix + "evictable", tenant, tenant + ":evictable", key + ":protected", key + ":writes"}
}

// One atomic mutation, with bounded cleanup and no global scan. Hash rows are
// scoped by tenant/group/tree. Each tree and the registry have a hard bound.
const collabApplySource = `
local now=tonumber(ARGV[1])
local deadline=tonumber(ARGV[2])
local field=ARGV[3]
local kind=ARGV[4]
local value=ARGV[5]
local maxTrees=tonumber(ARGV[6])
local maxRows=tonumber(ARGV[7])
local maxRowBytes=tonumber(ARGV[8])
local maxTenant=tonumber(ARGV[10])
local function part(k)
 if k=='local_ending' then return 2 end
 if k=='pending' or k=='confirm' or k=='void' or k=='plaintext' or k=='consumed' then return 0 end
 return 1
end
local partition=part(kind)
local function charge(f,v) return string.len(v)+3*string.len(f)+32 end
local dead=redis.call('ZRANGEBYSCORE',KEYS[1],'-inf',now)
for _,victim in ipairs(dead) do
 local scope=cjson.decode(string.sub(victim,string.len(ARGV[11])+1))
 local tenant=ARGV[11]..'tenant:'..scope.Tenant
 redis.call('DEL',victim,victim..':expiry',victim..':protected',victim..':writes')
 redis.call('ZREM',KEYS[4],victim);redis.call('ZREM',tenant,victim);redis.call('ZREM',tenant..':evictable',victim)
end
redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',now)
redis.call('ZREMRANGEBYSCORE',KEYS[5],'-inf',now)
local expired=redis.call('ZRANGEBYSCORE',KEYS[3],'-inf',now)
for _,f in ipairs(expired) do redis.call('HDEL',KEYS[2],f);redis.call('ZREM',KEYS[8],f) end
redis.call('ZREMRANGEBYSCORE',KEYS[3],'-inf',now)
local rows=redis.call('HGETALL',KEYS[2])
local counts={0,0,0}
local sizes={0,0,0}
local old=redis.call('HGET',KEYS[2],field)
for i=1,#rows,2 do
 local r=cjson.decode(rows[i+1]);local p=part(r.Kind)+1
 counts[p]=counts[p]+1;sizes[p]=sizes[p]+charge(rows[i],rows[i+1])
end
if kind=='confirm' or kind=='void' then
 if not old then return 0 end
 local row=cjson.decode(old);local incoming=cjson.decode(value)
 if row.Token.Call~=incoming.Token.Call or row.Token.Generation~=incoming.Token.Generation then return 0 end
 if kind=='void' then row.Issued.Status='void'
 elseif row.Issued.Status=='void' then return 0
 else row.Issued.Status='confirmed' end
 local updated=cjson.encode(row)
 if string.len(updated)>maxRowBytes or sizes[1]-charge(field,old)+charge(field,updated)>16384 then return 0 end
 redis.call('HSET',KEYS[2],field,updated)
 return 1
end
if old then
 if kind=='local_ending' then return counts[3] end
 return 1
end
if deadline<=now or string.len(value)>maxRowBytes then return 0 end
if kind=='local_ending' and counts[3]>=3 then return 0 end
local byteLimits={16384,14336,2048}
local rowLimit=maxRows
if partition==2 then rowLimit=3 end
if partition==0 then
 local evidence=redis.call('ZRANGE',KEYS[8],0,-1)
 for _,f in ipairs(evidence) do
  if counts[1]<rowLimit and sizes[1]+charge(field,value)<=byteLimits[1] then break end
  local oldValue=redis.call('HGET',KEYS[2],f)
  if oldValue then
   redis.call('HDEL',KEYS[2],f);redis.call('ZREM',KEYS[3],f);redis.call('ZREM',KEYS[8],f)
   counts[1]=counts[1]-1;sizes[1]=sizes[1]-charge(f,oldValue)
  end
 end
end
if counts[partition+1]>=rowLimit or sizes[partition+1]+charge(field,value)>byteLimits[partition+1] then return 0 end
local function evict(registry)
 local victims=redis.call('ZRANGE',registry,0,-1)
 for _,victim in ipairs(victims) do
  if redis.call('EXISTS',victim..':protected')==0 then
   local scope=cjson.decode(string.sub(victim,string.len(ARGV[11])+1))
   local tenant=ARGV[11]..'tenant:'..scope.Tenant
   redis.call('DEL',victim,victim..':expiry',victim..':protected',victim..':writes')
   redis.call('ZREM',KEYS[1],victim);redis.call('ZREM',KEYS[4],victim)
   redis.call('ZREM',tenant,victim);redis.call('ZREM',tenant..':evictable',victim)
   return true
  end
  redis.call('ZREM',registry,victim)
 end
 return false
end
if not redis.call('ZSCORE',KEYS[5],KEYS[2]) and redis.call('ZCARD',KEYS[5])>=maxTenant then
 if not evict(KEYS[6]) then return 0 end
end
if not redis.call('ZSCORE',KEYS[1],KEYS[2]) and redis.call('ZCARD',KEYS[1])>=maxTrees then
 if not evict(KEYS[4]) then return 0 end
end
redis.call('HSET',KEYS[2],field,value)
if partition==0 then redis.call('ZADD',KEYS[8],now,field) end
redis.call('ZADD',KEYS[3],deadline,field)
local current=tonumber(redis.call('ZSCORE',KEYS[1],KEYS[2]) or '0')
local treeDeadline=math.max(current,deadline)
redis.call('ZADD',KEYS[1],treeDeadline,KEYS[2]);redis.call('ZADD',KEYS[5],treeDeadline,KEYS[2])
redis.call('PEXPIREAT',KEYS[2],treeDeadline);redis.call('PEXPIREAT',KEYS[3],treeDeadline);redis.call('PEXPIREAT',KEYS[8],treeDeadline)
if partition~=0 then
 local protection=tonumber(redis.call('GET',KEYS[7]) or '0')
 protection=math.max(protection,deadline)
 redis.call('SET',KEYS[7],protection);redis.call('PEXPIREAT',KEYS[7],protection)
 redis.call('ZREM',KEYS[4],KEYS[2]);redis.call('ZREM',KEYS[6],KEYS[2])
elseif redis.call('EXISTS',KEYS[7])==0 then
 redis.call('ZADD',KEYS[4],now,KEYS[2]);redis.call('ZADD',KEYS[6],now,KEYS[2])
end
-- Tenant index metadata expires too, so a succession of tenants stays bounded.
local newest=redis.call('ZREVRANGE',KEYS[5],0,0,'WITHSCORES')
if #newest>0 then redis.call('PEXPIREAT',KEYS[5],newest[2]);redis.call('PEXPIREAT',KEYS[6],newest[2]) end
if kind=='local_ending' then return counts[3]+1 end
return 1
`

var collabLookupScript = redis.NewScript(`
local expired=redis.call('ZRANGEBYSCORE',KEYS[3],'-inf',ARGV[1])
for _,f in ipairs(expired) do redis.call('HDEL',KEYS[2],f);redis.call('ZREM',KEYS[8],f) end
redis.call('ZREMRANGEBYSCORE',KEYS[3],'-inf',ARGV[1])
local known='1'
if redis.call('EXISTS',KEYS[2])==0 then
 local dead=redis.call('ZRANGEBYSCORE',KEYS[1],'-inf',ARGV[1])
 for _,victim in ipairs(dead) do
  local scope=cjson.decode(string.sub(victim,string.len(ARGV[4])+1))
  local tenant=ARGV[4]..'tenant:'..scope.Tenant
  redis.call('ZREM',KEYS[4],victim);redis.call('ZREM',tenant,victim);redis.call('ZREM',tenant..':evictable',victim)
 end
 redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',ARGV[1]);redis.call('ZREMRANGEBYSCORE',KEYS[5],'-inf',ARGV[1])
 if (redis.call('ZCARD',KEYS[5])>=tonumber(ARGV[3]) and redis.call('ZCARD',KEYS[6])==0) or (redis.call('ZCARD',KEYS[1])>=tonumber(ARGV[2]) and redis.call('ZCARD',KEYS[4])==0) then known='0' end
end
local rows=redis.call('HVALS',KEYS[2]);table.insert(rows,1,known);return rows
`)

func (c *collabSharedCache) Apply(ctx context.Context, m resilience.CollabRecordMutation) error {
	return c.ApplyBatch(ctx, []resilience.CollabRecordMutation{m})
}
func (c *collabSharedCache) ApplyBatch(ctx context.Context, batch []resilience.CollabRecordMutation) error {
	// Pipeline keeps order, amortizes a slow round trip and has no per-write goroutines.
	_, err := c.rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, m := range batch {
			if m.Kind == resilience.CollabRecordMutationPending {
				m.Issued.Status = resilience.CollabIssuedStatusPending
			}

			now := time.Now()
			normalized, payload, ok := resilience.NormalizeCollabRecordMutation(m, now)
			if !ok {
				continue
			}
			scope, field, expiry := resilience.CollabRecordStorageIdentity(normalized)
			if scope.Tenant == "" || scope.Group == "" || scope.Tree == "" || field == "" {
				continue
			}

			// EVAL, rather than EVALSHA, also works after Redis restarts mid-pipeline.
			pipe.Eval(ctx, collabApplySource, collabCacheKeys(scope), now.UnixMilli(), expiry.UnixMilli(), field, string(m.Kind), string(payload), resilience.CollabRecordMaxTrees, resilience.CollabRecordMaxTreeEntries, resilience.CollabRecordMaxRowBytes, resilience.CollabRecordMaxTreeBytes, resilience.CollabRecordMaxTenantTrees, collabCachePrefix)
		}
		return nil
	})
	return err
}
func (c *collabSharedCache) LookupBatch(ctx context.Context, b resilience.CollabRecordBatch) (resilience.CollabRecordBatchResult, error) {
	if ctx.Err() != nil {
		return resilience.CollabRecordBatchResult{}, ctx.Err()
	}
	if !b.Records && !b.Dispositions {
		return resilience.CollabRecordBatchResult{}, nil
	}
	keys := collabCacheKeys(b.Scope)
	values, err := collabLookupScript.Run(ctx, c.rdb, keys, strconv.FormatInt(time.Now().UnixMilli(), 10), resilience.CollabRecordMaxTrees, resilience.CollabRecordMaxTenantTrees, collabCachePrefix).StringSlice()
	if err != nil {
		return resilience.CollabRecordBatchResult{}, err
	}
	if len(values) == 0 || values[0] != "1" {
		return resilience.CollabRecordBatchResult{TreeLocalEndings: resilience.CollabTreeLocalEndingLimit}, nil
	}
	values = values[1:]
	local := resilience.NewMemoryCollabRecordsStore()
	complete := true
	for _, value := range values {
		if ctx.Err() != nil {
			return resilience.CollabRecordBatchResult{}, ctx.Err()
		}
		if len(value) > resilience.CollabRecordMaxRowBytes {
			complete = false
			continue
		}
		var mutation resilience.CollabRecordMutation
		if json.Unmarshal([]byte(value), &mutation) != nil {
			complete = false
			continue
		}
		scope, _, _ := resilience.CollabRecordStorageIdentity(mutation)
		if scope == b.Scope {
			if !local.ApplyMutation(mutation) {
				complete = false
			}
		}
	}
	result := local.LookupBatch(ctx, b)
	if !complete {
		result.RecordsComplete = false
		result.DispositionsComplete = false
		result.TreeLocalEndingsKnown = false
		result.TreeLocalEndings = resilience.CollabTreeLocalEndingLimit
	}
	return result, nil
}

// The only shared write allowed to gate a local ending: one atomic bounded
// reservation before delivery, independent of the best-effort write queue.
func (c *collabSharedCache) ReserveTreeLocalEnding(ctx context.Context, scope resilience.CollabRecordScope, event resilience.CollabDigest) (int, bool) {
	now := time.Now()
	m, payload, ok := resilience.NormalizeCollabRecordMutation(resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationLocalEnding, Scope: scope, Event: event}, now)
	if !ok || scope.Tenant == "" || scope.Group == "" || scope.Tree == "" || event == "" {
		return resilience.CollabTreeLocalEndingLimit, false
	}
	_, field, expiry := resilience.CollabRecordStorageIdentity(m)
	count, err := c.rdb.Eval(ctx, collabApplySource, collabCacheKeys(scope), now.UnixMilli(), expiry.UnixMilli(), field, string(m.Kind), string(payload), resilience.CollabRecordMaxTrees, resilience.CollabRecordMaxTreeEntries, resilience.CollabRecordMaxRowBytes, resilience.CollabRecordMaxTreeBytes, resilience.CollabRecordMaxTenantTrees, collabCachePrefix).Int()
	if err != nil || count < 1 {
		return resilience.CollabTreeLocalEndingLimit, false
	}
	return count, true
}
