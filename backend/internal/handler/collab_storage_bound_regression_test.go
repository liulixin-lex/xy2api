//go:build unit

package handler

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"testing/synctest"
	"time"
)

func TestCollabBoundaryUncooperativeWaiterBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		ctx, stop := watchCollabTakeover(context.Background(), time.Second, func() bool { return false }, func(query context.Context) bool {
			_, ok := query.Deadline()
			require.True(t, ok)
			time.Sleep(70 * time.Second)
			return false
		})
		defer stop()
		t.Logf("initial waiter elapsed=%s context=%v", time.Since(start), ctx.Err())
		require.LessOrEqual(t, time.Since(start), 250*time.Millisecond, "optional waiter storage must have a hard bound")
		time.Sleep(70 * time.Second) // let the detached fixture finish after measuring the caller
	})
}
func TestCollabBoundaryUncooperativeSupervisorJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		started := make(chan struct{})
		_, stop := watchCollabTakeover(context.Background(), time.Minute, func() bool { return false }, func(query context.Context) bool {
			calls++
			if calls == 2 {
				close(started)
				time.Sleep(70 * time.Second)
			}
			return false
		})
		<-started
		start := time.Now()
		stop()
		t.Logf("stop/join elapsed=%s calls=%d", time.Since(start), calls)
		require.LessOrEqual(t, time.Since(start), 250*time.Millisecond, "request cleanup must not join an uncooperative optional storage call")
		time.Sleep(70 * time.Second) // let the detached fixture finish after measuring the caller
	})
}

func TestCollabBoundaryUncooperativeAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		allowed := collabStorageAllowed(context.Background(), func(query context.Context) bool {
			_, bounded := query.Deadline()
			require.True(t, bounded)
			time.Sleep(70 * time.Second)
			return true
		})
		require.False(t, allowed)
		require.LessOrEqual(t, time.Since(start), 250*time.Millisecond)
		time.Sleep(70 * time.Second) // let the detached fixture finish after measuring the caller
	})
}
