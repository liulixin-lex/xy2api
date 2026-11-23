//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

type collabBindingCacheValue struct {
	id      int64
	expires time.Time
}
type collabBindingTraceCache struct {
	stubGatewayCache
	values     map[string]collabBindingCacheValue
	tombstones map[string]time.Time
	calls      []string
	fail       string
}

func (s *collabBindingTraceCache) SetSessionAccountID(ctx context.Context, group int64, key string, id int64, ttl time.Duration) error {
	kind := "account"
	if strings.HasPrefix(key, openAIHTTPResponseOwnerUserPrefix) {
		kind = "user"
	}
	if strings.HasPrefix(key, openAIHTTPResponseOwnerKeyPrefix) {
		kind = "key"
	}
	deadline, _ := ctx.Deadline()
	s.calls = append(s.calls, fmt.Sprintf("set:%s:ttl=%s:budget=%s", kind, ttl, time.Until(deadline)))
	if err := ctx.Err(); err != nil {
		return err
	}
	if kind == s.fail {
		return errors.New("synthetic write failure")
	}
	if s.values == nil {
		s.values = make(map[string]collabBindingCacheValue)
	}
	s.values[fmt.Sprint(group, ":", key)] = collabBindingCacheValue{id, time.Now().Add(ttl)}
	return nil
}
func (s *collabBindingTraceCache) GetSessionAccountID(ctx context.Context, group int64, key string) (int64, error) {
	value := s.values[fmt.Sprint(group, ":", key)]
	if !time.Now().Before(value.expires) {
		return 0, ErrStickySessionNotFound
	}
	return value.id, nil
}
func (s *collabBindingTraceCache) DeleteSessionAccountID(ctx context.Context, group int64, key string) error {
	delete(s.values, fmt.Sprint(group, ":", key))
	return nil
}

func TestCollabRegressionOrdinaryStoreContract(t *testing.T) {
	for _, fail := range []string{"", "account", "user", "key", "canceled"} {
		t.Run("failure="+fail, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cache := &collabBindingTraceCache{fail: fail}
				writer := NewOpenAIWSStateStore(cache)
				reader := NewOpenAIWSStateStore(cache)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if fail == "canceled" {
					cancel()
				}
				accountErr := writer.BindResponseAccount(ctx, 4, " resp_trace ", 7, time.Second)
				ownerErr := writer.BindHTTPResponseOwner(ctx, 4, " resp_trace ", 3, 5, time.Second)
				id, err := writer.GetResponseAccount(context.Background(), 4, "resp_trace")
				require.NoError(t, err)
				require.EqualValues(t, 7, id)
				user, key, found, err := writer.GetHTTPResponseOwner(context.Background(), 4, "resp_trace")
				require.NoError(t, err)
				require.True(t, found)
				require.EqualValues(t, 3, user)
				require.EqualValues(t, 5, key)
				peerID, _ := reader.GetResponseAccount(context.Background(), 4, "resp_trace")
				_, _, peerOwner, peerErr := reader.GetHTTPResponseOwner(context.Background(), 4, "resp_trace")
				require.Equal(t, fail != "account" && fail != "canceled", peerID == 7)
				require.Equal(t, fail == "" || fail == "account", peerOwner)
				require.Equal(t, fail == "account" || fail == "canceled", accountErr != nil)
				require.Equal(t, fail == "user" || fail == "key" || fail == "canceled", ownerErr != nil)
				expected := []string{"set:account:ttl=1s:budget=3s", "set:user:ttl=1s:budget=3s"}
				if fail != "user" && fail != "canceled" {
					expected = append(expected, "set:key:ttl=1s:budget=3s")
				}
				require.Equal(t, expected, cache.calls)
				time.Sleep(time.Second)
				id, _ = writer.GetResponseAccount(context.Background(), 4, "resp_trace")
				_, _, found, _ = writer.GetHTTPResponseOwner(context.Background(), 4, "resp_trace")
				require.Zero(t, id)
				require.False(t, found)
				t.Logf("trace=%v account_error=%t owner_error=%t peer_account=%d peer_owner=%t peer_error=%t expired_local=true", cache.calls, accountErr != nil, ownerErr != nil, peerID, peerOwner, peerErr != nil)
			})
		})
	}
}

type collabBindingDelayedAlias struct{ collabMemoryAliases }

func (s *collabBindingDelayedAlias) Put(ctx context.Context, scope CollabContinuationScope, id string, a CollabContinuationAlias) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(200 * time.Millisecond):
	}
	return s.collabMemoryAliases.Put(ctx, scope, id, a)
}
func TestCollabRegressionAsyncBindingDoesNotResurrectDeletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := &collabBindingTraceCache{}
		store := NewOpenAIWSStateStore(cache)
		svc := &OpenAIGatewayService{openaiWSStateStore: store}
		svc.SetCollabContinuationStore(&collabBindingDelayedAlias{})
		c := collabDeliveredContext()
		SetOpenAIHTTPResponseOwner(c, 3, 5)
		persist := svc.CommitCollabResponseIdentity(c, &Account{ID: 7}, &OpenAIForwardResult{ResponseID: "resp_upstream"}, "resp_client")
		require.NotNil(t, persist)
		before, _ := store.GetResponseAccount(context.Background(), 0, "resp_client")
		require.EqualValues(t, 7, before)
		persist()
		synctest.Wait()
		time.Sleep(100 * time.Millisecond)
		require.NoError(t, store.DeleteResponseAccount(context.Background(), 0, "resp_client"))
		deleted, _ := store.GetResponseAccount(context.Background(), 0, "resp_client")
		require.Zero(t, deleted)
		time.Sleep(150 * time.Millisecond)
		synctest.Wait()
		local, _ := store.GetResponseAccount(context.Background(), 0, "resp_client")
		peer, _ := NewOpenAIWSStateStore(cache).GetResponseAccount(context.Background(), 0, "resp_client")
		t.Logf("local_before=%d immediately_after_delete=%d at_250ms=%d peer_at_250ms=%d writes=%v", before, deleted, local, peer, cache.calls)
		require.Zero(t, local, "deferred persistence must not republish a binding deleted by a later request")
		require.Zero(t, peer)
	})
}

func (s *collabBindingTraceCache) CreateResponseBinding(ctx context.Context, group int64, key string, id int64, ttl time.Duration) error {
	mapKey := fmt.Sprint(group, ":", key)
	if time.Now().Before(s.tombstones[mapKey]) || time.Now().Before(s.values[mapKey].expires) {
		return nil
	}
	return s.SetSessionAccountID(ctx, group, key, id, ttl)
}
func (s *collabBindingTraceCache) DeleteResponseBinding(ctx context.Context, group int64, key string) error {
	if s.tombstones == nil {
		s.tombstones = map[string]time.Time{}
	}
	s.tombstones[fmt.Sprint(group, ":", key)] = time.Now().Add(CollabBindingPublicationLifetime)
	return s.DeleteSessionAccountID(ctx, group, key)
}

func TestCollabRegressionDeferredBindingGeneration(t *testing.T) {
	for _, change := range []string{"local_delete", "peer_delete", "local_replace", "peer_replace", "same_value_new_generation", "expired_callback"} {
		t.Run(change, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cache := &collabBindingTraceCache{}
				store := NewOpenAIWSStateStore(cache)
				peer := NewOpenAIWSStateStore(cache)
				publisher := store.(CollabResponseIdentityStore)
				persist := publisher.PrepareCollabResponseAccount(0, "resp_generation", 7, time.Hour)
				target := store
				if strings.HasPrefix(change, "peer") {
					target = peer
				}
				want := int64(0)
				switch change {
				case "local_delete", "peer_delete":
					require.NoError(t, target.DeleteResponseAccount(context.Background(), 0, "resp_generation"))
				case "local_replace", "peer_replace":
					want = 9
					require.NoError(t, target.BindResponseAccount(context.Background(), 0, "resp_generation", want, time.Hour))
				case "same_value_new_generation":
					want = 7
					require.NoError(t, store.BindResponseAccount(context.Background(), 0, "resp_generation", want, time.Hour))
				case "expired_callback":
					time.Sleep(CollabBindingPublicationLifetime)
				}
				before := len(cache.calls)
				require.NoError(t, persist(context.Background()))
				require.Len(t, cache.calls, before, "stale publication must not issue a SET")
				shared, _ := NewOpenAIWSStateStore(cache).GetResponseAccount(context.Background(), 0, "resp_generation")
				require.Equal(t, want, shared)
				if change == "local_delete" || change == "local_replace" {
					local, _ := store.GetResponseAccount(context.Background(), 0, "resp_generation")
					require.Equal(t, want, local)
				}
			})
		})
	}
}

func (s *collabBindingTraceCache) SetResponseBinding(ctx context.Context, group int64, key string, id int64, ttl time.Duration) error {
	if s.tombstones == nil {
		s.tombstones = map[string]time.Time{}
	}
	s.tombstones[fmt.Sprint(group, ":", key)] = time.Now().Add(collabBindingFenceTTL(ttl))
	return s.SetSessionAccountID(ctx, group, key, id, ttl)
}

func collabBindingFenceTTL(ttl time.Duration) time.Duration {
	if ttl < CollabBindingPublicationLifetime {
		return CollabBindingPublicationLifetime
	}
	return ttl
}
