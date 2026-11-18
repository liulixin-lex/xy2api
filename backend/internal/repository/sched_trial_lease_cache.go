package repository

import (
	"context"
	"log/slog"
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
elseif operation == 'promote' then
    local target = ARGV[7]
    local score = redis.call('ZSCORE', key, target)
    redis.call('ZREM', key, member)
    redis.call('ZADD', key, math.max(now, tonumber(score) or now), target)
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

func (c *schedTrialLeaseCache) run(ctx context.Context, operation string, accountID int64, model string, groupID int64, session, now string, promoted ...string) (bool, error) {
	args := []any{operation, now, service.TrialLeaseIdle.Milliseconds(), service.TrialLeaseLimit, int64(schedTrialLeaseTTL / time.Second), schedTrialLeaseGroupPrefix(groupID) + session}
	if len(promoted) > 0 {
		args = append(args, schedTrialLeaseGroupPrefix(groupID)+promoted[0])
	}
	result, err := schedTrialLeaseScript.Run(ctx, c.rdb, []string{schedTrialLeaseKey(accountID, model)}, args...).Int()
	if err != nil {
		return false, err
	}
	if result == 0 && (operation == "acquire" || operation == "room") {
		c.recordFull(ctx, accountID, model, groupID, now)
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

func (c *schedTrialLeaseCache) Promote(ctx context.Context, a int64, m string, g int64, request, session string, now time.Time) error {
	_, err := c.run(ctx, "promote", a, m, g, request, strconv.FormatInt(now.UnixMilli(), 10), session)
	return err
}

// Retain observations beyond a one-hour trial and its next evaluation.
const schedTrialFullTTL = 24 * time.Hour

var schedTrialPeekRoomScript = redis.NewScript(`
local holders = redis.call('ZCOUNT', KEYS[1], '(' .. ARGV[1], '+inf')
return holders < tonumber(ARGV[2]) and 1 or 0
`)

var schedTrialRecordFullScript = redis.NewScript(`
local previous = redis.call('HGET', KEYS[1], ARGV[1])
if not previous or tonumber(previous) < tonumber(ARGV[2]) then
    redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
    redis.call('EXPIRE', KEYS[1], ARGV[3])
end
return 1
`)

func schedTrialFullKey(accountID int64, model string) string {
	return "sched:trial_full:" + strconv.FormatInt(accountID, 10) + ":" + model
}

func (c *schedTrialLeaseCache) recordFull(ctx context.Context, accountID int64, model string, groupID int64, nowMs string) {
	// Observability is best effort and must not turn a full seat into a dispatch error.
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	err := schedTrialRecordFullScript.Run(ctx, c.rdb, []string{schedTrialFullKey(accountID, model)},
		groupID, nowMs, int64(schedTrialFullTTL/time.Second)).Err()
	if err != nil {
		slog.WarnContext(ctx, "sched_quality.trial_full_record_failed", "account_id", accountID, "group_id", groupID, "error", err)
	}
}

func (c *schedTrialLeaseCache) PeekRoom(ctx context.Context, accountID int64, model string, now time.Time) (bool, error) {
	result, err := schedTrialPeekRoomScript.Run(ctx, c.rdb, []string{schedTrialLeaseKey(accountID, model)},
		now.UnixMilli()-service.TrialLeaseIdle.Milliseconds(), service.TrialLeaseLimit).Int()
	return result == 1, err
}

func (c *schedTrialLeaseCache) LastFull(ctx context.Context, accountID int64, model string, groupID int64) (time.Time, error) {
	ms, err := c.rdb.HGet(ctx, schedTrialFullKey(accountID, model), strconv.FormatInt(groupID, 10)).Int64()
	if err == redis.Nil {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms).UTC(), nil
}
