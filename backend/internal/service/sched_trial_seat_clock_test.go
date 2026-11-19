//go:build unit

package service_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/repository"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Advance the Redis clock between the successful lease command and its independent full-seat write.
type afterLeaseReply struct {
	operation string
	after     func()
}

func (h *afterLeaseReply) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *afterLeaseReply) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *afterLeaseReply) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		args := cmd.Args()
		if err == nil && h.after != nil && (cmd.Name() == "eval" || cmd.Name() == "evalsha") && len(args) > 5 && args[5] == h.operation {
			callback := h.after
			h.after = nil
			callback()
		}
		return err
	}
}

func TestObserveFullSeatAcrossClocks(t *testing.T) {
	const ms = time.Millisecond
	for _, tc := range []struct {
		name, operation                                    string
		sinceStart, betweenSteps, redisOffset, replayDelay time.Duration
		want                                               string
	}{
		{"same_clock_before", "room", -5 * ms, 0, 0, 0, "unknown"},
		{"same_clock_inside", "acquire", 5 * ms, 0, 0, 0, "unknown"},
		{"delayed_room_crosses_start", "room", -5 * ms, 10 * ms, 0, 0, "unknown"},
		{"delayed_acquire_crosses_end", "acquire", q.TrialDuration - 5*ms, 10 * ms, 0, 0, "unknown"},
		{"redis_8ms_ahead_before_start", "room", -5 * ms, 0, 8 * ms, 0, "unknown"},
		{"redis_8ms_ahead_before_deadline", "room", q.TrialDuration - 5*ms, 0, 8 * ms, 0, "unknown"},
		{"redis_8ms_behind_after_deadline", "room", q.TrialDuration + 5*ms, 0, -8 * ms, 0, "unknown"},
		{"redis_8ms_behind_inside", "room", 5 * ms, 0, -8 * ms, 0, "unknown"},
		{"before_trial", "room", -3 * time.Second, 10 * ms, 8 * ms, 0, "not_observed"},
		{"within_trial", "acquire", q.TrialDuration - 3*time.Second, 10 * ms, 8 * ms, 0, "full"},
		{"retention_elapsed", "room", q.TrialDuration - ms, 0, 0, 25 * time.Hour, "not_observed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			at := time.Now().UTC().Truncate(time.Millisecond)
			start := at.Add(-tc.sinceStart)
			end := start.Add(q.TrialDuration)
			srv := miniredis.RunT(t)
			srv.SetTime(at.Add(tc.redisOffset))
			rdb := redis.NewClient(&redis.Options{Addr: srv.Addr(), MaxRetries: -1})
			t.Cleanup(func() { _ = rdb.Close() })
			leases := repository.NewSchedTrialLeaseCache(rdb)
			for n := 0; n < service.TrialLeaseLimit; n++ {
				ok, _, err := leases.Acquire(ctx, 1, "synthetic-model", 20, fmt.Sprintf("s:%d", n), time.Now())
				require.NoError(t, err)
				require.True(t, ok)
			}
			h := &afterLeaseReply{operation: tc.operation, after: func() {
				srv.SetTime(at.Add(tc.redisOffset + tc.betweenSteps))
			}}
			rdb.AddHook(h)
			var ok bool
			var err error
			if tc.operation == "room" {
				ok, err = leases.Room(ctx, 1, "synthetic-model", 10, "r:new", at)
			} else {
				ok, _, err = leases.Acquire(ctx, 1, "synthetic-model", 10, "r:new", at)
			}
			require.NoError(t, err)
			require.False(t, ok)
			require.Nil(t, h.after, "lease script reply was observed")
			recorded, err := leases.LastFull(ctx, 1, "synthetic-model", 10)
			require.NoError(t, err)
			require.Equal(t, at.Add(tc.redisOffset+tc.betweenSteps), recorded)
			horizon := end.Add(11*time.Second + tc.replayDelay)
			srv.FastForward(horizon.Sub(at.Add(tc.betweenSteps)))
			srv.SetTime(horizon.Add(tc.redisOffset))
			basis := service.ReplayTrialTimeoutBasisForTest(t, leases, start, horizon)
			require.Equal(t, new(tc.want), basis.TrialSeats)
		})
	}
}
