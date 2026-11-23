//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type collabReservationFault struct {
	collabBindingTraceCache
	failure string
	writes  int
}

type collabRefusingBindingCache struct {
	collabBindingTraceCache
	reserve       bool
	refuseKey     bool
	delay         time.Duration
	beforeRefusal func()
}

func (c *collabRefusingBindingCache) ReserveResponseBindings(ctx context.Context, group int64, keys []string, token string) (bool, error) {
	if c.reserve {
		time.Sleep(c.delay)
		if c.beforeRefusal != nil {
			c.beforeRefusal()
		}
		return false, nil
	}
	return c.collabBindingTraceCache.ReserveResponseBindings(ctx, group, keys, token)
}

func (c *collabRefusingBindingCache) CreateResponseBinding(ctx context.Context, group int64, key string, id int64, ttl time.Duration) (bool, error) {
	if c.refuseKey && !strings.HasPrefix(key, openAIHTTPResponseOwnerKeyPrefix) {
		return c.collabBindingTraceCache.CreateResponseBinding(ctx, group, key, id, ttl)
	}
	if c.beforeRefusal != nil {
		c.beforeRefusal()
	}
	return false, nil
}

func TestCollabRefusalPreservesNewLocalGeneration(t *testing.T) {
	for _, stage := range []string{"reservation", "account", "owner_user", "owner_key"} {
		t.Run(stage, func(t *testing.T) {
			cache := &collabRefusingBindingCache{reserve: stage == "reservation", refuseKey: stage == "owner_key"}
			store := NewOpenAIWSStateStore(cache)
			ctx := context.Background()
			owner := strings.HasPrefix(stage, "owner")
			cache.beforeRefusal = func() {
				if owner {
					_ = store.BindHTTPResponseOwner(ctx, 0, "resp_generation", 11, 110, time.Hour)
				} else {
					_ = store.BindResponseAccount(ctx, 0, "resp_generation", 11, time.Hour)
				}
			}
			pub := store.(CollabResponseIdentityStore)
			if owner {
				persist := pub.PrepareCollabHTTPResponseOwner(0, "resp_generation", 9, 90, time.Hour)
				require.NoError(t, persist(ctx))
				user, key, found, err := store.GetHTTPResponseOwner(ctx, 0, "resp_generation")
				require.NoError(t, err)
				require.True(t, found)
				require.EqualValues(t, 11, user)
				require.EqualValues(t, 110, key)
				require.EqualValues(t, 11, store.(*defaultOpenAIWSStateStore).responseOwners[openAIWSResponseAccountMapKey(0, "resp_generation")].userID)
			} else {
				persist := pub.PrepareCollabResponseAccount(0, "resp_generation", 9, time.Hour)
				require.NoError(t, persist(ctx))
				account, err := store.GetResponseAccount(ctx, 0, "resp_generation")
				require.NoError(t, err)
				require.EqualValues(t, 11, account)
				require.EqualValues(t, 11, store.(*defaultOpenAIWSStateStore).responseToAccount[openAIWSResponseAccountMapKey(0, "resp_generation")].accountID)
			}
		})
	}
}

func TestCollabReservationLateRefusalEvictsLocalBinding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := &collabRefusingBindingCache{reserve: true, delay: time.Second}
		ctx := context.Background()
		require.NoError(t, NewOpenAIWSStateStore(cache).BindResponseAccount(ctx, 0, "resp_late", 7, time.Hour))
		store := NewOpenAIWSStateStore(cache)
		persist := store.(CollabResponseIdentityStore).PrepareCollabResponseAccount(0, "resp_late", 9, time.Hour)
		require.NoError(t, persist(ctx))
		time.Sleep(time.Second)
		synctest.Wait()
		account, err := store.GetResponseAccount(ctx, 0, "resp_late")
		require.NoError(t, err)
		require.EqualValues(t, 7, account)
	})
}

func TestCollabRefusedPublicationContinuationReaders(t *testing.T) {
	for _, ordinary := range []bool{false, true} {
		t.Run(map[bool]string{false: "publisher", true: "ordinary"}[ordinary], func(t *testing.T) {
			cache := &collabBindingTraceCache{}
			first, second := NewOpenAIWSStateStore(cache), NewOpenAIWSStateStore(cache)
			ctx := context.Background()
			group := int64(3)
			const id = "resp_collision"
			if ordinary {
				require.NoError(t, first.BindResponseAccount(ctx, group, id, 7, time.Hour))
				require.NoError(t, first.BindHTTPResponseOwner(ctx, group, id, 7, 70, time.Hour))
			} else {
				pub := first.(CollabResponseIdentityStore)
				require.NoError(t, pub.PrepareCollabResponseAccount(group, id, 7, time.Hour, "first")(ctx))
				require.NoError(t, pub.PrepareCollabHTTPResponseOwner(group, id, 7, 70, time.Hour, "first")(ctx))
			}
			pub := second.(CollabResponseIdentityStore)
			accountPersist := pub.PrepareCollabResponseAccount(group, id, 9, time.Hour, "second")
			ownerPersist := pub.PrepareCollabHTTPResponseOwner(group, id, 9, 90, time.Hour, "second")
			accounts := []Account{}
			for _, accountID := range []int64{7, 9} {
				accounts = append(accounts, Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: map[string]any{"openai_ws_force_http": true}})
			}
			svc := &OpenAIGatewayService{accountRepo: stubOpenAIAccountRepo{accounts: accounts}, cache: cache, cfg: newOpenAIWSV2TestConfig(), concurrencyService: NewConcurrencyService(stubConcurrencyCache{}), openaiWSStateStore: second}
			// Read immediately after preparation and after every deferred completion.
			for i := 0; i < 3; i++ {
				if i > 0 {
					require.NoError(t, accountPersist(ctx))
					require.NoError(t, ownerPersist(ctx))
				}
				selection, err := svc.SelectAccountByPreviousResponseID(ctx, &group, id, "gpt-5.1", nil, false)
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.EqualValues(t, 7, selection.Account.ID)
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				owned, err := svc.ValidateOpenAIHTTPResponseOwner(ctx, group, id, 7, 70)
				require.NoError(t, err)
				require.True(t, owned)
				owned, err = svc.ValidateOpenAIHTTPResponseOwner(ctx, group, id, 9, 90)
				require.NoError(t, err)
				require.False(t, owned)
			}
		})
	}
}

func (c *collabReservationFault) ReserveResponseBindings(context.Context, int64, []string, string) (bool, error) {
	switch c.failure {
	case "panic":
		panic("synthetic reservation failure")
	case "timeout":
		time.Sleep(70 * time.Second)
	}
	return false, errors.New("synthetic reservation unavailable")
}
func (c *collabReservationFault) CreateResponseBinding(context.Context, int64, string, int64, time.Duration) (bool, error) {
	c.writes++
	return true, nil
}

func TestCollabReservationFailureKeepsLocalBinding(t *testing.T) {
	for _, failure := range []string{"error", "timeout", "panic"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cache := &collabReservationFault{failure: failure}
				store := NewOpenAIWSStateStore(cache)
				start := time.Now()
				before := resilience.CollabSafetySnapshot().ConsumerPanics
				persist := store.(CollabResponseIdentityStore).PrepareCollabResponseAccount(0, "resp_reserved", 7, time.Hour)
				require.LessOrEqual(t, time.Since(start), 250*time.Millisecond)
				require.NoError(t, persist(context.Background()))
				local, err := store.GetResponseAccount(context.Background(), 0, "resp_reserved")
				require.NoError(t, err)
				require.EqualValues(t, 7, local)
				peer, err := NewOpenAIWSStateStore(cache).GetResponseAccount(context.Background(), 0, "resp_reserved")
				require.NoError(t, err)
				require.Zero(t, peer)
				require.Zero(t, cache.writes)
				if failure == "panic" {
					require.Equal(t, before+1, resilience.CollabSafetySnapshot().ConsumerPanics)
				}
				time.Sleep(70 * time.Second) // finish the deliberately uncooperative fixture
			})
		})
	}
}
