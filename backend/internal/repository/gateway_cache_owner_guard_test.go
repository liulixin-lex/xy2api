//go:build unit

package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOwnerGuardRealRedis(t *testing.T) {
	addr := os.Getenv("SCHEDULING_TEST_REDIS_ADDR")
	if addr == "" {
		addr = miniredis.RunT(t).Addr()
	}
	db := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = db.Close() })
	testOwnerGuardRedis(t, db)
}
func TestOwnerGuardLocalRedis(t *testing.T) {
	testOwnerGuardRedis(t, newLocalRedisClient(t))
}
func testOwnerGuardRedis(t *testing.T, db *redis.Client) {
	t.Helper()
	prefix := uuid.NewString()
	cache, ctx := &gatewayCache{rdb: db}, context.Background()
	const group = int64(7)
	cleanup := func(t *testing.T, bindings, scopes []string) {
		t.Helper()
		var keys []string
		for _, b := range bindings {
			keys = append(keys, sessionBindingKeys(group, b)...)
		}
		for _, scope := range scopes {
			keys = append(keys, sessionKey(resilienceSessionAvoidPrefix, group, scope), sessionKey(resilienceSessionAvoidWrittenPrefix, group, scope))
		}
		t.Cleanup(func() { _ = db.Del(ctx, keys...).Err() })
	}
	for _, tc := range []struct {
		name                                 string
		expired, legacyWrite, reset, blocked bool
	}{
		{name: "live", blocked: true}, {name: "expired", expired: true}, {name: "submillisecond_reset", reset: true},
		{name: "legacy_write", legacyWrite: true, blocked: true}, {name: "reset_legacy_write", legacyWrite: true, reset: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary, alias := prefix+"primary-"+tc.name, prefix+"alias-"+tc.name
			require.NoError(t, cache.SetSessionAccountID(ctx, group, alias, 2, time.Hour))
			binding, err := cache.ResolveSessionBinding(ctx, group, primary, alias, true)
			require.NoError(t, err)
			opts := service.BindingCommitOptions{FailureScope: "sticky_change:" + primary, RejectScope: "first_output:" + primary}
			cleanup(t, []string{primary, alias}, []string{opts.RejectScope, opts.FailureScope})
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
		primary, scope := prefix+"boundary-session", "first_output:"+prefix
		cleanup(t, []string{primary}, []string{scope})
		keys := sessionBindingKeys(group, primary)
		keys = append(keys, sessionKey(resilienceSessionAvoidPrefix, group, scope), sessionKey(resilienceSessionAvoidWrittenPrefix, group, scope))
		at := time.Now().Add(time.Hour).UnixMilli()
		avoidKeys := keys[2:]
		for _, entry := range []struct{ id, expiry, stamp int64 }{{1, at, at - 10}, {9, at + 1000, at - 10}, {2, at + 1000, at}} {
			require.NoError(t, resilienceAvoidAddScript.Run(ctx, db, avoidKeys, entry.id, entry.expiry, entry.stamp, 0).Err())
		}
		members, err := resilienceAvoidReadScript.Run(ctx, db, avoidKeys, at).StringSlice()
		require.NoError(t, err)
		require.Contains(t, members, "1")
		members, err = resilienceAvoidReadScript.Run(ctx, db, avoidKeys, at+1).StringSlice()
		require.NoError(t, err)
		require.NotContains(t, members, "1")
		for _, tc := range []struct {
			now    int64
			status service.BindingCommitStatus
		}{{at, service.BindingTargetAvoided}, {at + 1, service.BindingCommitted}} {
			result, err := commitSessionBindingScript.Run(ctx, db, keys, 0, "", 1, "next", 60000, 60000, 2, false, true, 0, tc.now).StringSlice()
			require.NoError(t, err)
			require.Equal(t, string(tc.status), result[0])
		}
	})

	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("reset_late_%v", late), func(t *testing.T) {
			primary := fmt.Sprintf("%s-binding-%v", prefix, late)
			scope := "first_output:" + primary
			cleanup(t, []string{primary}, []string{scope})
			require.NoError(t, cache.SetSessionAccountID(ctx, group, primary, 1, time.Hour))
			binding, err := cache.GetSessionBinding(ctx, group, primary)
			require.NoError(t, err)
			avoidKey := sessionKey(resilienceSessionAvoidPrefix, group, scope)
			metaKey := sessionKey(resilienceSessionAvoidWrittenPrefix, group, scope)
			keys := append([]string{avoidKey}, sessionBindingKeys(group, primary)...)
			keys = append(keys, metaKey)
			reset := time.Now().Add(-time.Second).UnixMilli()
			before, after := reset-10, reset+10
			deadline := after + 60000
			write := func(stamp, expiry, cutoff int64) int64 {
				n, err := recordFirstOutputAvoidScript.Run(ctx, db, keys, 1, expiry, stamp, 1, binding.Generation, cutoff).Int64()
				require.NoError(t, err)
				return n
			}
			if !late {
				require.Equal(t, deadline+50000, write(before, deadline+50000, 0))
			}
			require.Equal(t, deadline, write(after, deadline, reset))
			if late {
				require.Zero(t, write(before, deadline+50000, 0))
			}
			records, err := NewResilienceStore(db).SessionAvoidWrittenAt(ctx, group, scope)
			require.NoError(t, err)
			require.Equal(t, time.UnixMilli(after), records[1])
			require.Equal(t, float64(deadline), db.ZScore(ctx, avoidKey, "1").Val())
			moved, err := cache.CommitSessionBinding(ctx, group, primary, binding, 2, time.Hour, service.BindingCommitOptions{})
			require.NoError(t, err)
			require.Equal(t, service.BindingCommitted, moved.Status)
			refused, err := cache.CommitSessionBinding(ctx, group, primary, service.StickySessionBinding{AccountID: 2, Generation: moved.NewGen}, 1, time.Hour, service.BindingCommitOptions{RejectScope: scope, TargetResetAt: time.UnixMilli(reset)})
			require.NoError(t, err)
			require.Equal(t, service.BindingTargetAvoided, refused.Status)
		})
	}
}
