//go:build unit

package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"testing"
	"time"
)

func TestCollabBoundaryAfterFuncDeliveryProbePanic(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Disconnect.Enabled = false })
	before := resilience.CollabSafetySnapshot().ConsumerPanics
	ctx, cancel := context.WithCancel(context.Background())
	state := resilience.NewRequestState()
	called := make(chan struct{})
	state.InitDelivery(ctx, time.Now(), func() int64 { close(called); panic("collabBoundary injected delivery probe failure") })
	ctx = resilience.WithRequestState(ctx, state)
	upstream, _ := collabUpstreamContext(ctx)
	cancel()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("callback not called")
	}
	// Unrecovered panic here terminates the test process; caller recovery cannot help.
	select {
	case <-upstream.Done():
	case <-time.After(time.Second):
		t.Fatal("panic recovery must cancel the optional upstream")
	}
	if got := resilience.CollabSafetySnapshot().ConsumerPanics; got != before+1 {
		t.Fatalf("panic count=%d want=%d", got, before+1)
	}
}
