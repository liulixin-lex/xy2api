//go:build unit

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Supply different local expiry times through the publication contract.
// Only the reservation token can participate in Redis ordering.
type collabBoundaryPublicationContext struct {
	context.Context
	publication service.CollabBindingPublication
}

func (c collabBoundaryPublicationContext) Value(key any) any {
	if fmt.Sprintf("%T", key) == "service.collabBindingPublicationKey" {
		return c.publication
	}
	return c.Context.Value(key)
}
func collabBoundaryContext(token string, expires time.Time) context.Context {
	return collabBoundaryPublicationContext{context.Background(), service.CollabBindingPublication{Token: token, Expires: expires}}
}
func collabBoundaryRedis(t *testing.T) (*gatewayCache, *miniredis.Miniredis) {
	fixture := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: fixture.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &gatewayCache{rdb: client}, fixture
}
func TestCollabBoundaryClockSkewDeferredCreate(t *testing.T) {
	for _, action := range []string{"delete", "set", "create"} {
		t.Run(action, func(t *testing.T) {
			c, f := collabBoundaryRedis(t)
			ctx := context.Background()
			now := time.Now()
			f.SetTime(now)
			const group int64 = 3
			key := "resp_skew_" + action
			// Fast instance prepared first, but its publication has not reached Redis.
			old := collabBoundaryContext("request:earlier", now.Add(65*time.Second))
			{
				reserved, err := c.ReserveResponseBindings(ctx, group, []string{key}, "request:earlier")
				require.NoError(t, err)
				require.Equal(t, true, reserved)
			}
			want := int64(9)
			switch action {
			case "delete":
				require.NoError(t, c.DeleteResponseBinding(ctx, group, key))
				want = 0
			case "set":
				require.NoError(t, c.SetResponseBinding(ctx, group, key, 9, time.Hour))
			case "create":
				{
					reserved, err := c.ReserveResponseBindings(ctx, group, []string{key}, "request:later")
					require.NoError(t, err)
					require.Equal(t, true, reserved)
				}
				{
					created, err := c.CreateResponseBinding(collabBoundaryContext("request:later", now.Add(time.Minute)), group, key, 9, time.Hour)
					require.NoError(t, err)
					require.Equal(t, true, created)
				}
			}
			{
				created, err := c.CreateResponseBinding(old, group, key, 7, time.Hour)
				require.NoError(t, err)
				require.Equal(t, false, created)
			}
			got, err := c.GetSessionAccountID(ctx, group, key)
			if want > 0 {
				require.NoError(t, err)
			}
			t.Logf("later=%s expected=%d got=%d", action, want, got)
			require.Equal(t, want, got, "old fast-clock create must not win over a later operation")
		})
	}
}
func TestCollabBoundaryGenericDeleteErasesFence(t *testing.T) {
	for _, action := range []string{"delete", "delete_if_owner"} {
		t.Run(action, func(t *testing.T) {
			c, _ := collabBoundaryRedis(t)
			ctx := context.Background()
			key := "resp_mixed_" + action
			now := time.Now()
			old := collabBoundaryContext("request:earlier", now.Add(time.Minute))
			{
				reserved, err := c.ReserveResponseBindings(ctx, 3, []string{key}, "request:earlier")
				require.NoError(t, err)
				require.Equal(t, true, reserved)
			}
			require.NoError(t, c.SetResponseBinding(ctx, 3, key, 9, time.Hour))
			if action == "delete" {
				require.NoError(t, c.DeleteSessionAccountID(ctx, 3, key))
			} else {
				require.NoError(t, c.DeleteSessionAccountIDIfOwner(ctx, 3, key, 9))
			}
			{
				created, err := c.CreateResponseBinding(old, 3, key, 7, time.Hour)
				require.NoError(t, err)
				require.Equal(t, false, created)
			}
			got, _ := c.GetSessionAccountID(ctx, 3, key)
			t.Logf("later_api=%s got=%d", action, got)
			require.Zero(t, got, "a generic cache deletion must retain the deferred-write fence")
		})
	}
}

func TestCollabBoundaryGenericDeletePendingReservation(t *testing.T) {
	for _, action := range []string{"delete", "owner", "missing", "different_owner"} {
		t.Run(action, func(t *testing.T) {
			cache, _ := collabBoundaryRedis(t)
			ctx := context.Background()
			const key = "resp_pending"
			if action != "missing" {
				require.NoError(t, cache.SetSessionAccountID(ctx, 0, key, 9, time.Hour))
			}
			{
				reserved, err := cache.ReserveResponseBindings(ctx, 0, []string{key}, "reserved")
				require.NoError(t, err)
				require.Equal(t, action == "missing", reserved)
			}
			expected := int64(0)
			switch action {
			case "delete":
				require.NoError(t, cache.DeleteSessionAccountID(ctx, 0, key))
			case "different_owner":
				require.NoError(t, cache.DeleteSessionAccountIDIfOwner(ctx, 0, key, 8))
				expected = 9
			default:
				require.NoError(t, cache.DeleteSessionAccountIDIfOwner(ctx, 0, key, 9))
			}
			{
				created, err := cache.CreateResponseBinding(collabBoundaryContext("reserved", time.Now().Add(time.Hour)), 0, key, 7, time.Hour)
				require.NoError(t, err)
				require.Equal(t, false, created)
			}
			actual, _ := cache.GetSessionAccountID(ctx, 0, key)
			require.Equal(t, expected, actual)
		})
	}
}
