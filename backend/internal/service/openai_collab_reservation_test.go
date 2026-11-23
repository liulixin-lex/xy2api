//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type collabReservationFault struct {
	collabBindingTraceCache
	failure string
	writes  int
}

func (c *collabReservationFault) ReserveResponseBindings(context.Context, int64, []string, string) error {
	switch c.failure {
	case "panic":
		panic("synthetic reservation failure")
	case "timeout":
		time.Sleep(70 * time.Second)
	}
	return errors.New("synthetic reservation unavailable")
}
func (c *collabReservationFault) CreateResponseBinding(context.Context, int64, string, int64, time.Duration) error {
	c.writes++
	return nil
}

func TestCollabReservationFailureKeepsLocalBinding(t *testing.T) {
	for _, failure := range []string{"error", "timeout", "panic"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cache := &collabReservationFault{failure: failure}
				store := NewOpenAIWSStateStore(cache)
				start := time.Now()
				before := resilience.CollabSafetySnapshot().ConsumerPanics
				persist := store.(CollabResponseIdentityStore).PrepareCollabResponseAccount(0, "resp_reserved", 7, time.Hour)
				require.LessOrEqual(t, time.Since(start), 250*time.Millisecond)
				require.NoError(t, persist(context.Background()))
				local, err := store.GetResponseAccount(context.Background(), 0, "resp_reserved")
				require.NoError(t, err)
				require.EqualValues(t, 7, local)
				peer, err := NewOpenAIWSStateStore(cache).GetResponseAccount(context.Background(), 0, "resp_reserved")
				require.NoError(t, err)
				require.Zero(t, peer)
				require.Zero(t, cache.writes)
				if failure == "panic" {
					require.Equal(t, before+1, resilience.CollabSafetySnapshot().ConsumerPanics)
				}
				time.Sleep(70 * time.Second) // finish the deliberately uncooperative fixture
			})
		})
	}
}
