package repository

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	schedTrialLeaseTTL = time.Hour
	// Outlast seat idleness and active trial rounds. A later acquire creates
	// a fresh epoch if the inactive account/model fence has expired.
	schedTrialGenerationTTL = 7 * 24 * time.Hour
)

// Every operation prunes idle members in the same script as its read/write.
// In particular, capacity checking and acquisition cannot interleave across
// gateway instances. Each account/model has one key, shared by every group.
var schedTrialLeaseScript = redis.NewScript(`
redis.replicate_commands()
local key = KEYS[1]
local operation = ARGV[1]
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local age = tonumber(ARGV[2])
local observed = now - age
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
    -- A queued write cannot start a new idle window for an expired decision.
    if age >= idle then return '' end
    local score = redis.call('ZSCORE', key, member)
    if not score and redis.call('ZCARD', key) >= limit then
        return finish('')
    end
    -- The incarnation changes if Redis ever loses the fence hash. Never
    -- recycle a token just because every seat expired or the hash was evicted.
    redis.call('HSETNX', KEYS[2], 'epoch', ARGV[8])
    redis.call('EXPIRE', KEYS[2], ARGV[9])
    local epoch = redis.call('HGET', KEYS[2], 'epoch')
    local generation = redis.call('HGET', KEYS[2], ARGV[7]) or '0'
    redis.call('ZADD', key, math.max(observed, tonumber(score) or observed), member)
    return finish(epoch .. ':' .. generation)
elseif operation == 'promote' then
    local epoch = redis.call('HGET', KEYS[2], 'epoch')
    local generation = redis.call('HGET', KEYS[2], ARGV[8]) or '0'
    if not epoch or ARGV[9] ~= epoch .. ':' .. generation then
        return -1
    end
    local target = ARGV[7]
    local score = redis.call('ZSCORE', key, target)
    redis.call('ZREM', key, member)
    if age < idle then
        redis.call('ZADD', key, math.max(observed, tonumber(score) or observed), target)
    end
elseif operation == 'touch' then
    local score = redis.call('ZSCORE', key, member)
    if score and age < idle then
        redis.call('ZADD', key, 'XX', math.max(observed, tonumber(score)), member)
    end
elseif operation == 'release' then
    redis.call('ZREM', key, member)
elseif operation == 'release_group' then
    if redis.call('HEXISTS', KEYS[2], 'epoch') == 1 then
        redis.call('HINCRBY', KEYS[2], member, 1)
        redis.call('EXPIRE', KEYS[2], ARGV[7])
    end
    for _, value in ipairs(redis.call('ZRANGE', key, 0, -1)) do
        if string.sub(value, 1, string.len(member)) == member then
            redis.call('ZREM', key, value)
        end
    end
end
return finish(0)
`)

type schedTrialLeaseCache struct {
	rdb   *redis.Client
	since func(time.Time) time.Duration
}

// NewSchedTrialLeaseCache returns the shared Redis trial capacity store.
// Construction performs no I/O; Redis failures are returned by each operation.
func NewSchedTrialLeaseCache(rdb *redis.Client) service.SchedTrialLeases {
	return &schedTrialLeaseCache{rdb: rdb, since: time.Since}
}

func schedTrialLeaseKey(accountID int64, model string) string {
	return "sched:trial:" + strconv.FormatInt(accountID, 10) + ":" + model
}

func schedTrialLeaseGroupPrefix(groupID int64) string {
	return strconv.FormatInt(groupID, 10) + ":"
}

func (c *schedTrialLeaseCache) run(ctx context.Context, operation string, accountID int64, model string, groupID int64, session string, elapsed time.Duration, extra ...string) *redis.Cmd {
	// Send elapsed monotonic time, never a gateway wall-clock score. Redis
	// timestamps both pruning and the backdated activity in the same script.
	args := []any{operation, max(elapsed.Milliseconds(), 0), service.TrialLeaseIdle.Milliseconds(), service.TrialLeaseLimit, int64(schedTrialLeaseTTL / time.Second), schedTrialLeaseGroupPrefix(groupID) + session}
	for _, arg := range extra {
		args = append(args, arg)
	}
	keys := []string{schedTrialLeaseKey(accountID, model), "sched:trial-generation:" + strconv.FormatInt(accountID, 10) + ":" + model}
	return schedTrialLeaseScript.Run(ctx, c.rdb, keys, args...)
}

func (c *schedTrialLeaseCache) Room(ctx context.Context, accountID int64, model string, groupID int64, session string, now time.Time) (bool, error) {
	v, err := c.run(ctx, "room", accountID, model, groupID, session, 0).Int()
	if err == nil && v == 0 {
		c.recordFull(ctx, accountID, model, groupID)
	}
	return v == 1, err
}

func (c *schedTrialLeaseCache) Acquire(ctx context.Context, accountID int64, model string, groupID int64, session string, now time.Time) (bool, string, error) {
	elapsed := c.since(now)
	generation, err := c.run(ctx, "acquire", accountID, model, groupID, session, elapsed, schedTrialLeaseGroupPrefix(groupID), uuid.NewString(), strconv.FormatInt(int64(schedTrialGenerationTTL/time.Second), 10)).Text()
	// The lease script also returns an empty token for expired decisions.
	if err == nil && generation == "" && elapsed < service.TrialLeaseIdle {
		c.recordFull(ctx, accountID, model, groupID)
	}
	return generation != "", generation, err
}

func (c *schedTrialLeaseCache) Touch(ctx context.Context, accountID int64, model string, groupID int64, session string, now time.Time) error {
	return c.run(ctx, "touch", accountID, model, groupID, session, c.since(now)).Err()
}

func (c *schedTrialLeaseCache) Release(ctx context.Context, accountID int64, model string, groupID int64, session string) error {
	return c.run(ctx, "release", accountID, model, groupID, session, 0).Err()
}

func (c *schedTrialLeaseCache) ReleaseGroup(ctx context.Context, accountID int64, model string, groupID int64) error {
	return c.run(ctx, "release_group", accountID, model, groupID, "", 0, strconv.FormatInt(int64(schedTrialGenerationTTL/time.Second), 10)).Err()
}

func (c *schedTrialLeaseCache) Promote(ctx context.Context, a int64, m string, g int64, request, session, generation string, now time.Time) error {
	v, err := c.run(ctx, "promote", a, m, g, request, c.since(now), schedTrialLeaseGroupPrefix(g)+session, schedTrialLeaseGroupPrefix(g), generation).Int()
	if err == nil && v == -1 {
		return service.ErrTrialGenerationChanged
	}
	return err
}

// Retain observations beyond a one-hour trial and its next evaluation.
const schedTrialFullTTL = 24 * time.Hour

var schedTrialPeekRoomScript = redis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local holders = redis.call('ZCOUNT', KEYS[1], '(' .. (now - tonumber(ARGV[1])), '+inf')
return holders < tonumber(ARGV[2]) and 1 or 0
`)

var schedTrialRecordFullScript = redis.NewScript(`
redis.replicate_commands()
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local previous = redis.call('HGET', KEYS[1], ARGV[1])
if not previous or tonumber(previous) < now then
    redis.call('HSET', KEYS[1], ARGV[1], now)
    redis.call('EXPIRE', KEYS[1], ARGV[2])
end
return 1
`)

func schedTrialFullKey(accountID int64, model string) string {
	return "sched:trial_full:" + strconv.FormatInt(accountID, 10) + ":" + model
}

func (c *schedTrialLeaseCache) recordFull(ctx context.Context, accountID int64, model string, groupID int64) {
	if !resilience.CollabAttemptFrom(ctx).AccountEffectsAllowed() {
		return
	}
	// Observability is best effort and must not turn a full seat into a dispatch error.
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	err := schedTrialRecordFullScript.Run(ctx, c.rdb, []string{schedTrialFullKey(accountID, model)},
		groupID, int64(schedTrialFullTTL/time.Second)).Err()
	if err != nil {
		slog.WarnContext(ctx, "sched_quality.trial_full_record_failed", "account_id", accountID, "group_id", groupID, "error", err)
	}
}

func (c *schedTrialLeaseCache) PeekRoom(ctx context.Context, accountID int64, model string) (bool, error) {
	result, err := schedTrialPeekRoomScript.Run(ctx, c.rdb, []string{schedTrialLeaseKey(accountID, model)},
		service.TrialLeaseIdle.Milliseconds(), service.TrialLeaseLimit).Int()
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
