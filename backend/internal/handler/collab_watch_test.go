//go:build unit

package handler

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollabWatchAcceptanceWaitersAndJoin(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "accepted reasoning"}[accept], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var accepted atomic.Bool
				calls := atomic.Int32{}
				ctx, stop := watchCollabTakeover(context.Background(), 15*time.Second, accepted.Load, func(context.Context) bool { calls.Add(1); return false })
				time.Sleep(14 * time.Second)
				accepted.Store(accept)
				time.Sleep(2 * time.Second)
				require.Equal(t, !accept, ctx.Err() != nil)
				stop()
				n := calls.Load()
				time.Sleep(5 * time.Second)
				require.Equal(t, n, calls.Load())
				require.LessOrEqual(t, n, int32(16))
			})
		})
	}
	synctest.Test(t, func(t *testing.T) {
		var queries atomic.Int32
		ctx, stop := watchCollabTakeover(context.Background(), time.Minute, func() bool { return false }, func(ctx context.Context) bool {
			if queries.Add(1) == 1 {
				return false
			}
			<-ctx.Done()
			return true
		})
		time.Sleep(1250 * time.Millisecond)
		synctest.Wait()
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		stop()
		require.EqualValues(t, 2, queries.Load())
	})
}
