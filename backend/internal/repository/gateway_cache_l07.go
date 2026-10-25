package repository

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// l07StatePrefix keys the L-07 busy segment of a sticky session (cache DESIGN
// §6.1A.2): fields owner, first_busy_ts (unix ms) and busy_n. It is written
// only when a busy owner is kept and lives twice max_hold_seconds.
const l07StatePrefix = "l07:"

var errL07StateValue = errors.New("l07: unexpected state value")

func buildL07StateKey(groupID int64, sessionHash string) string {
	return l07StatePrefix + strconv.FormatInt(groupID, 10) + ":" + sessionHash
}

// L07ReadBusy reads the session's busy segment and the resilience failure
// count of the session on the owner in one round trip.
func (c *gatewayCache) L07ReadBusy(ctx context.Context, groupID int64, sessionHash string, owner int64) (service.L07BusyState, error) {
	id := strconv.FormatInt(owner, 10)
	pipe := c.rdb.Pipeline()
	seg := pipe.HMGet(ctx, buildL07StateKey(groupID, sessionHash), "owner", "first_busy_ts", "busy_n")
	failures := pipe.HMGet(ctx, sessionKey(resilienceSessionFailurePrefix, groupID, sessionHash), id, sessionFailureEndField+id)
	if _, err := pipe.Exec(ctx); err != nil {
		return service.L07BusyState{}, err
	}
	var out service.L07BusyState
	values := append(seg.Val(), failures.Val()...)
	targets := []*int64{&out.Owner, &out.FirstBusyMs, &out.BusyN, &out.FailCount, &out.FailEndMs}
	if len(values) != len(targets) {
		return service.L07BusyState{}, errL07StateValue
	}
	for i, v := range values {
		if v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return service.L07BusyState{}, errL07StateValue
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return service.L07BusyState{}, err
		}
		*targets[i] = n
	}
	return out, nil
}

// L07WriteBusy records a kept busy owner of the session.
func (c *gatewayCache) L07WriteBusy(ctx context.Context, groupID int64, sessionHash string, owner, firstBusyMs, busyN int64, ttl time.Duration) error {
	key := buildL07StateKey(groupID, sessionHash)
	_, err := c.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, key, "owner", owner, "first_busy_ts", firstBusyMs, "busy_n", busyN)
		pipe.PExpire(ctx, key, ttl)
		return nil
	})
	return err
}

// L07RefreshSessionTTLClear is RefreshSessionTTL of an owner hit, with the
// end of the session's busy segment in the same pipeline.
func (c *gatewayCache) L07RefreshSessionTTLClear(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) (bool, error) {
	pipe := c.rdb.Pipeline()
	pipe.Expire(ctx, buildSessionKey(groupID, sessionHash), ttl)
	del := pipe.Del(ctx, buildL07StateKey(groupID, sessionHash))
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	return del.Val() > 0, nil
}

// L07Clear ends the session's busy segment.
func (c *gatewayCache) L07Clear(ctx context.Context, groupID int64, sessionHash string) (bool, error) {
	n, err := c.rdb.Del(ctx, buildL07StateKey(groupID, sessionHash)).Result()
	return n > 0, err
}

var _ service.L07StateStore = (*gatewayCache)(nil)
