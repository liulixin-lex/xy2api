//go:build unit

package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCollabRecordsSharedReservedCapacityAndAtomicLimit(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	shared := (&gatewayCache{rdb: client}).CollabRecords()
	ctx := context.Background()
	scope := resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}
	for i := 0; i < 100; i++ {
		require.NoError(t, shared.Apply(ctx, resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationPlaintext, Plaintext: resilience.CollabPlaintextRecord{Scope: scope, Call: resilience.CollabDigest(fmt.Sprint(i)), Arguments: "args", ExpiresAt: time.Now().Add(time.Hour)}}))
	}
	key := resilience.CollabDeliveryKey{Scope: scope, Thread: "thread", Item: "item", Delivery: "delivery", Digest: "cipher"}
	copies := make([]resilience.CollabDigest, 17)
	for i := range copies {
		copies[i] = resilience.CollabDigest(fmt.Sprint(i))
	}
	require.NoError(t, shared.Apply(ctx, resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationTerminated, Terminated: resilience.CollabTerminatedRecord{Key: key, Event: "notice", Copies: copies, ExpiresAt: time.Now().Add(time.Hour)}}))
	require.NoError(t, shared.Apply(ctx, resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationReplaced, Replaced: resilience.CollabReplacedRecord{Key: key, Account: "account", Placeholder: resilience.CollabPlaceholderMessage, ExpiresAt: time.Now().Add(time.Hour)}}))
	a := resilience.NewSafeCollabRecordsStore(resilience.NewMemoryCollabRecordsStore(), shared, 1)
	defer a.Close()
	b := resilience.NewSafeCollabRecordsStore(resilience.NewMemoryCollabRecordsStore(), shared, 1)
	defer b.Close()
	var wg sync.WaitGroup
	var accepted atomic.Int64
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			store := a
			if i%2 == 1 {
				store = b
			}
			_, ok := store.ReserveTreeLocalEnding(ctx, scope, resilience.CollabDigest(fmt.Sprint(i)))
			if ok {
				accepted.Add(1)
			}
		})
	}
	wg.Wait()
	require.Equal(t, int64(3), accepted.Load())
	q := resilience.CollabRecordQuery{Key: key}
	result, err := shared.LookupBatch(ctx, resilience.CollabRecordBatch{Scope: scope, Queries: []resilience.CollabRecordQuery{q}, Dispositions: true})
	require.NoError(t, err)
	require.True(t, result.TreeLocalEndingsKnown)
	require.Equal(t, 3, result.LocalEndingCount())
	require.True(t, result.DispositionsComplete)
	require.Len(t, result.Dispositions[q].Terminated, 1)
	require.Len(t, result.Dispositions[q].Replaced, 1)
	require.Len(t, result.Dispositions[q].Terminated[0].Copies, resilience.CollabRecordMaxCopies)
	restarted := resilience.NewSafeCollabRecordsStore(resilience.NewMemoryCollabRecordsStore(), shared, 1)
	defer restarted.Close()
	count, ok := restarted.ReserveTreeLocalEnding(ctx, scope, "overflow")
	require.False(t, ok)
	require.Equal(t, 3, count)
}
func TestCollabRecordsSharedTenantIsolationAndEviction(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	shared := (&gatewayCache{rdb: client}).CollabRecords()
	ctx := context.Background()
	protected := resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "protected"}
	key := resilience.CollabDeliveryKey{Scope: protected, Thread: "thread", Item: "item", Delivery: "delivery", Digest: "cipher"}
	require.NoError(t, shared.Apply(ctx, resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationTerminated, Terminated: resilience.CollabTerminatedRecord{Key: key, Event: "event", ExpiresAt: time.Now().Add(time.Hour)}}))
	for i := 0; i < resilience.CollabRecordMaxTenantTrees+20; i++ {
		scope := protected
		scope.Tree = resilience.CollabDigest(fmt.Sprint(i))
		require.NoError(t, shared.Apply(ctx, resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationPlaintext, Plaintext: resilience.CollabPlaintextRecord{Scope: scope, Call: "call", Arguments: "args", ExpiresAt: time.Now().Add(time.Hour)}}))
	}
	require.Equal(t, int64(resilience.CollabRecordMaxTenantTrees), client.ZCard(ctx, collabCacheKeys(protected)[4]).Val())
	oldest := protected
	oldest.Tree = "0"
	require.Zero(t, client.HLen(ctx, collabCacheKeys(oldest)[1]).Val())
	newest := protected
	newest.Tree = resilience.CollabDigest(fmt.Sprint(resilience.CollabRecordMaxTenantTrees + 19))
	require.Equal(t, int64(1), client.HLen(ctx, collabCacheKeys(newest)[1]).Val())
	q := resilience.CollabRecordQuery{Key: key}
	result, err := shared.LookupBatch(ctx, resilience.CollabRecordBatch{Scope: protected, Queries: []resilience.CollabRecordQuery{q}, Dispositions: true})
	require.NoError(t, err)
	require.Len(t, result.Dispositions[q].Terminated, 1)
	// Fill this tenant with non-evictable dispositions: another tenant still enters.
	for i := 0; i < resilience.CollabRecordMaxTenantTrees; i++ {
		k := key
		k.Scope.Tree = resilience.CollabDigest("protected-" + fmt.Sprint(i))
		err := shared.Apply(ctx, resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationTerminated, Terminated: resilience.CollabTerminatedRecord{Key: k, Event: "event", ExpiresAt: time.Now().Add(time.Hour)}})
		if i == resilience.CollabRecordMaxTenantTrees-1 {
			require.ErrorIs(t, err, errCollabAdmission)
		} else {
			require.NoError(t, err)
		}
	}
	unavailable := protected
	unavailable.Tree = "unavailable"
	result, err = shared.LookupBatch(ctx, resilience.CollabRecordBatch{Scope: unavailable, Dispositions: true})
	require.NoError(t, err)
	require.False(t, result.TreeLocalEndingsKnown)
	require.Equal(t, 3, result.LocalEndingCount())
	other := protected
	other.Tenant = "other"
	reserver, ok := shared.(resilience.CollabLocalEndingReserver)
	require.True(t, ok)
	count, ok := reserver.ReserveTreeLocalEnding(ctx, other, "notice")
	require.True(t, ok)
	require.Equal(t, 1, count)
	server.FastForward(resilience.CollabRecordTTL + time.Second)
	count, ok = reserver.ReserveTreeLocalEnding(ctx, unavailable, "notice")
	require.True(t, ok)
	require.Equal(t, 1, count)
}
