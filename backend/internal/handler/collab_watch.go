package handler

import (
	"context"
	"sync"
	"time"
)

// The supervisor owns its timers and joins before request cleanup. Queue reads
// have a short cancellation bound and occur at most once per second after the
// initial gate. Acceptance suspends the deadline and user queue checks; a
// retained original account still yields to its waiters throughout takeover.
func watchCollabTakeover(parent context.Context, duration time.Duration, accepted func() bool, waiters func(context.Context) bool, retainedWaiters ...func(context.Context) bool) (context.Context, func()) {
	started := time.Now()
	deadline := started.Add(duration)
	ctx, cancel := context.WithCancel(parent)
	queued := func(check func(context.Context) bool) (blocked bool) {
		defer func() {
			if recover() != nil {
				blocked = true
			}
		}()
		query, done := context.WithTimeout(ctx, 250*time.Millisecond)
		defer done()
		return check(query)
	}
	if duration <= 0 || queued(waiters) {
		cancel()
		return ctx, func() {}
	}
	duration -= time.Since(started)
	if duration <= 0 {
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
			case <-ticker.C:
				if accepted() {
					if len(retainedWaiters) > 0 && retainedWaiters[0] != nil && queued(retainedWaiters[0]) {
						cancel()
						return
					}
					continue
				}
				if !time.Now().Before(deadline) || queued(waiters) {
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	return ctx, func() { once.Do(func() { cancel(); close(stop); <-stopped }) }
}
