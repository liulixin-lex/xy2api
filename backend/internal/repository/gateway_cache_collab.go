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

const collabCachePrefix = "collab:records:v1:"

func collabCacheKeys(scope resilience.CollabRecordScope) []string {
	b, _ := json.Marshal(scope)
	key := collabCachePrefix + string(b)
	return []string{collabCachePrefix + "trees", key, key + ":expiry"}
}

// One atomic mutation, with bounded cleanup and no global scan. Hash rows are
// scoped by tenant/group/tree. Each tree and the registry have a hard bound.
const collabApplySource = `
local now=tonumber(ARGV[1])
local deadline=tonumber(ARGV[2])
local field=ARGV[3]
local kind=ARGV[4]
redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',now)
local expired=redis.call('ZRANGEBYSCORE',KEYS[3],'-inf',now)
for _,f in ipairs(expired) do redis.call('HDEL',KEYS[2],f) end
redis.call('ZREMRANGEBYSCORE',KEYS[3],'-inf',now)
local old=redis.call('HGET',KEYS[2],field)
if kind=='confirm' or kind=='void' then
 if not old then return 0 end
 local row=cjson.decode(old)
 local incoming=cjson.decode(ARGV[5])
 if row.Token.Call~=incoming.Token.Call or row.Token.Generation~=incoming.Token.Generation then return 0 end
 if kind=='void' then row.Issued.Status='void'
 elseif row.Issued.Status=='void' then return 0
 else row.Issued.Status='confirmed' end
 redis.call('HSET',KEYS[2],field,cjson.encode(row))
 return 1
end
if old then return 1 end
if deadline<=now then return 0 end
if not redis.call('ZSCORE',KEYS[1],KEYS[2]) and redis.call('ZCARD',KEYS[1])>=tonumber(ARGV[6]) then return 0 end
if redis.call('HLEN',KEYS[2])>=tonumber(ARGV[7]) then return 0 end
redis.call('HSET',KEYS[2],field,ARGV[5])
redis.call('ZADD',KEYS[3],deadline,field)
local current=tonumber(redis.call('ZSCORE',KEYS[1],KEYS[2]) or '0')
local treeDeadline=math.max(current,deadline)
redis.call('ZADD',KEYS[1],treeDeadline,KEYS[2])
redis.call('PEXPIREAT',KEYS[2],treeDeadline)
redis.call('PEXPIREAT',KEYS[3],treeDeadline)
return 1
`

var collabLookupScript = redis.NewScript(`
local expired=redis.call('ZRANGEBYSCORE',KEYS[2],'-inf',ARGV[1])
for _,f in ipairs(expired) do redis.call('HDEL',KEYS[1],f) end
redis.call('ZREMRANGEBYSCORE',KEYS[2],'-inf',ARGV[1])
return redis.call('HVALS',KEYS[1])
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
			scope, field, expiry := resilience.CollabRecordStorageIdentity(m)
			if scope.Tenant == "" || scope.Group == "" || scope.Tree == "" || field == "" {
				continue
			}
			now := time.Now()
			if m.Kind == resilience.CollabRecordMutationLocalEnding && expiry.IsZero() {
				expiry = now.Add(resilience.CollabRecordTTL)
				m.Issued.ExpiresAt = expiry
			}
			if expiry.After(now.Add(resilience.CollabRecordTTL)) {
				expiry = now.Add(resilience.CollabRecordTTL)
			}
			if m.Kind != resilience.CollabRecordMutationConfirm && m.Kind != resilience.CollabRecordMutationVoid {
				// Reuse validation so direct shared calls cannot introduce malformed evidence.
				if !resilience.NewMemoryCollabRecordsStore().ApplyMutation(m) {
					continue
				}
			}
			payload, encodeErr := json.Marshal(m)
			if encodeErr != nil {
				return encodeErr
			}
			// EVAL, rather than EVALSHA, also works after Redis restarts mid-pipeline.
			pipe.Eval(ctx, collabApplySource, collabCacheKeys(scope), now.UnixMilli(), expiry.UnixMilli(), field, string(m.Kind), string(payload), resilience.CollabRecordMaxTrees, resilience.CollabRecordMaxTreeEntries)
		}
		return nil
	})
	return err
}
func (c *collabSharedCache) LookupBatch(ctx context.Context, b resilience.CollabRecordBatch) (resilience.CollabRecordBatchResult, error) {
	if !b.Records && !b.Dispositions {
		return resilience.CollabRecordBatchResult{}, nil
	}
	keys := collabCacheKeys(b.Scope)
	values, err := collabLookupScript.Run(ctx, c.rdb, keys[1:], strconv.FormatInt(time.Now().UnixMilli(), 10)).StringSlice()
	if err != nil {
		return resilience.CollabRecordBatchResult{}, err
	}
	local := resilience.NewMemoryCollabRecordsStore()
	for _, value := range values {
		var mutation resilience.CollabRecordMutation
		if json.Unmarshal([]byte(value), &mutation) != nil {
			continue
		}
		scope, _, _ := resilience.CollabRecordStorageIdentity(mutation)
		if scope == b.Scope {
			local.ApplyMutation(mutation)
		}
	}
	return local.LookupBatch(ctx, b), nil
}
