//go:build unit

package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollabRefusedPublicationLocalView(t *testing.T) {
	for _, kind := range []string{"account", "owner"} {
		for _, winner := range []string{"publisher", "ordinary", "later_set", "later_delete"} {
			t.Run(kind+"/"+winner, func(t *testing.T) {
				cache, _ := collabBoundaryRedis(t)
				ctx := context.Background()
				first := service.NewOpenAIWSStateStore(cache)
				second := service.NewOpenAIWSStateStore(cache)
				const id = "resp_collision"
				prepare := func(store service.OpenAIWSStateStore, value int64, publisher string) func(context.Context) error {
					pub := store.(service.CollabResponseIdentityStore)
					if kind == "owner" {
						return pub.PrepareCollabHTTPResponseOwner(3, id, value, value*10, time.Hour, publisher)
					}
					return pub.PrepareCollabResponseAccount(3, id, value, time.Hour, publisher)
				}
				bind := func() {
					if kind == "owner" {
						require.NoError(t, first.BindHTTPResponseOwner(ctx, 3, id, 7, 70, time.Hour))
					} else {
						require.NoError(t, first.BindResponseAccount(ctx, 3, id, 7, time.Hour))
					}
				}
				if winner == "publisher" {
					require.NoError(t, prepare(first, 7, "request_first")(ctx))
				} else if winner == "ordinary" {
					bind()
				}
				persist := prepare(second, 9, "request_second")
				if winner == "later_set" {
					bind()
				} else if winner == "later_delete" {
					if kind == "owner" {
						for _, prefix := range []string{"openai:http-response-owner:user:", "openai:http-response-owner:key:"} {
							require.NoError(t, cache.DeleteSessionAccountID(ctx, 3, prefix+fmt.Sprintf("%x", sha256.Sum256([]byte(id)))))
						}
					} else {
						require.NoError(t, first.DeleteResponseAccount(ctx, 3, id))
					}
				}
				require.NoError(t, persist(ctx), "refusal is not a store failure")
				for _, reader := range []struct {
					name  string
					store service.OpenAIWSStateStore
				}{{"fresh_peer", service.NewOpenAIWSStateStore(cache)}, {"refused_publisher", second}} {
					t.Run(reader.name, func(t *testing.T) {
						if kind == "owner" {
							user, key, found, err := reader.store.GetHTTPResponseOwner(ctx, 3, id)
							if winner == "later_delete" {
								require.ErrorIs(t, err, service.ErrStickySessionNotFound)
								require.False(t, found)
								require.Zero(t, user)
								require.Zero(t, key)
							} else {
								require.NoError(t, err)
								require.True(t, found)
								assert.Equal(t, int64(7), user)
								assert.Equal(t, int64(70), key)
							}
						} else {
							account, err := reader.store.GetResponseAccount(ctx, 3, id)
							require.NoError(t, err)
							if winner == "later_delete" {
								require.Zero(t, account)
							} else {
								require.Equal(t, int64(7), account)
							}
						}
					})
				}
			})
		}
	}
}

func TestCollabRefusedOwnerKeyLocalView(t *testing.T) {
	cache, _ := collabBoundaryRedis(t)
	ctx := context.Background()
	store := service.NewOpenAIWSStateStore(cache)
	const id = "resp_owner_key"
	persist := store.(service.CollabResponseIdentityStore).PrepareCollabHTTPResponseOwner(3, id, 9, 90, time.Hour)
	key := "openai:http-response-owner:key:" + fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
	require.NoError(t, cache.SetSessionAccountID(ctx, 3, key, 70, time.Hour))
	require.NoError(t, persist(ctx))
	for _, reader := range []service.OpenAIWSStateStore{service.NewOpenAIWSStateStore(cache), store} {
		user, key, found, err := reader.GetHTTPResponseOwner(ctx, 3, id)
		require.NoError(t, err)
		require.True(t, found)
		require.EqualValues(t, 9, user)
		require.EqualValues(t, 70, key)
	}
}

func TestCollabRefusalDiffersFromStoreFailure(t *testing.T) {
	cache, _ := collabBoundaryRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	accepted, err := cache.ReserveResponseBindings(ctx, 0, []string{"resp_error"}, "request:pending")
	require.False(t, accepted)
	require.ErrorIs(t, err, context.Canceled)
	ctx = collabBoundaryPublicationContext{ctx, service.CollabBindingPublication{Token: "request:pending", Expires: time.Now().Add(time.Minute)}}
	accepted, err = cache.CreateResponseBinding(ctx, 0, "resp_error", 7, time.Hour)
	require.False(t, accepted)
	require.ErrorIs(t, err, context.Canceled)
}
