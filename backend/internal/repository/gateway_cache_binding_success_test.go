package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestStickyBindingSuccessClearsEarlierFailure(t *testing.T) {
	addr := os.Getenv("SCHEDULING_TEST_REDIS_ADDR")
	if addr == "" {
		addr = miniredis.RunT(t).Addr()
	}
	testStickyBindingSuccessClearsEarlierFailure(t, addr)
}

func testStickyBindingSuccessClearsEarlierFailure(t *testing.T, addr string) {
	t.Helper()
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprintf("alias=%t", alias), func(t *testing.T) {
			ctx := context.Background()
			first := redis.NewClient(&redis.Options{Addr: addr})
			second := redis.NewClient(&redis.Options{Addr: addr})
			one, two := &gatewayCache{rdb: first}, &gatewayCache{rdb: second}
			const group = int64(-1024)
			primary, legacy := "openai:v2:"+uuid.NewString(), "openai:"+uuid.NewString()
			soft := uuid.NewString()
			avoid := "sticky_change:" + soft
			avoidKey, writtenKey := sessionKey(resilienceSessionAvoidPrefix, group, avoid), sessionKey(resilienceSessionAvoidWrittenPrefix, group, avoid)
			keys := append(sessionBindingKeys(group, primary), sessionBindingKeys(group, legacy)...)
			keys = append(keys, avoidKey, writtenKey, sessionKey(resilienceSessionAvoidPrefix, group, soft), sessionKey(resilienceSessionAvoidWrittenPrefix, group, soft))
			t.Cleanup(func() { _ = first.Del(ctx, keys...).Err(); _ = first.Close(); _ = second.Close() })
			require.NoError(t, one.SetSessionAccountID(ctx, group, primary, 100, time.Hour))
			expected, err := one.GetSessionBinding(ctx, group, primary)
			require.NoError(t, err)
			if alias {
				require.NoError(t, one.SetSessionAccountID(ctx, group, legacy, 100, time.Hour))
				expected, err = one.ResolveSessionBinding(ctx, group, primary, legacy, true)
				require.NoError(t, err)
			}
			for _, target := range []int64{101, 102} {
				recorded, err := one.RecordSessionBindingFailure(ctx, group, primary, avoid, expected, target, 2*time.Minute)
				require.NoError(t, err)
				require.True(t, recorded)
			}
			require.NoError(t, NewResilienceStore(first).AddSessionAvoid(ctx, group, soft, 101, 10*time.Minute))
			stale := expected
			stale.Generation += "-stale"
			changed, err := two.CommitSessionBinding(ctx, group, primary, stale, 101, time.Hour, avoid)
			require.NoError(t, err)
			require.False(t, changed)
			require.True(t, first.HExists(ctx, writtenKey, "101").Val(), "failed CAS must not clear failure memory")
			changed, err = two.CommitSessionBinding(ctx, group, primary, expected, 101, time.Hour, avoid)
			require.NoError(t, err)
			require.True(t, changed)
			avoided, err := NewResilienceStore(first).SessionAvoid(ctx, group, avoid)
			require.NoError(t, err)
			require.NotContains(t, avoided, int64(101), "a concurrent success supersedes the earlier failure for its target")
			require.Contains(t, avoided, int64(102), "other failed targets remain excluded")
			require.False(t, first.HExists(ctx, writtenKey, "101").Val())
			require.True(t, first.HExists(ctx, writtenKey, "102").Val())
			require.Greater(t, first.PTTL(ctx, avoidKey).Val(), 100*time.Second)
			require.InDelta(t, first.PTTL(ctx, avoidKey).Val().Milliseconds(), first.PTTL(ctx, writtenKey).Val().Milliseconds(), 100)
			softAvoid, err := NewResilienceStore(first).SessionAvoid(ctx, group, soft)
			require.NoError(t, err)
			require.Contains(t, softAvoid, int64(101), "ordinary soft avoidance is independent")
			current, err := one.GetSessionBinding(ctx, group, primary)
			require.NoError(t, err)
			require.EqualValues(t, 101, current.AccountID)
			require.NotEqual(t, expected.Generation, current.Generation)
			if alias {
				aliased, err := two.GetSessionBinding(ctx, group, legacy)
				require.NoError(t, err)
				require.Equal(t, current, aliased)
			}
			recorded, err := one.RecordSessionBindingFailure(ctx, group, primary, avoid, expected, 101, 2*time.Minute)
			require.NoError(t, err)
			require.False(t, recorded, "a late old-generation failure cannot restore the removed avoidance")
		})
	}
}
