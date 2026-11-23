package service

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// Publications carry their generation and deadline into the atomic shared write.
const CollabBindingPublicationLifetime = time.Minute

// Shared creation must distinguish never-published from deleted bindings. A
// plain SET or SET NX loses that distinction and can resurrect a deletion.
// CreateResponseBinding must compare CollabBindingPublicationFrom(ctx) with
// the stored generation and check its deadline atomically at write execution.
type CollabResponseBindingCache interface {
	SetResponseBinding(context.Context, int64, string, int64, time.Duration) error
	CreateResponseBinding(context.Context, int64, string, int64, time.Duration) error
	DeleteResponseBinding(context.Context, int64, string) error
}

// Adapters publish locally without I/O and return a conditional shared write.
// Adapters without this contract cannot safely defer an unconditional Bind.
type CollabResponseIdentityStore interface {
	PrepareCollabResponseAccount(int64, string, int64, time.Duration) func(context.Context) error
	PrepareCollabHTTPResponseOwner(int64, string, int64, int64, time.Duration) func(context.Context) error
}

func (s *defaultOpenAIWSStateStore) PrepareCollabResponseAccount(group int64, id string, account int64, ttl time.Duration) func(context.Context) error {
	generation := s.bindResponseAccountLocal(group, id, account, ttl)
	publication := NewCollabBindingPublication()
	return func(ctx context.Context) error {
		s.responseToAccountMu.RLock()
		current := s.responseToAccount[openAIWSResponseAccountMapKey(group, id)]
		s.responseToAccountMu.RUnlock()
		if current.generation != generation || !time.Now().Before(publication.Expires) || ctx.Err() != nil {
			return ctx.Err()
		}
		if s.cache == nil {
			return nil
		}
		cache, ok := s.cache.(CollabResponseBindingCache)
		if !ok {
			return errors.New("conditional response binding unavailable")
		}
		return cache.CreateResponseBinding(context.WithValue(ctx, collabBindingPublicationKey{}, publication), group, openAIWSResponseAccountCacheKey(id), account, ttl)
	}
}
func (s *defaultOpenAIWSStateStore) PrepareCollabHTTPResponseOwner(group int64, id string, user, key int64, ttl time.Duration) func(context.Context) error {
	generation := s.bindHTTPResponseOwnerLocal(group, id, user, key, ttl)
	publication := NewCollabBindingPublication()
	return func(ctx context.Context) error {
		s.responseOwnerMu.RLock()
		current := s.responseOwners[openAIWSResponseAccountMapKey(group, id)]
		s.responseOwnerMu.RUnlock()
		if current.generation != generation || !time.Now().Before(publication.Expires) || ctx.Err() != nil {
			return ctx.Err()
		}
		if s.cache == nil {
			return nil
		}
		cache, ok := s.cache.(CollabResponseBindingCache)
		if !ok {
			return errors.New("conditional response binding unavailable")
		}
		ctx = context.WithValue(ctx, collabBindingPublicationKey{}, publication)
		if err := cache.CreateResponseBinding(ctx, group, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerUserPrefix, id), user, ttl); err != nil {
			return err
		}
		return cache.CreateResponseBinding(ctx, group, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerKeyPrefix, id), key, ttl)
	}
}

func (s *defaultOpenAIWSStateStore) setResponseBinding(ctx context.Context, group int64, key string, id int64, ttl time.Duration) error {
	if cache, ok := s.cache.(CollabResponseBindingCache); ok {
		return cache.SetResponseBinding(ctx, group, key, id, ttl)
	}
	return s.cache.SetSessionAccountID(ctx, group, key, id, ttl)
}

// Microsecond generations are exactly representable in Lua numbers. The local
// counter makes prepares monotonic even when the wall clock stalls or retreats.
// Redis compares them and the publication deadline in the same atomic script.
type CollabBindingPublication struct {
	Generation int64
	Expires    time.Time
}
type collabBindingPublicationKey struct{}

var collabBindingClock atomic.Int64

func NewCollabBindingPublication() CollabBindingPublication {
	for {
		prior := collabBindingClock.Load()
		now := time.Now().UnixMicro()
		if now <= prior {
			now = prior + 1
		}
		if collabBindingClock.CompareAndSwap(prior, now) {
			return CollabBindingPublication{Generation: now, Expires: time.Now().Add(CollabBindingPublicationLifetime)}
		}
	}
}
func CollabBindingPublicationFrom(ctx context.Context) CollabBindingPublication {
	if publication, ok := ctx.Value(collabBindingPublicationKey{}).(CollabBindingPublication); ok {
		return publication
	}
	return NewCollabBindingPublication()
}
