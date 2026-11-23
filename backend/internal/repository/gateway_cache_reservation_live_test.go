//go:build unit

package repository

import (
	"context"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type collabDelayedReserve struct {
	*gatewayCache
	started, proceed chan struct{}
}

func (c *collabDelayedReserve) ReserveResponseBindings(ctx context.Context, group int64, keys []string, token string) error {
	close(c.started)
	<-c.proceed
	return c.gatewayCache.ReserveResponseBindings(ctx, group, keys, token)
}

func TestCollabReservationDelayedReservationOverwritesLiveBinding(t *testing.T) {
	for _, owner := range []bool{false, true} {
		t.Run(map[bool]string{false: "account", true: "http_owner"}[owner], func(t *testing.T) {
			cache, _ := collabBoundaryRedis(t)
			delayed := &collabDelayedReserve{cache, make(chan struct{}), make(chan struct{})}
			oldStore := service.NewOpenAIWSStateStore(delayed)
			liveStore := service.NewOpenAIWSStateStore(cache)
			prepared := make(chan func(context.Context) error, 1)
			go func() {
				if owner {
					prepared <- oldStore.(service.CollabResponseIdentityStore).PrepareCollabHTTPResponseOwner(3, "resp_synthetic", 7, 70, time.Hour)
				} else {
					prepared <- oldStore.(service.CollabResponseIdentityStore).PrepareCollabResponseAccount(3, "resp_synthetic", 7, time.Hour)
				}
			}()
			<-delayed.started
			if owner {
				require.NoError(t, liveStore.BindHTTPResponseOwner(context.Background(), 3, "resp_synthetic", 9, 90, time.Hour))
			} else {
				require.NoError(t, liveStore.BindResponseAccount(context.Background(), 3, "resp_synthetic", 9, time.Hour))
			}
			close(delayed.proceed)
			publish := <-prepared
			require.NoError(t, publish(context.Background()))
			peer := service.NewOpenAIWSStateStore(cache)
			if owner {
				user, key, found, err := peer.GetHTTPResponseOwner(context.Background(), 3, "resp_synthetic")
				require.NoError(t, err)
				require.True(t, found)
				t.Logf("live user/key=9/90; fresh peer user/key=%d/%d", user, key)
				require.EqualValues(t, 9, user)
				require.EqualValues(t, 90, key)
			} else {
				account, err := peer.GetResponseAccount(context.Background(), 3, "resp_synthetic")
				require.NoError(t, err)
				t.Logf("live account=9; fresh peer account=%d", account)
				require.EqualValues(t, 9, account)
			}
		})
	}
}

func TestCollabReservationExistingAndExpired(t *testing.T) {
	for _, existing := range []string{"generic_set", "response_set", "published", "expired", "missing"} {
		t.Run(existing, func(t *testing.T) {
			c, f := collabBoundaryRedis(t)
			ctx := context.Background()
			key := "resp_synthetic_" + existing
			switch existing {
			case "generic_set":
				require.NoError(t, c.SetSessionAccountID(ctx, 0, key, 9, time.Hour))
			case "response_set":
				require.NoError(t, c.SetResponseBinding(ctx, 0, key, 9, time.Hour))
			case "published":
				require.NoError(t, c.ReserveResponseBindings(ctx, 0, []string{key}, "newer"))
				require.NoError(t, c.CreateResponseBinding(collabBoundaryContext("newer", time.Now().Add(time.Minute)), 0, key, 9, time.Hour))
			}
			require.NoError(t, c.ReserveResponseBindings(ctx, 0, []string{key}, "late"))
			if existing == "expired" {
				f.FastForward(61 * time.Second)
			}
			require.NoError(t, c.CreateResponseBinding(collabBoundaryContext("late", time.Now().Add(10*time.Hour)), 0, key, 7, time.Hour))
			actual, _ := c.GetSessionAccountID(ctx, 0, key)
			expected := int64(9)
			if existing == "expired" {
				expected = 0
			}
			if existing == "missing" {
				expected = 7
			}
			t.Logf("case=%s expected=%d observed=%d", existing, expected, actual)
			require.Equal(t, expected, actual)
		})
	}
}

func TestCollabReservationPublisherOwnership(t *testing.T) {
	for _, owner := range []bool{false, true} {
		for _, same := range []bool{false, true} {
			t.Run(fmt.Sprintf("owner_%t/same_request_%t", owner, same), func(t *testing.T) {
				cache, _ := collabBoundaryRedis(t)
				store := service.NewOpenAIWSStateStore(cache)
				publisher := store.(service.CollabResponseIdentityStore)
				prepare := func(value int64, identity string) func(context.Context) error {
					if owner {
						return publisher.PrepareCollabHTTPResponseOwner(0, "resp_ownership", value, value*10, time.Hour, identity)
					}
					return publisher.PrepareCollabResponseAccount(0, "resp_ownership", value, time.Hour, identity)
				}
				ctx := context.Background()
				first := prepare(7, "request_a")
				require.NoError(t, first(ctx))
				next := "request_b"
				if same {
					next = "request_a"
				}
				require.NoError(t, prepare(9, next)(ctx))
				require.NoError(t, first(ctx))
				want := int64(7)
				if same {
					want = 9
				}
				for _, reader := range []service.OpenAIWSStateStore{store, service.NewOpenAIWSStateStore(cache)} {
					if owner {
						user, key, found, err := reader.GetHTTPResponseOwner(ctx, 0, "resp_ownership")
						require.NoError(t, err)
						require.True(t, found)
						require.Equal(t, want, user)
						require.Equal(t, want*10, key)
					} else {
						actual, err := reader.GetResponseAccount(ctx, 0, "resp_ownership")
						require.NoError(t, err)
						require.Equal(t, want, actual)
					}
				}
			})
		}
	}
}

func TestCollabReservationMixedConditionalDeleteBound(t *testing.T) {
	const oldDelete = `local current = redis.call('GET', KEYS[1]); if current == ARGV[1] then redis.call('DEL', KEYS[1], KEYS[2]); return 1 end; return 0`
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			cache, fixture := collabBoundaryRedis(t)
			ctx := context.Background()
			key := "resp_mixed"
			require.NoError(t, cache.ReserveResponseBindings(ctx, 0, []string{key}, "request:pending"))
			require.NoError(t, cache.rdb.Eval(ctx, oldDelete, sessionBindingKeys(0, key), 8).Err())
			if expired {
				fixture.FastForward(service.CollabBindingPublicationLifetime)
			}
			require.NoError(t, cache.CreateResponseBinding(collabBoundaryContext("request:pending", time.Now().Add(time.Hour)), 0, key, 7, time.Hour))
			got, _ := cache.GetSessionAccountID(ctx, 0, key)
			want := int64(7)
			if expired {
				want = 0
			}
			require.Equal(t, want, got)
		})
	}
}

func TestCollabReservationOrdinaryMutationWins(t *testing.T) {
	for _, mutation := range []string{"set", "delete"} {
		t.Run(mutation, func(t *testing.T) {
			cache, _ := collabBoundaryRedis(t)
			ctx := context.Background()
			key := "resp_authority"
			require.NoError(t, cache.ReserveResponseBindings(ctx, 0, []string{key}, "request:first"))
			require.NoError(t, cache.CreateResponseBinding(collabBoundaryContext("request:first", time.Now().Add(time.Minute)), 0, key, 7, time.Hour))
			require.NoError(t, cache.ReserveResponseBindings(ctx, 0, []string{key}, "request:second"))
			if mutation == "set" {
				require.NoError(t, cache.SetSessionAccountID(ctx, 0, key, 9, time.Hour))
			} else {
				require.NoError(t, cache.DeleteSessionAccountID(ctx, 0, key))
			}
			require.NoError(t, cache.CreateResponseBinding(collabBoundaryContext("request:second", time.Now().Add(time.Minute)), 0, key, 11, time.Hour))
			got, _ := cache.GetSessionAccountID(ctx, 0, key)
			want := int64(9)
			if mutation == "delete" {
				want = 0
			}
			require.Equal(t, want, got)
		})
	}
}
