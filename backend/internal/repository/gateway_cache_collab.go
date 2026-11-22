package repository

import (
	"context"
	"encoding/json"
	"errors"
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
	return []string{collabCachePrefix + "trees", key, key + ":expiry", collabCachePrefix + "evictable", tenant, tenant + ":evictable", key + ":protected", key + ":writes", collabCachePrefix + "tenants"}
}

// One atomic mutation, with registry cleanup bounded by the tree cap. Hash rows are
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
local share=tonumber(ARGV[13])
local sharedLimit=tonumber(ARGV[14])
local byteLimits={tonumber(ARGV[15]),tonumber(ARGV[16]),tonumber(ARGV[17])}
local function part(k)
 if k=='local_ending' then return 2 end
 if k=='pending' or k=='confirm' or k=='void' or k=='plaintext' or k=='consumed' then return 0 end
 return 1
end
local partition=part(kind)
local function charge(f,v) return string.len(v)+3*string.len(f)+32 end
local function tenantKey(tree)
 local scope=cjson.decode(string.sub(tree,string.len(ARGV[11])+1))
 return ARGV[11]..'tenant:'..scope.Tenant
end
local function dropTree(victim)
 local tenant=tenantKey(victim)
 redis.call('DEL',victim,victim..':expiry',victim..':protected',victim..':writes')
 redis.call('ZREM',KEYS[1],victim);redis.call('ZREM',KEYS[4],victim)
 redis.call('ZREM',tenant,victim);redis.call('ZREM',tenant..':evictable',victim)
 local n=redis.call('ZCARD',tenant)
 if n==0 then redis.call('HDEL',KEYS[9],tenant) else redis.call('HSET',KEYS[9],tenant,n) end
end
local function evict(registry,excessOnly)
 local victims=redis.call('ZRANGE',registry,0,-1)
 for _,victim in ipairs(victims) do
  local tenant=tenantKey(victim)
  if victim~=KEYS[2] and redis.call('EXISTS',victim..':protected')==0 and (not excessOnly or tenant==KEYS[5] or redis.call('ZCARD',tenant)>share) then
   dropTree(victim);return true
  end
 end
 return false
end
local function admit()
local tenantCount=redis.call('ZCARD',KEYS[5])
local sharedUsed=0
for _,n in ipairs(redis.call('HVALS',KEYS[9])) do sharedUsed=sharedUsed+math.max(0,tonumber(n)-share) end
if not redis.call('ZSCORE',KEYS[1],KEYS[2]) then
 if tenantCount>=maxTenant then
  if not evict(KEYS[6],false) then return false end
 elseif tenantCount>=share and sharedUsed>=sharedLimit then
  if not evict(KEYS[4],true) then return false end
 elseif redis.call('ZCARD',KEYS[1])>=maxTrees then
  if not evict(KEYS[4],false) then return false end
 end
end
return true
end
local function repair()
 if redis.call('HLEN',KEYS[2])==0 then return true end
 if not admit() then return false end
 local stored=redis.call('HGETALL',KEYS[2])
 local last=0;local protected=0
 for i=1,#stored,2 do
  local row=cjson.decode(stored[i+1]);local f=stored[i]
  local untilTime=tonumber(redis.call('ZSCORE',KEYS[3],f) or '0')
  if untilTime<=0 then untilTime=math.max(deadline,now+1);redis.call('ZADD',KEYS[3],untilTime,f) end
  last=math.max(last,untilTime)
  if part(row.Kind)~=0 or row.Kind=='void' or (row.Issued and row.Issued.Status=='void') then
   protected=math.max(protected,untilTime);redis.call('ZREM',KEYS[8],f)
  elseif not redis.call('ZSCORE',KEYS[8],f) then redis.call('ZADD',KEYS[8],now,f) end
 end
 redis.call('ZADD',KEYS[1],last,KEYS[2]);redis.call('ZADD',KEYS[5],last,KEYS[2])
 redis.call('HSET',KEYS[9],KEYS[5],redis.call('ZCARD',KEYS[5]))
 redis.call('PEXPIREAT',KEYS[2],last);redis.call('PEXPIREAT',KEYS[3],last);redis.call('PEXPIREAT',KEYS[8],last)
 if protected>now then redis.call('SET',KEYS[7],protected);redis.call('PEXPIREAT',KEYS[7],protected) else redis.call('DEL',KEYS[7]) end
 local eligible=math.max(now,protected)
 redis.call('ZADD',KEYS[4],eligible,KEYS[2]);redis.call('ZADD',KEYS[6],eligible,KEYS[2])
 local newest=redis.call('ZREVRANGE',KEYS[5],0,0,'WITHSCORES')
 if #newest>0 then redis.call('PEXPIREAT',KEYS[5],newest[2]);redis.call('PEXPIREAT',KEYS[6],newest[2]) end
 return true
end
local expected={'zset','hash','zset','zset','zset','zset','string','zset','hash'}
for i,key in ipairs(KEYS) do
 local typ=redis.call('TYPE',key).ok
 if typ~='none' and typ~=expected[i] then return redis.error_reply('invalid collaboration key type') end
end
local function apply()
local dead=redis.call('ZRANGEBYSCORE',KEYS[1],'-inf',now)
for _,victim in ipairs(dead) do dropTree(victim) end
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
local function same(a,b)
 if type(a)~=type(b) then return false end
 if type(a)~='table' then return a==b end
 for k,v in pairs(a) do if not same(v,b[k]) then return false end end
 for k,_ in pairs(b) do if a[k]==nil then return false end end
 return true
end
local function protect(untilTime)
 local protection=math.max(tonumber(redis.call('GET',KEYS[7]) or '0'),untilTime)
 redis.call('SET',KEYS[7],protection);redis.call('PEXPIREAT',KEYS[7],protection)
 -- Eligibility is indexed by the protection deadline, so expiry needs no write.
 redis.call('ZADD',KEYS[4],protection,KEYS[2]);redis.call('ZADD',KEYS[6],protection,KEYS[2])
end
if kind=='confirm' then
 if redis.call('HEXISTS',KEYS[2],'v:')==1 or redis.call('HEXISTS',KEYS[2],'v:'..cjson.decode(value).Token.Token)==1 then return 0 end
 if not old then return 0 end
 local row=cjson.decode(old);local incoming=cjson.decode(value)
 if row.Token.Call~=incoming.Token.Call or row.Token.Generation~=incoming.Token.Generation then return 0 end
 if row.Issued.Status=='void' then return 0 end
 row.Issued.Status='confirmed'
 local updated=cjson.encode(row)
 if string.len(updated)>maxRowBytes or sizes[1]-charge(field,old)+charge(field,updated)>byteLimits[1] then return 0 end
 redis.call('HSET',KEYS[2],field,updated)
 return 1
end
local pending={}
if ARGV[12]~='' then pending=cjson.decode(ARGV[12]) end
if old and kind~='local_ending' then
 local row=cjson.decode(old)
 if kind=='terminated' then
  local incoming=cjson.decode(value)
  incoming.Terminated.ExpiresAt=row.Terminated.ExpiresAt
  if not same(incoming.Terminated,row.Terminated) then return 0 end
  row.Reserved=nil;redis.call('HSET',KEYS[2],field,cjson.encode(row))
 end
 return 1
end
if deadline<=now or string.len(value)>maxRowBytes then return 0 end
local extraCount={0,0,0};local extraSize={0,0,0}
if not old then extraCount[partition+1]=1;extraSize[partition+1]=charge(field,value) end
local additions={}
for _,item in ipairs(pending) do
 local existing=redis.call('HGET',KEYS[2],item.field)
 if existing then
  local prior=cjson.decode(existing);local incoming=cjson.decode(item.value)
  incoming.Terminated.ExpiresAt=prior.Terminated.ExpiresAt
  if not same(incoming.Terminated,prior.Terminated) then return 0 end
 else
  extraCount[2]=extraCount[2]+1;extraSize[2]=extraSize[2]+charge(item.field,item.value)
  table.insert(additions,item)
 end
end
-- Identical reservations do not refresh protection.
if old and kind=='local_ending' and #additions==0 then return counts[3] end
if counts[3]+extraCount[3]>3 or sizes[3]+extraSize[3]>byteLimits[3] then return 0 end
if counts[2]+extraCount[2]>maxRows or sizes[2]+extraSize[2]>byteLimits[2] then return 0 end
if partition==0 then
 local evidence=redis.call('ZRANGE',KEYS[8],0,-1)
 for _,f in ipairs(evidence) do
  if counts[1]+extraCount[1]<=maxRows and sizes[1]+extraSize[1]<=byteLimits[1] then break end
  local oldValue=redis.call('HGET',KEYS[2],f)
  if oldValue then
   local row=cjson.decode(oldValue)
   if row.Kind~='void' and (not row.Issued or row.Issued.Status~='void') then
    redis.call('HDEL',KEYS[2],f);redis.call('ZREM',KEYS[3],f);redis.call('ZREM',KEYS[8],f)
    counts[1]=counts[1]-1;sizes[1]=sizes[1]-charge(f,oldValue)
   end
  end
 end
end
if kind=='void' and (counts[1]+extraCount[1]>maxRows or sizes[1]+extraSize[1]>byteLimits[1]) then
 for i=1,#rows,2 do
  if part(cjson.decode(rows[i+1]).Kind)==0 then redis.call('HDEL',KEYS[2],rows[i]);redis.call('ZREM',KEYS[3],rows[i]);redis.call('ZREM',KEYS[8],rows[i]) end
 end
 local row=cjson.decode(value);row.Token.Token='';row.Token.Call='';row.Token.Generation=0;value=cjson.encode(row);field='v:'
 counts[1]=0;sizes[1]=0;extraCount[1]=1;extraSize[1]=charge(field,value)
end
if counts[1]+extraCount[1]>maxRows or sizes[1]+extraSize[1]>byteLimits[1] then return 0 end
if not admit() then return 0 end
if not old then
 redis.call('ZADD',KEYS[3],deadline,field);redis.call('HSET',KEYS[2],field,value)
 if partition==0 then redis.call('ZADD',KEYS[8],now,field) end
end
for _,item in ipairs(additions) do
 redis.call('ZADD',KEYS[3],deadline,item.field);redis.call('HSET',KEYS[2],item.field,item.value)
end
if kind=='void' then
 local token=cjson.decode(value).Token
 for i=1,#rows,2 do
  local row=cjson.decode(rows[i+1])
  if row.Kind=='pending' and (token.Token=='' or row.Token.Token==token.Token) then row.Issued.Status='void';redis.call('HSET',KEYS[2],rows[i],cjson.encode(row));redis.call('ZREM',KEYS[8],rows[i]) end
 end
end
local current=tonumber(redis.call('ZSCORE',KEYS[1],KEYS[2]) or '0')
local treeDeadline=math.max(current,deadline)
redis.call('ZADD',KEYS[1],treeDeadline,KEYS[2]);redis.call('ZADD',KEYS[5],treeDeadline,KEYS[2])
redis.call('HSET',KEYS[9],KEYS[5],redis.call('ZCARD',KEYS[5]))
redis.call('PEXPIREAT',KEYS[2],treeDeadline);redis.call('PEXPIREAT',KEYS[3],treeDeadline);redis.call('PEXPIREAT',KEYS[8],treeDeadline)
if partition~=0 then protect(deadline)
elseif redis.call('EXISTS',KEYS[7])==0 then
 redis.call('ZADD',KEYS[4],now,KEYS[2]);redis.call('ZADD',KEYS[6],now,KEYS[2])
end
local newest=redis.call('ZREVRANGE',KEYS[5],0,0,'WITHSCORES')
if #newest>0 then redis.call('PEXPIREAT',KEYS[5],newest[2]);redis.call('PEXPIREAT',KEYS[6],newest[2]) end
if kind=='local_ending' then return counts[3]+extraCount[3] end
return 1
end
local ok,result=pcall(apply)
if ok and type(result)=='number' and result>0 then
 local repaired,complete=pcall(repair)
 if repaired and complete then return result end
 ok=false;result='collaboration metadata repair failed'
end
-- Runtime errors do not roll back Lua writes. Never leave an immortal hash.
if redis.call('EXISTS',KEYS[2])==1 and redis.call('PTTL',KEYS[2])<0 then
 local expiry=math.max(deadline,now+1)
 redis.call('PEXPIREAT',KEYS[2],expiry);redis.call('PEXPIREAT',KEYS[3],expiry);redis.call('PEXPIREAT',KEYS[8],expiry)
end
if not ok then return redis.error_reply(tostring(result)) end
return result

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
  local n=redis.call('ZCARD',tenant)
  if n==0 then redis.call('HDEL',KEYS[9],tenant) else redis.call('HSET',KEYS[9],tenant,n) end
 end
 redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',ARGV[1]);redis.call('ZREMRANGEBYSCORE',KEYS[5],'-inf',ARGV[1])
 local function eligible(registry,excessOnly)
  for _,victim in ipairs(redis.call('ZRANGE',registry,0,-1)) do
   local scope=cjson.decode(string.sub(victim,string.len(ARGV[4])+1))
   local tenant=ARGV[4]..'tenant:'..scope.Tenant
   if redis.call('EXISTS',victim..':protected')==0 and (not excessOnly or tenant==KEYS[5] or redis.call('ZCARD',tenant)>tonumber(ARGV[5])) then return true end
  end
  return false
 end
 local sharedUsed=0
 for _,n in ipairs(redis.call('HVALS',KEYS[9])) do sharedUsed=sharedUsed+math.max(0,tonumber(n)-tonumber(ARGV[5])) end
 if redis.call('ZCARD',KEYS[5])>=tonumber(ARGV[3]) then
  if not eligible(KEYS[6],false) then known='0' end
 elseif redis.call('ZCARD',KEYS[5])>=tonumber(ARGV[5]) and sharedUsed>=tonumber(ARGV[6]) then
  if not eligible(KEYS[4],true) then known='0' end
 elseif redis.call('ZCARD',KEYS[1])>=tonumber(ARGV[2]) and not eligible(KEYS[4],false) then known='0' end
end
local rows=redis.call('HVALS',KEYS[2]);table.insert(rows,1,known);return rows
`)

func (c *collabSharedCache) Apply(ctx context.Context, m resilience.CollabRecordMutation) error {
	return c.ApplyBatch(ctx, []resilience.CollabRecordMutation{m})
}
func (c *collabSharedCache) ApplyBatch(ctx context.Context, batch []resilience.CollabRecordMutation) error {
	if len(batch) > 64 {
		return errCollabAdmission
	}
	// Pipeline keeps order, amortizes a slow round trip and has no per-write goroutines.
	var commands []*redis.Cmd
	invalid := false
	_, err := c.rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, m := range batch {
			if m.Kind == resilience.CollabRecordMutationPending {
				m.Issued.Status = resilience.CollabIssuedStatusPending
			}

			now := time.Now()
			normalized, payload, ok := resilience.NormalizeCollabRecordMutation(m, now)
			if !ok {
				invalid = true
				continue
			}
			scope, field, expiry := resilience.CollabRecordStorageIdentity(normalized)
			if scope.Tenant == "" || scope.Group == "" || scope.Tree == "" || field == "" {
				invalid = true
				continue
			}

			// EVAL, rather than EVALSHA, also works after Redis restarts mid-pipeline.
			commands = append(commands, pipe.Eval(ctx, collabApplySource, collabCacheKeys(scope), collabApplyArgs(now, expiry, field, m.Kind, payload, "")...))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if invalid {
		return errCollabAdmission
	}
	for _, cmd := range commands {
		n, err := cmd.Int()
		if err != nil {
			return err
		}
		if n == 0 {
			return errCollabAdmission
		}
	}
	return nil
}
func (c *collabSharedCache) LookupBatch(ctx context.Context, b resilience.CollabRecordBatch) (resilience.CollabRecordBatchResult, error) {
	if ctx.Err() != nil {
		return resilience.CollabRecordBatchResult{}, ctx.Err()
	}
	if !b.Records && !b.Dispositions {
		return resilience.CollabRecordBatchResult{}, nil
	}
	keys := collabCacheKeys(b.Scope)
	values, err := collabLookupScript.Run(ctx, c.rdb, keys, strconv.FormatInt(time.Now().UnixMilli(), 10), resilience.CollabRecordMaxTrees, resilience.CollabRecordMaxTenantTrees, collabCachePrefix, resilience.CollabRecordTenantShare, resilience.CollabRecordSharedTrees).StringSlice()
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
	count, err := c.rdb.Eval(ctx, collabApplySource, collabCacheKeys(scope), collabApplyArgs(now, expiry, field, m.Kind, payload, "")...).Int()
	if err != nil || count < 1 {
		return resilience.CollabTreeLocalEndingLimit, false
	}
	return count, true
}

var errCollabAdmission = errors.New("collaboration record admission refused")

func collabApplyArgs(now, expiry time.Time, field string, kind resilience.CollabRecordMutationKind, payload []byte, pending string) []any {
	return []any{now.UnixMilli(), expiry.UnixMilli(), field, string(kind), string(payload), resilience.CollabRecordMaxTrees, resilience.CollabRecordMaxTreeEntries, resilience.CollabRecordMaxRowBytes, resilience.CollabRecordMaxTreeBytes, resilience.CollabRecordMaxTenantTrees, collabCachePrefix, pending, resilience.CollabRecordTenantShare, resilience.CollabRecordSharedTrees, resilience.CollabRecordEvidenceBytes, resilience.CollabRecordDispositionBytes, resilience.CollabRecordCounterBytes}
}
func (c *collabSharedCache) ReserveLocalEnding(ctx context.Context, records []resilience.CollabTerminatedRecord) (int, bool) {
	now := time.Now()
	rows, ok := resilience.PrepareCollabLocalEnding(records, now)
	if !ok {
		return resilience.CollabTreeLocalEndingLimit, false
	}
	type pendingRow struct {
		Field string `json:"field"`
		Value string `json:"value"`
	}
	pending := make([]pendingRow, 0, len(rows)-1)
	for _, m := range rows[1:] {
		_, field, _ := resilience.CollabRecordStorageIdentity(m)
		payload, _ := json.Marshal(m)
		pending = append(pending, pendingRow{field, string(payload)})
	}
	payload, _ := json.Marshal(rows[0])
	holds, _ := json.Marshal(pending)
	scope, field, expiry := resilience.CollabRecordStorageIdentity(rows[0])
	count, err := c.rdb.Eval(ctx, collabApplySource, collabCacheKeys(scope), collabApplyArgs(now, expiry, field, rows[0].Kind, payload, string(holds))...).Int()
	if err != nil || count < 1 {
		return resilience.CollabTreeLocalEndingLimit, false
	}
	return count, true
}

// WriteInvalidations uses its own caller deadline and never waits behind the
// best-effort observation writer. This also permits a bounded shutdown flush.
func (c *collabSharedCache) WriteInvalidations(ctx context.Context, rows []resilience.CollabRecordMutation) error {
	for _, row := range rows {
		if row.Kind != resilience.CollabRecordMutationVoid {
			return errCollabAdmission
		}
	}
	return c.ApplyBatch(ctx, rows)
}
