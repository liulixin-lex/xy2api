//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCacheTraceAdmissionDoesNotWaitForWorkerQueueLock(t *testing.T) {
	p := &cacheTraceWorkers{budget: cacheTraceDefaultByteBudget}
	p.mu.Lock()
	defer p.mu.Unlock()
	type admission struct {
		cost int64
		ok   bool
	}
	done := make(chan admission, 1)
	go func() {
		cost, ok := p.reserve(256, 512)
		done <- admission{cost, ok}
	}()
	select {
	case result := <-done:
		require.True(t, result.ok)
		require.EqualValues(t, 2304, result.cost)
		require.Equal(t, result.cost, p.reserved.Load())
		p.reserved.Add(-result.cost)
	case <-time.After(time.Second):
		t.Fatal("admission blocked on the worker queue mutex")
	}
}
