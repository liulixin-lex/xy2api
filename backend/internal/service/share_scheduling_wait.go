package service

import (
	"context"
	"sync"
	"time"
)

// AccountWaitLeaseCache registers an individual wait attempt, never an
// anonymous counter. Release must fence late registrations and be idempotent.
type AccountWaitLeaseCache interface {
	RegisterAccountWait(ctx context.Context, accountID int64, attempt string, maxWaiting int, ttl time.Duration) (bool, error)
	ReleaseAccountWait(ctx context.Context, accountID int64, attempt string, ttl time.Duration) error
}

// registerShareWait fails closed when ownership cannot be confirmed. Even an
// ambiguous registration error releases only this attempt, using a fresh
// context; another request's queue place can never be decremented.
func (s *ConcurrencyService) registerShareWait(ctx context.Context, accountID int64, maxWaiting int, budget time.Duration) (func(), bool) {
	cache, ok := s.cache.(AccountWaitLeaseCache)
	if !ok || ctx.Err() != nil || maxWaiting <= 0 {
		return nil, false
	}
	attempt := generateRequestID()
	ttl := budget + 5*time.Second
	var once sync.Once
	release := func() {
		once.Do(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			_ = cache.ReleaseAccountWait(cleanup, accountID, attempt, ttl)
		})
	}
	registerCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	registered, err := cache.RegisterAccountWait(registerCtx, accountID, attempt, maxWaiting, ttl)
	if err != nil || ctx.Err() != nil {
		release()
		return nil, false
	}
	if !registered {
		return nil, false
	}
	return release, true
}
