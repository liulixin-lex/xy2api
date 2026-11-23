//go:build unit

package resilience

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func affinityTestRoot() CollabTreeHint {
	return CollabTreeHint{Scope: CollabRecordScope{Tenant: "tenant-digest", Group: "group-digest", Tree: "tree-digest"}, Thread: "root-digest", AccountID: 1}
}

func affinityTestLease(item string) CollabDeliveryLease {
	scope := affinityTestRoot().Scope
	return CollabDeliveryLease{Key: CollabDeliveryKey{Scope: scope, Thread: "child-digest", Item: CollabDigest(item), Delivery: "delivery-digest", Digest: "ciphertext-digest"}, Issued: CollabIssuedToken{Scope: scope, Token: "token-digest", Call: "call-digest", Generation: 1}, AccountID: 1}
}

func TestCollabTreeAffinityAdmissionAndIsolation(t *testing.T) {
	ctx := context.Background()
	s := NewCollabTreeAffinity(32, time.Hour)
	root := affinityTestRoot()
	require.True(t, s.ProvisionalTreeHint(ctx, root))
	hint, ok := s.LookupTreeHint(ctx, root.Scope)
	require.True(t, ok, "admission must publish before executable delivery")
	require.Equal(t, CollabTreeHintStatusProvisional, hint.Status)
	child := root
	child.Thread, child.Parent, child.AccountID = "child-digest", root.Thread, 2
	require.False(t, s.ProvisionalTreeHint(ctx, child))
	require.True(t, s.RefreshTreeMembership(ctx, child))
	unknownTree := root.Scope
	unknownTree.Tree = ""
	member, ok := s.LookupThread(ctx, unknownTree, child.Thread)
	require.True(t, ok)
	require.Equal(t, root.Scope, member.Scope, "grandchildren can resolve the parent's tree")
	require.True(t, s.ConfirmTreeHint(ctx, root))
	hint, ok = s.LookupTreeHint(ctx, root.Scope)
	require.True(t, ok)
	require.Equal(t, root.AccountID, hint.AccountID)
	require.Equal(t, CollabTreeHintStatusConfirmed, hint.Status)
	for _, scope := range []CollabRecordScope{{Tenant: "another-tenant", Group: root.Scope.Group, Tree: root.Scope.Tree}, {Tenant: root.Scope.Tenant, Group: "another-group", Tree: root.Scope.Tree}, {Tenant: root.Scope.Tenant, Group: root.Scope.Group, Tree: "another-tree"}} {
		_, ok = s.LookupTreeHint(ctx, scope)
		require.False(t, ok)
		_, ok = s.LookupThread(ctx, scope, child.Thread)
		require.False(t, ok)
	}
	changedRoot := root
	changedRoot.AccountID = 3
	require.True(t, s.ProvisionalTreeHint(ctx, changedRoot))
	require.False(t, s.ConfirmTreeHint(ctx, root), "late success must not confirm a different admission")
	require.True(t, s.ConfirmTreeHint(ctx, changedRoot))
}

func TestCollabTreeAffinityExpiryAndRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		s := NewCollabTreeAffinity(32, time.Minute)
		root := affinityTestRoot()
		require.True(t, s.ProvisionalTreeHint(ctx, root))
		child := root
		child.Thread, child.Parent = "child-digest", root.Thread
		require.True(t, s.RefreshTreeMembership(ctx, child))
		time.Sleep(40 * time.Second)
		grandchild := child
		grandchild.Thread, grandchild.Parent = "grandchild-digest", child.Thread
		require.True(t, s.RefreshTreeMembership(ctx, grandchild))
		time.Sleep(30 * time.Second)
		_, ok := s.LookupTreeHint(ctx, root.Scope)
		require.True(t, ok)
		_, ok = s.LookupThread(ctx, root.Scope, child.Thread)
		require.True(t, ok, "tree activity renews older membership")
		time.Sleep(30 * time.Second)
		_, ok = s.LookupTreeHint(ctx, root.Scope)
		require.False(t, ok)
		_, ok = s.LookupThread(ctx, root.Scope, child.Thread)
		require.False(t, ok, "lookups must not renew TTL")
	})
}

func TestCollabTreeAffinityThreadRecoveryAndAvoidanceTiming(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		s := NewCollabTreeAffinity(32, time.Minute)
		root := affinityTestRoot()
		require.True(t, s.ProvisionalTreeHint(ctx, root))
		avoid := CollabThreadAvoidance{Scope: root.Scope, Thread: "child-digest", AccountID: root.AccountID, ExpiresAt: time.Now().Add(2 * time.Minute)}
		require.True(t, s.AvoidThread(ctx, avoid))
		require.True(t, s.ThreadAvoided(ctx, avoid), "client retry need not wait for final failure")
		require.Empty(t, s.ThreadAvoidances(ctx, root.Scope, root.Thread))
		require.Empty(t, s.ThreadAvoidances(ctx, root.Scope, "sibling-digest"))
		route := CollabThreadRoute{Scope: root.Scope, Thread: avoid.Thread, AccountID: 2}
		require.True(t, s.BindRecoveredThread(ctx, route))
		got, ok := s.LookupThreadRoute(ctx, root.Scope, avoid.Thread)
		require.True(t, ok)
		require.Equal(t, int64(2), got.AccountID)
		hint, ok := s.LookupTreeHint(ctx, root.Scope)
		require.True(t, ok)
		require.Equal(t, root.AccountID, hint.AccountID)
		time.Sleep(time.Minute)
		require.True(t, s.ThreadAvoided(ctx, avoid), "tree TTL must not shorten today's avoidance")
		time.Sleep(time.Minute)
		require.False(t, s.ThreadAvoided(ctx, avoid))
	})
}

func TestCollabTreeAffinityLeasesArePerDelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		s := NewCollabTreeAffinity(32, time.Minute)
		first, second := affinityTestLease("first-item"), affinityTestLease("second-item")
		require.True(t, s.AcquireDeliveryLease(ctx, first))
		require.True(t, s.AcquireDeliveryLease(ctx, second))
		require.True(t, s.AcquireDeliveryLease(ctx, first), "completion replay is idempotent")
		s.ReleaseDeliveryLease(ctx, first.Key, CollabLeaseReleaseConsumed)
		require.True(t, s.HasDeliveryLeases(ctx, first.Key.Scope))
		require.False(t, s.AcquireDeliveryLease(ctx, first), "a read delivery cannot be resurrected")
		s.ReleaseDeliveryLease(ctx, second.Key, CollabLeaseReleaseTerminated)
		require.False(t, s.HasDeliveryLeases(ctx, first.Key.Scope))
		third := first
		third.Key.Delivery = "another-delivery"
		require.True(t, s.AcquireDeliveryLease(ctx, third), "identical ciphertext in a new delivery is independent")
		time.Sleep(40 * time.Second)
		require.True(t, s.AcquireDeliveryLease(ctx, third))
		time.Sleep(20 * time.Second)
		require.False(t, s.HasDeliveryLeases(ctx, first.Key.Scope), "duplicate frames do not extend a lease")
	})
}

func TestCollabTreeAffinityBoundedConcurrentUse(t *testing.T) {
	ctx := context.Background()
	s := NewCollabTreeAffinity(16, time.Hour)
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			root := affinityTestRoot()
			root.Scope.Tree = CollabDigest(fmt.Sprintf("tree-%d", i))
			root.Thread = CollabDigest(fmt.Sprintf("thread-%d", i))
			s.ProvisionalTreeHint(ctx, root)
			s.ConfirmTreeHint(ctx, root)
			s.LookupTreeHint(ctx, root.Scope)
			s.LookupThread(ctx, root.Scope, root.Thread)
			s.AvoidThread(ctx, CollabThreadAvoidance{Scope: root.Scope, Thread: root.Thread, AccountID: 1, ExpiresAt: time.Now().Add(time.Minute)})
			s.ThreadAvoidances(ctx, root.Scope, root.Thread)
		})
	}
	wg.Wait()
	require.LessOrEqual(t, len(s.entries), 16)
	require.Equal(t, len(s.entries), s.lru.Len())
	require.False(t, s.ProvisionalTreeHint(ctx, CollabTreeHint{}))
	require.False(t, s.AcquireDeliveryLease(ctx, CollabDeliveryLease{}))
}
