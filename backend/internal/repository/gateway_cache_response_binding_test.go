//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCollabResponseBindingFence(t *testing.T) {
	fixture := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: fixture.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &gatewayCache{rdb: client}
	ctx := context.Background()
	for _, operation := range []string{"delete_missing", "delete_existing", "replace", "same_value_generation", "expired_newer_binding", "fresh"} {
		t.Run(operation, func(t *testing.T) {
			key := "openai:response:" + operation
			first := service.NewOpenAIWSStateStore(cache)
			peer := service.NewOpenAIWSStateStore(cache)
			persist := first.(service.CollabResponseIdentityStore).PrepareCollabResponseAccount(3, key, 7, time.Hour)
			want := int64(0)
			switch operation {
			case "delete_existing":
				require.NoError(t, peer.BindResponseAccount(ctx, 3, key, 4, time.Hour))
				fallthrough
			case "delete_missing":
				require.NoError(t, peer.DeleteResponseAccount(ctx, 3, key))
			case "replace":
				want = 9
				require.NoError(t, peer.BindResponseAccount(ctx, 3, key, want, time.Hour))
			case "same_value_generation":
				want = 7
				require.NoError(t, peer.BindResponseAccount(ctx, 3, key, want, time.Hour))
			case "expired_newer_binding":
				require.NoError(t, peer.BindResponseAccount(ctx, 3, key, 9, time.Millisecond))
				fixture.FastForward(2 * time.Millisecond)
			case "fresh":
				want = 7
			}
			require.NoError(t, persist(ctx))
			got, err := service.NewOpenAIWSStateStore(cache).GetResponseAccount(ctx, 3, key)
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
}
