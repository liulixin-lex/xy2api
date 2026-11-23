package service

import (
	"context"
	"errors"
	"time"
)

// Delayed publications expire before a shared deletion's tombstone can expire.
// This bounds both metadata retention and callbacks held past settlement.
const CollabBindingPublicationLifetime = time.Minute

// Shared creation must distinguish never-published from deleted bindings. A
// plain SET or SET NX loses that distinction and can resurrect a deletion.
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
	published := time.Now()
	return func(ctx context.Context) error {
		s.responseToAccountMu.RLock()
		current := s.responseToAccount[openAIWSResponseAccountMapKey(group, id)]
		s.responseToAccountMu.RUnlock()
		if current.generation != generation || time.Since(published) >= CollabBindingPublicationLifetime || ctx.Err() != nil {
			return ctx.Err()
		}
		if s.cache == nil {
			return nil
		}
		cache, ok := s.cache.(CollabResponseBindingCache)
		if !ok {
			return errors.New("conditional response binding unavailable")
		}
		return cache.CreateResponseBinding(ctx, group, openAIWSResponseAccountCacheKey(id), account, ttl)
	}
}
func (s *defaultOpenAIWSStateStore) PrepareCollabHTTPResponseOwner(group int64, id string, user, key int64, ttl time.Duration) func(context.Context) error {
	generation := s.bindHTTPResponseOwnerLocal(group, id, user, key, ttl)
	published := time.Now()
	return func(ctx context.Context) error {
		s.responseOwnerMu.RLock()
		current := s.responseOwners[openAIWSResponseAccountMapKey(group, id)]
		s.responseOwnerMu.RUnlock()
		if current.generation != generation || time.Since(published) >= CollabBindingPublicationLifetime || ctx.Err() != nil {
			return ctx.Err()
		}
		if s.cache == nil {
			return nil
		}
		cache, ok := s.cache.(CollabResponseBindingCache)
		if !ok {
			return errors.New("conditional response binding unavailable")
		}
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
