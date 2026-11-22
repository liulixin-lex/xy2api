package handler

import (
	"context"
	"sync"
	"time"
)

// The supervisor owns its timers and joins before request cleanup. Queue reads
// have a short cancellation bound and occur at most once per second after the
// initial gate. Acceptance ends optional supervision, not the normal stream.
func watchCollabTakeover(parent context.Context, duration time.Duration, accepted func() bool, waiters func(context.Context) bool) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	queued := func() (blocked bool) {
		defer func() {
			if recover() != nil {
				blocked = true
			}
		}()
		query, done := context.WithTimeout(ctx, 250*time.Millisecond)
		defer done()
		return waiters(query)
	}
	if duration <= 0 || queued() {
		cancel()
		return ctx, func() {}
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		timer, ticker := time.NewTimer(duration), time.NewTicker(time.Second)
		defer timer.Stop()
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-timer.C:
				if !accepted() {
					cancel()
				}
				return
			case <-ticker.C:
				if accepted() {
					return
				}
				if queued() {
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	return ctx, func() { once.Do(func() { cancel(); close(stop); <-stopped }) }
}
