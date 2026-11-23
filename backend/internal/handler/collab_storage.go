package handler

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

var collabStorageWorkers atomic.Int32

// A storage adapter may ignore cancellation. The caller still yields within
// its budget. Callbacks capture only the service and query values: no request,
// acquired account slot, or release function belongs to this worker.
func collabStorageAllowed(parent context.Context, check func(context.Context) bool) bool {
	if parent.Err() != nil {
		return false
	}
	if collabStorageWorkers.Add(1) > 8 {
		collabStorageWorkers.Add(-1)
		return false
	}
	query, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		defer resilience.RecoverCollabWorker()
		defer collabStorageWorkers.Add(-1)
		defer close(done)
		done <- check(query)
	}()
	select {
	case <-parent.Done():
		return false
	case <-query.Done():
		return false
	case allowed := <-done:
		return allowed && parent.Err() == nil && query.Err() == nil
	}
}
