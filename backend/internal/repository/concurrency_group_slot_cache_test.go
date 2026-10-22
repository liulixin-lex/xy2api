//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Group slots are counted from the member suffix with the same strict cutoffs
// as PeekAccountsLoadBatch, and the read never trims anything.
func TestGetAccountGroupConcurrencyBatchCountsSuffixedMembersReadOnly(t *testing.T) {
	m := miniredis.RunT(t)
	now := time.Now().Truncate(time.Second)
	m.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: m.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	cache, ok := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	require.True(t, ok)

	slotCutoff := float64(now.Unix() - 900)
	liveCutoff := float64(now.Unix() - 60)
	require.NoError(t, client.ZAdd(ctx, accountSlotKeyPrefix+"1",
		redis.Z{Score: float64(now.Unix()), Member: "rp-1|g10"},
		redis.Z{Score: float64(now.Unix()), Member: "rp-2|g10"},
		redis.Z{Score: float64(now.Unix()), Member: "rp-3|g20"},
		redis.Z{Score: float64(now.Unix()), Member: "rp-4"}, // no group: counted in the total only
		redis.Z{Score: slotCutoff + 1, Member: "rp-5|g20"},
		redis.Z{Score: slotCutoff, Member: "rp-6|g10"}, // expired at the boundary
		redis.Z{Score: slotCutoff - 1, Member: "rp-7|g10"},
	).Err())
	require.NoError(t, client.ZAdd(ctx, liveAccountSlotKeyPrefix+"1",
		redis.Z{Score: float64(now.Unix()), Member: "rp-8|g20"},
		redis.Z{Score: liveCutoff, Member: "rp-9|g10"}, // expired live lease
	).Err())
	require.NoError(t, client.ZAdd(ctx, accountSlotKeyPrefix+"2",
		redis.Z{Score: float64(now.Unix()), Member: "rold-1"}, // process from before the suffix
	).Err())
	before := m.Dump()

	got, err := cache.GetAccountGroupConcurrencyBatch(ctx, []int64{1, 2, 3, 1})
	require.NoError(t, err)
	require.Equal(t, map[int64]map[int64]int{1: {10: 2, 20: 3}}, got)
	require.Equal(t, before, m.Dump(), "the read must not trim expired members")

	// The green number never exceeds the account's current concurrency.
	load, err := cache.PeekAccountsLoadBatch(ctx, []service.AccountWithConcurrency{{ID: 1, MaxConcurrency: 10}})
	require.NoError(t, err)
	require.Equal(t, 6, load[1].CurrentConcurrency)

	empty, err := cache.GetAccountGroupConcurrencyBatch(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

// A slot taken through the service with a group in context is counted for
// that group and drops out when it is released.
func TestAccountGroupSlotLifecycleThroughTheService(t *testing.T) {
	m := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: m.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	svc := service.NewConcurrencyService(NewConcurrencyCache(client, 15, 900))

	groupCtx := context.WithValue(ctx, ctxkey.Group, &service.Group{ID: 10})
	first, err := svc.AcquireAccountSlot(groupCtx, 1, 3)
	require.NoError(t, err)
	require.True(t, first.Acquired)
	second, err := svc.AcquireAccountSlot(ctx, 1, 3)
	require.NoError(t, err)
	require.True(t, second.Acquired)

	require.Equal(t, map[int64]map[int64]int{1: {10: 1}}, svc.GetAccountGroupConcurrencyBatch(ctx, []int64{1}))
	total, err := svc.GetAccountConcurrencyBatch(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, 2, total[1])

	first.ReleaseFunc()
	require.Empty(t, svc.GetAccountGroupConcurrencyBatch(ctx, []int64{1}))
	second.ReleaseFunc()
	total, err = svc.GetAccountConcurrencyBatch(ctx, []int64{1})
	require.NoError(t, err)
	require.Zero(t, total[1])
}
