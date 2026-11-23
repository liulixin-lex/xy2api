package service

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Reservations expire in Redis; the local deadline also avoids needless writes.
const CollabBindingPublicationLifetime = time.Minute

// Redis orders preparation against every later set/delete on the same key.
// A create requires the exact unconsumed reservation and never compares clocks.
type CollabResponseBindingCache interface {
	ReserveResponseBindings(context.Context, int64, []string, string) error
	SetResponseBinding(context.Context, int64, string, int64, time.Duration) error
	CreateResponseBinding(context.Context, int64, string, int64, time.Duration) error
	DeleteResponseBinding(context.Context, int64, string) error
}

// Adapters publish locally, reserve shared metadata within a short optional
// budget, and return a conditional shared write. Reservation failure keeps the
// local binding and suppresses shared publication.
// Adapters without this contract cannot safely defer an unconditional Bind.
type CollabResponseIdentityStore interface {
	PrepareCollabResponseAccount(int64, string, int64, time.Duration) func(context.Context) error
	PrepareCollabHTTPResponseOwner(int64, string, int64, int64, time.Duration) func(context.Context) error
}

func (s *defaultOpenAIWSStateStore) PrepareCollabResponseAccount(group int64, id string, account int64, ttl time.Duration) func(context.Context) error {
	generation := s.bindResponseAccountLocal(group, id, account, ttl)
	publication := s.reserveCollabBinding(group, []string{openAIWSResponseAccountCacheKey(id)})
	return func(ctx context.Context) error {
		s.responseToAccountMu.RLock()
		current := s.responseToAccount[openAIWSResponseAccountMapKey(group, id)]
		s.responseToAccountMu.RUnlock()
		if publication.Token == "" || current.generation != generation || !time.Now().Before(publication.Expires) || ctx.Err() != nil {
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
	publication := s.reserveCollabBinding(group, []string{openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerUserPrefix, id), openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerKeyPrefix, id)})
	return func(ctx context.Context) error {
		s.responseOwnerMu.RLock()
		current := s.responseOwners[openAIWSResponseAccountMapKey(group, id)]
		s.responseOwnerMu.RUnlock()
		if publication.Token == "" || current.generation != generation || !time.Now().Before(publication.Expires) || ctx.Err() != nil {
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

// Token equality is the only shared ordering predicate. Expires is a local
// optimization; Redis owns reservation expiry and authoritative invalidation.
type CollabBindingPublication struct {
	Token   string
	Expires time.Time
}
type collabBindingPublicationKey struct{}

func NewCollabBindingPublication() CollabBindingPublication {
	return CollabBindingPublication{Token: uuid.NewString(), Expires: time.Now().Add(CollabBindingPublicationLifetime)}
}
func CollabBindingPublicationFrom(ctx context.Context) CollabBindingPublication {
	publication, _ := ctx.Value(collabBindingPublicationKey{}).(CollabBindingPublication)
	return publication
}

var collabBindingReservations atomic.Int32

func (s *defaultOpenAIWSStateStore) reserveCollabBinding(group int64, keys []string) CollabBindingPublication {
	publication := NewCollabBindingPublication()
	if s.cache == nil {
		return publication
	}
	cache, ok := s.cache.(CollabResponseBindingCache)
	if !ok {
		return CollabBindingPublication{}
	}
	if collabBindingReservations.Add(1) > 4 {
		collabBindingReservations.Add(-1)
		return CollabBindingPublication{}
	}
	// No request values, local-map locks, or concurrency slots reach this worker.
	ctx, cancel := context.WithTimeout(context.Background(), resilience.CollabRecordTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer resilience.RecoverCollabWorker()
		defer collabBindingReservations.Add(-1)
		defer close(done)
		done <- cache.ReserveResponseBindings(ctx, group, keys, publication.Token)
	}()
	select {
	case <-ctx.Done():
		return CollabBindingPublication{}
	case err, completed := <-done:
		if !completed || err != nil || ctx.Err() != nil {
			return CollabBindingPublication{}
		}
		return publication
	}
}
