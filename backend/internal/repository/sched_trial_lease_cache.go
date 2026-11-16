package repository

import (
	"context"
	"strconv"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const schedTrialLeaseTTL = time.Hour

// Every operation prunes idle members in the same script as its read/write.
// In particular, capacity checking and acquisition cannot interleave across
// gateway instances. Each account/model has one key, shared by every group.
var schedTrialLeaseScript = redis.NewScript(`
redis.replicate_commands()
local key = KEYS[1]
local operation = ARGV[1]
local now = tonumber(ARGV[2])
if not now then
    local clock = redis.call('TIME')
    now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
end
local idle = tonumber(ARGV[3])
local limit = tonumber(ARGV[4])
local ttl = tonumber(ARGV[5])
local member = ARGV[6]
redis.call('ZREMRANGEBYSCORE', key, '-inf', now - idle)

local function finish(result)
    redis.call('EXPIRE', key, ttl)
    return result
end

if operation == 'room' then
    if redis.call('ZSCORE', key, member) or redis.call('ZCARD', key) < limit then
        return finish(1)
    end
    return finish(0)
elseif operation == 'acquire' then
    local score = redis.call('ZSCORE', key, member)
    if score then
        redis.call('ZADD', key, math.max(now, tonumber(score)), member)
        return finish(1)
    end
    if redis.call('ZCARD', key) >= limit then
        return finish(0)
    end
    redis.call('ZADD', key, now, member)
    return finish(1)
elseif operation == 'touch' then
    local score = redis.call('ZSCORE', key, member)
    if score then
        redis.call('ZADD', key, 'XX', math.max(now, tonumber(score)), member)
    end
elseif operation == 'release' then
    redis.call('ZREM', key, member)
elseif operation == 'release_group' then
    for _, value in ipairs(redis.call('ZRANGE', key, 0, -1)) do
        if string.sub(value, 1, string.len(member)) == member then
            redis.call('ZREM', key, value)
        end
    end
end
return finish(0)
`)

type schedTrialLeaseCache struct {
	rdb *redis.Client
}

// NewSchedTrialLeaseCache returns the shared Redis trial capacity store.
// Construction performs no I/O; Redis failures are returned by each operation.
func NewSchedTrialLeaseCache(rdb *redis.Client) service.SchedTrialLeases {
	return &schedTrialLeaseCache{rdb: rdb}
}

func schedTrialLeaseKey(accountID int64, model string) string {
	return "sched:trial:" + strconv.FormatInt(accountID, 10) + ":" + model
}

func schedTrialLeaseGroupPrefix(groupID int64) string {
	return strconv.FormatInt(groupID, 10) + ":"
}

func (c *schedTrialLeaseCache) run(ctx context.Context, operation string, accountID int64, model string, groupID int64, session, now string) (bool, error) {
	result, err := schedTrialLeaseScript.Run(ctx, c.rdb, []string{schedTrialLeaseKey(accountID, model)},
		operation, now, service.TrialLeaseIdle.Milliseconds(), service.TrialLeaseLimit,
		int64(schedTrialLeaseTTL/time.Second), schedTrialLeaseGroupPrefix(groupID)+session).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *schedTrialLeaseCache) Room(ctx context.Context, accountID int64, model string, groupID int64, session string, now time.Time) (bool, error) {
	return c.run(ctx, "room", accountID, model, groupID, session, strconv.FormatInt(now.UnixMilli(), 10))
}

func (c *schedTrialLeaseCache) Acquire(ctx context.Context, accountID int64, model string, groupID int64, session string, now time.Time) (bool, error) {
	return c.run(ctx, "acquire", accountID, model, groupID, session, strconv.FormatInt(now.UnixMilli(), 10))
}

func (c *schedTrialLeaseCache) Touch(ctx context.Context, accountID int64, model string, groupID int64, session string, now time.Time) error {
	_, err := c.run(ctx, "touch", accountID, model, groupID, session, strconv.FormatInt(now.UnixMilli(), 10))
	return err
}

func (c *schedTrialLeaseCache) Release(ctx context.Context, accountID int64, model string, groupID int64, session string) error {
	_, err := c.run(ctx, "release", accountID, model, groupID, session, "")
	return err
}

func (c *schedTrialLeaseCache) ReleaseGroup(ctx context.Context, accountID int64, model string, groupID int64) error {
	_, err := c.run(ctx, "release_group", accountID, model, groupID, "", "")
	return err
}
