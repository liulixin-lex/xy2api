//go:build unit

package resilience

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollabTreeAffinityReleaseBeforeWritten(t *testing.T) {
	for _, reason := range []CollabLeaseRelease{CollabLeaseReleaseConsumed, CollabLeaseReleaseTerminated} {
		t.Run(string(reason), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				s := NewCollabTreeAffinity(32, time.Minute)
				lease := affinityTestLease("item")
				s.ReleaseDeliveryLease(ctx, lease.Key, reason)
				require.False(t, s.AcquireDeliveryLease(ctx, lease), "acceptance may precede the Written callback")
				require.False(t, s.HasDeliveryLeases(ctx, lease.Key.Scope))
				time.Sleep(time.Minute)
				require.True(t, s.AcquireDeliveryLease(ctx, lease), "tombstones must expire")
			})
		})
	}
}

func TestCollabTreeAffinityLookupsRetireOnlyExpiredPartition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		s := NewCollabTreeAffinity(32, time.Hour)
		lease := affinityTestLease("item")
		lease.ExpiresAt = time.Now().Add(time.Second)
		require.True(t, s.AcquireDeliveryLease(ctx, lease))
		require.True(t, s.AvoidThread(ctx, CollabThreadAvoidance{Scope: lease.Key.Scope, Thread: lease.Key.Thread, AccountID: 1, ExpiresAt: lease.ExpiresAt}))
		other := affinityTestRoot()
		other.Scope.Tree = "unrelated-tree"
		require.True(t, s.ProvisionalTreeHint(ctx, other))
		time.Sleep(time.Second)
		require.Empty(t, s.ThreadAvoidances(ctx, lease.Key.Scope, lease.Key.Thread))
		require.False(t, s.HasDeliveryLeases(ctx, lease.Key.Scope))
		require.Len(t, s.entries, 2, "lookups retire expired partition members without sweeping unrelated entries")
		_, ok := s.LookupTreeHint(ctx, other.Scope)
		require.True(t, ok)
	})
}

func BenchmarkCollabTreeAffinityPartitionLookups(b *testing.B) {
	for _, size := range []int{16, CollabRecordMaxEntries} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			s := NewCollabTreeAffinity(CollabRecordMaxEntries, time.Hour)
			ctx := context.Background()
			for i := range size {
				scope := affinityTestRoot().Scope
				scope.Tree = CollabDigest(fmt.Sprintf("tree-%d", i))
				s.AvoidThread(ctx, CollabThreadAvoidance{Scope: scope, Thread: "thread", AccountID: 1, ExpiresAt: time.Now().Add(time.Hour)})
			}
			scope := affinityTestRoot().Scope
			b.ResetTimer()
			for b.Loop() {
				s.ThreadAvoidances(ctx, scope, "thread")
				s.HasDeliveryLeases(ctx, scope)
			}
		})
	}
}

func TestCollabTreeAffinityIndexEvictionAndRelease(t *testing.T) {
	ctx := context.Background()
	s := NewCollabTreeAffinity(2, time.Minute)
	lease := affinityTestLease("first")
	require.True(t, s.AcquireDeliveryLease(ctx, lease))
	avoid := CollabThreadAvoidance{Scope: lease.Key.Scope, Thread: lease.Key.Thread, AccountID: 1, ExpiresAt: time.Now().Add(time.Minute)}
	require.True(t, s.AvoidThread(ctx, avoid))
	other := lease
	other.Key.Scope.Tree = "other-tree"
	other.Issued.Scope = other.Key.Scope
	require.True(t, s.AcquireDeliveryLease(ctx, other))
	require.False(t, s.HasDeliveryLeases(ctx, lease.Key.Scope), "eviction removes the tree index entry")
	require.True(t, s.ThreadAvoided(ctx, avoid))
	s.ReleaseDeliveryLease(ctx, other.Key, CollabLeaseReleaseConsumed)
	require.Empty(t, s.leases, "released tombstones are not active leases")
	s.ReleaseDeliveryLease(ctx, lease.Key, CollabLeaseReleaseTerminated)
	require.Empty(t, s.avoidances, "eviction removes the thread index entry")
	require.False(t, s.AcquireDeliveryLease(ctx, other))
	require.False(t, s.AcquireDeliveryLease(ctx, lease))
}
