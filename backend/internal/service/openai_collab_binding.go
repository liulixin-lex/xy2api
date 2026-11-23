package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Reservations expire in Redis; the local deadline also avoids needless writes.
const CollabBindingPublicationLifetime = time.Minute

// Redis orders preparation against every later set/delete on the same key.
// A create requires the exact unconsumed reservation and never compares clocks.
// A publisher may replace only its own pending or published request identity.
type CollabResponseBindingCache interface {
	ReserveResponseBindings(context.Context, int64, []string, string) (accepted bool, err error)
	SetResponseBinding(context.Context, int64, string, int64, time.Duration) error
	CreateResponseBinding(context.Context, int64, string, int64, time.Duration) (accepted bool, err error)
	DeleteResponseBinding(context.Context, int64, string) error
}

// Adapters publish locally, reserve shared metadata within a short optional
// budget, and return a conditional shared write. Reservation failure keeps the
// local binding and suppresses shared publication. An explicit refusal evicts
// only that publication's local generation so the next lookup reads shared state.
// Adapters without this contract cannot safely defer an unconditional Bind.
type CollabResponseIdentityStore interface {
	PrepareCollabResponseAccount(int64, string, int64, time.Duration, ...string) func(context.Context) error
	PrepareCollabHTTPResponseOwner(int64, string, int64, int64, time.Duration, ...string) func(context.Context) error
}

func (s *defaultOpenAIWSStateStore) PrepareCollabResponseAccount(group int64, id string, account int64, ttl time.Duration, publisher ...string) func(context.Context) error {
	publication := NewCollabBindingPublication(publisher...)
	generation := s.bindResponseAccountLocal(group, id, account, ttl, publication.Token[:64])
	if generation == 0 {
		return func(context.Context) error { return nil }
	}
	discard := func() {
		s.responseToAccountMu.Lock()
		defer s.responseToAccountMu.Unlock()
		mapKey := openAIWSResponseAccountMapKey(group, id)
		if s.responseToAccount[mapKey].generation == generation {
			delete(s.responseToAccount, mapKey)
		}
	}
	publication = s.reserveCollabBinding(group, []string{openAIWSResponseAccountCacheKey(id)}, publication, discard)
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
		accepted, err := cache.CreateResponseBinding(context.WithValue(ctx, collabBindingPublicationKey{}, publication), group, openAIWSResponseAccountCacheKey(id), account, ttl)
		if !accepted && err == nil {
			discard()
		}
		return err
	}
}
func (s *defaultOpenAIWSStateStore) PrepareCollabHTTPResponseOwner(group int64, id string, user, key int64, ttl time.Duration, publisher ...string) func(context.Context) error {
	publication := NewCollabBindingPublication(publisher...)
	generation := s.bindHTTPResponseOwnerLocal(group, id, user, key, ttl, publication.Token[:64])
	if generation == 0 {
		return func(context.Context) error { return nil }
	}
	discard := func() {
		s.responseOwnerMu.Lock()
		defer s.responseOwnerMu.Unlock()
		mapKey := openAIWSResponseAccountMapKey(group, id)
		if s.responseOwners[mapKey].generation == generation {
			delete(s.responseOwners, mapKey)
		}
	}
	publication = s.reserveCollabBinding(group, []string{openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerUserPrefix, id), openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerKeyPrefix, id)}, publication, discard)
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
		for _, binding := range []struct {
			prefix string
			value  int64
		}{{openAIHTTPResponseOwnerUserPrefix, user}, {openAIHTTPResponseOwnerKeyPrefix, key}} {
			accepted, err := cache.CreateResponseBinding(ctx, group, openAIHTTPResponseOwnerCacheKey(binding.prefix, id), binding.value, ttl)
			if err != nil {
				return err
			}
			if !accepted {
				discard()
				return nil
			}
		}
		return nil
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

func NewCollabBindingPublication(publisher ...string) CollabBindingPublication {
	owner := ""
	if len(publisher) > 0 {
		owner = publisher[0]
	}
	if owner == "" {
		owner = uuid.NewString()
	}
	return CollabBindingPublication{Token: fmt.Sprintf("%x:%s", sha256.Sum256([]byte(owner)), uuid.NewString()), Expires: time.Now().Add(CollabBindingPublicationLifetime)}
}
func CollabBindingPublicationFrom(ctx context.Context) CollabBindingPublication {
	publication, _ := ctx.Value(collabBindingPublicationKey{}).(CollabBindingPublication)
	return publication
}

var collabBindingReservations atomic.Int32

func (s *defaultOpenAIWSStateStore) reserveCollabBinding(group int64, keys []string, publication CollabBindingPublication, discard func()) CollabBindingPublication {
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
	// No request values or concurrency slots reach this worker. Local-map locks
	// are acquired only after the shared operation, never held while it runs.
	ctx, cancel := context.WithTimeout(context.Background(), resilience.CollabRecordTimeout)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		defer resilience.RecoverCollabWorker()
		defer collabBindingReservations.Add(-1)
		defer close(done)
		accepted, err := cache.ReserveResponseBindings(ctx, group, keys, publication.Token)
		if !accepted && err == nil {
			// A late refusal must also evict after the optional wait has ended.
			discard()
		}
		done <- accepted && err == nil
	}()
	select {
	case <-ctx.Done():
		return CollabBindingPublication{}
	case accepted := <-done:
		if !accepted || ctx.Err() != nil {
			return CollabBindingPublication{}
		}
		return publication
	}
}
