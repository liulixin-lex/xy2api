//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOwnerGuardRealRedis(t *testing.T) {
	db := newLocalRedisClient(t)
	cache, ctx := &gatewayCache{rdb: db}, context.Background()
	const group = int64(7)
	for _, tc := range []struct {
		name                                 string
		expired, legacyWrite, reset, blocked bool
	}{
		{name: "live", blocked: true}, {name: "expired", expired: true}, {name: "submillisecond_reset", reset: true},
		{name: "legacy_write", legacyWrite: true, blocked: true}, {name: "reset_legacy_write", legacyWrite: true, reset: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary, alias := "primary-"+tc.name, "alias-"+tc.name
			require.NoError(t, cache.SetSessionAccountID(ctx, group, alias, 2, time.Hour))
			binding, err := cache.ResolveSessionBinding(ctx, group, primary, alias, true)
			require.NoError(t, err)
			opts := service.BindingCommitOptions{FailureScope: "sticky_change:" + primary, RejectScope: "first_output:" + primary}
			expiry, err := cache.RecordSessionBindingFailure(ctx, group, primary, opts.RejectScope, binding, 1, time.Hour)
			require.NoError(t, err)
			// A shorter renewal must return the longer deadline, exactly as stored.
			renewed, err := cache.RecordSessionBindingFailure(ctx, group, primary, opts.RejectScope, binding, 1, time.Minute)
			require.NoError(t, err)
			require.Equal(t, expiry, renewed)
			avoidKey, metaKey := sessionKey(resilienceSessionAvoidPrefix, group, opts.RejectScope), sessionKey(resilienceSessionAvoidWrittenPrefix, group, opts.RejectScope)
			written, err := db.HGet(ctx, metaKey, "1").Int64()
			require.NoError(t, err)
			if tc.reset {
				opts.TargetResetAt = time.UnixMilli(written).Add(time.Microsecond)
			}
			if tc.legacyWrite {
				require.NoError(t, db.HDel(ctx, metaKey, "1").Err())
			}
			if tc.expired {
				require.NoError(t, db.ZAdd(ctx, avoidKey, redis.Z{Score: float64(time.Now().Add(-time.Second).UnixMilli()), Member: "1"}).Err())
			}
			failed, err := cache.RecordSessionBindingFailure(ctx, group, primary, opts.FailureScope, binding, 1, time.Minute)
			require.NoError(t, err)
			require.Positive(t, failed)
			result, err := cache.CommitSessionBinding(ctx, group, primary, binding, 1, time.Hour, opts)
			require.NoError(t, err)
			require.Equal(t, binding.Generation, result.ObservedGen)
			hard, err := NewResilienceStore(db).SessionAvoid(ctx, group, opts.FailureScope)
			require.NoError(t, err)
			expectedID, expectedGeneration := int64(1), result.NewGen
			if tc.blocked {
				require.Equal(t, service.BindingTargetAvoided, result.Status)
				require.Empty(t, result.NewGen)
				require.Contains(t, hard, int64(1))
				expectedID, expectedGeneration = binding.AccountID, binding.Generation
			} else {
				require.Equal(t, service.BindingCommitted, result.Status)
				require.NotEmpty(t, result.NewGen)
				require.NotContains(t, hard, int64(1))
			}
			for _, key := range []string{primary, alias} {
				current, err := cache.GetSessionBinding(ctx, group, key)
				require.NoError(t, err)
				require.Equal(t, expectedID, current.AccountID)
				require.Equal(t, expectedGeneration, current.Generation)
			}
		})
	}
	t.Run("deadline_inclusive", func(t *testing.T) {
		const primary, scope = "boundary-session", "first_output:boundary-session"
		keys := sessionBindingKeys(group, primary)
		keys = append(keys, sessionKey(resilienceSessionAvoidPrefix, group, scope), sessionKey(resilienceSessionAvoidWrittenPrefix, group, scope))
		const at = int64(2000)
		require.NoError(t, db.ZAdd(ctx, keys[2], redis.Z{Score: float64(at), Member: "1"}).Err())
		for _, tc := range []struct {
			now    int64
			status service.BindingCommitStatus
		}{{at, service.BindingTargetAvoided}, {at + 1, service.BindingCommitted}} {
			result, err := commitSessionBindingScript.Run(ctx, db, keys, 0, "", 1, "next", 60000, 60000, 2, false, true, 0, tc.now).StringSlice()
			require.NoError(t, err)
			require.Equal(t, string(tc.status), result[0])
		}
	})

}
