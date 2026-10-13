package service

import (
	"context"
	"errors"
)

// ErrConcurrencyPeekUnavailable reports that read-only load inspection is not
// supported by the configured concurrency cache.
var ErrConcurrencyPeekUnavailable = errors.New("concurrency load peek unavailable")

// PeekAccountsLoadBatch never trims stale leases or registers any slot.
func (s *ConcurrencyService) PeekAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	if len(accounts) == 0 {
		return map[int64]*AccountLoadInfo{}, nil
	}
	if s == nil || s.cache == nil {
		return nil, ErrConcurrencyPeekUnavailable
	}
	reader, ok := s.cache.(interface {
		PeekAccountsLoadBatch(context.Context, []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error)
	})
	if !ok {
		return nil, ErrConcurrencyPeekUnavailable
	}
	return reader.PeekAccountsLoadBatch(ctx, accounts)
}
