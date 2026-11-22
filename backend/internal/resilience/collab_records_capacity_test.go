package resilience

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func capacityDigest(i int) CollabDigest { return CollabDigest(fmt.Sprintf("%064x", i+1)) }
func capacityKey(i int) CollabDeliveryKey {
	return CollabDeliveryKey{Scope: CollabRecordScope{Tenant: capacityDigest(0), Group: capacityDigest(1), Tree: capacityDigest(i + 10)}, Thread: capacityDigest(2), Item: capacityDigest(3), Delivery: capacityDigest(4), Digest: capacityDigest(5)}
}

// Reservation must cover the actual notice before any bytes can be delivered.
type capacityReserver interface {
	ReserveLocalEnding(context.Context, []CollabTerminatedRecord) (int, bool)
}

func TestCollabCapacityConcreteReservation(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCollabRecordsStore()
	reserver, ok := any(s).(capacityReserver)
	require.True(t, ok, "concrete disposition reservation is required")
	key := capacityKey(0)
	expiry := time.Now().Add(time.Hour)
	records := []CollabTerminatedRecord{{Key: key, Event: capacityDigest(100), ExpiresAt: expiry}}
	second := records[0]
	second.Key.Item = capacityDigest(101)
	records = append(records, second)
	n, ok := reserver.ReserveLocalEnding(ctx, records)
	require.True(t, ok)
	require.Equal(t, 1, n)
	q := CollabRecordQuery{Key: key}
	require.Empty(t, s.LookupBatch(ctx, CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{q}, Dispositions: true}).Dispositions[q].Terminated)
	for i := 0; i < 64; i++ {
		k := key
		k.Item = capacityDigest(200 + i)
		s.TerminalRejected(ctx, CollabTerminalRejectionRecord{Key: k, Rejection: capacityDigest(6), Event: capacityDigest(300 + i), ExpiresAt: expiry})
	}
	for _, r := range records {
		require.True(t, s.Terminated(ctx, r), "reserved publication cannot lose capacity")
	}
	require.Len(t, s.LookupBatch(ctx, CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{q}, Dispositions: true}).Dispositions[q].Terminated, 1)
	more := records[0]
	more.Event = capacityDigest(400)
	n, ok = reserver.ReserveLocalEnding(ctx, []CollabTerminatedRecord{more})
	require.False(t, ok)
	require.Equal(t, 3, n)
}
func TestCollabCapacityThreeFullDigestReservations(t *testing.T) {
	s := NewMemoryCollabRecordsStore()
	ctx := context.Background()
	k := capacityKey(0)
	for i := 0; i < 4; i++ {
		n, ok := s.ReserveTreeLocalEnding(ctx, k.Scope, capacityDigest(100+i))
		require.Equal(t, i < 3, ok)
		require.Equal(t, min(i+1, 3), n)
	}
}
func TestCollabCapacityEventSpecificTermination(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCollabRecordsStore()
	k := capacityKey(0)
	for i := 0; i < 2; i++ {
		r := CollabTerminatedRecord{Key: k, Event: capacityDigest(100 + i), ExpiresAt: time.Now().Add(time.Hour)}
		require.True(t, s.Terminated(ctx, r))
		require.True(t, s.Terminated(ctx, r))
	}
	q := CollabRecordQuery{Key: k}
	got := s.LookupBatch(ctx, CollabRecordBatch{Scope: k.Scope, Queries: []CollabRecordQuery{q}, Dispositions: true})
	require.Len(t, got.Dispositions[q].Terminated, 2)
	for i := 0; i < 2; i++ {
		effective := false
		for _, r := range got.Dispositions[q].Terminated {
			effective = effective || CollabTerminationEffective(r, k, []CollabDigest{capacityDigest(100 + i)}, time.Now())
		}
		require.True(t, effective)
	}
}
func TestCollabCapacityVoidSurvivesEvidenceEviction(t *testing.T) {
	ctx := context.Background()
	local := NewMemoryCollabRecordsStore()
	k := capacityKey(0)
	token, ok := local.PendingIssued(ctx, CollabIssuedRecord{Scope: k.Scope, Call: capacityDigest(10), Digest: k.Digest, Account: capacityDigest(7), ExpiresAt: time.Now().Add(time.Hour)})
	require.True(t, ok)
	require.True(t, local.ConfirmIssued(ctx, token))
	q := CollabRecordQuery{Key: k}
	b := CollabRecordBatch{Scope: k.Scope, Queries: []CollabRecordQuery{q}, Records: true}
	old := local.LookupBatch(ctx, b)
	shared := &collabSharedTestStore{lookup: func(context.Context, CollabRecordBatch) (CollabRecordBatchResult, error) {
		local.VoidIssued(ctx, token)
		for i := 0; i < 64; i++ {
			local.PlaintextDelivered(ctx, CollabPlaintextRecord{Scope: k.Scope, Call: capacityDigest(100 + i), Arguments: capacityDigest(6), ExpiresAt: time.Now().Add(time.Hour)})
		}
		return old, nil
	}}
	safe := NewSafeCollabRecordsStore(local, shared, 8)
	defer safe.Close()
	budget, cancel := WithCollabRecordReadBudget(ctx)
	defer cancel()
	for _, r := range safe.LookupBatch(budget, b).Records[q].Issued {
		require.NotEqual(t, CollabIssuedStatusConfirmed, r.Status)
	}
}
func TestCollabCapacityCopiesReportUnknown(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCollabRecordsStore()
	k := capacityKey(0)
	copies := make([]CollabDigest, 17)
	for i := range copies {
		copies[i] = capacityDigest(100 + i)
	}
	require.True(t, s.Terminated(ctx, CollabTerminatedRecord{Key: k, Copies: copies, Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Hour)}))
	k.Item = copies[16]
	q := CollabRecordQuery{Key: k}
	got := s.LookupBatch(ctx, CollabRecordBatch{Scope: k.Scope, Queries: []CollabRecordQuery{q}, Dispositions: true})
	require.False(t, got.DispositionsComplete, "truncated copy must not be reported absent")
}
func TestCollabCapacityRejectionMutationResult(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCollabRecordsStore()
	k := capacityKey(0)
	r := CollabTerminalRejectionRecord{Key: k, Event: capacityDigest(30), Rejection: capacityDigest(40), ExpiresAt: time.Now().Add(time.Hour)}
	_, ok := s.TerminalRejected(ctx, r)
	require.True(t, ok)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < 50; w++ {
		wg.Go(func() {
			<-start
			for i := 0; i < 200; i++ {
				s.PlaintextDelivered(ctx, CollabPlaintextRecord{Scope: capacityKey(w + 1).Scope, Call: capacityDigest(i + 100), Arguments: capacityDigest(6), ExpiresAt: time.Now().Add(time.Hour)})
			}
		})
	}
	close(start)
	failed := 0
	for i := 0; i < 5000; i++ {
		n, ok := s.TerminalRejected(ctx, r)
		if !ok || n != 1 {
			failed++
		}
	}
	wg.Wait()
	require.Zero(t, failed)
}
func TestCollabCapacitySharedPublicationFailure(t *testing.T) {
	shared := &collabSharedTestStore{apply: func(ctx context.Context, _ CollabRecordMutation) error { <-ctx.Done(); return ctx.Err() }}
	safe := NewSafeCollabRecordsStore(NewMemoryCollabRecordsStore(), shared, 1)
	defer safe.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.False(t, safe.Terminated(ctx, CollabTerminatedRecord{Key: capacityKey(0), Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Hour)}), "shared refusal must not be acknowledged")
}
func TestCollabCapacitySharedReadsAtFiftyStreams(t *testing.T) {
	shared := &collabSharedTestStore{lookup: func(ctx context.Context, b CollabRecordBatch) (CollabRecordBatchResult, error) {
		select {
		case <-time.After(10 * time.Millisecond):
			return CollabRecordBatchResult{RecordsComplete: true, DispositionsComplete: true, TreeLocalEndingsKnown: true}, nil
		case <-ctx.Done():
			return CollabRecordBatchResult{}, ctx.Err()
		}
	}}
	safe := NewSafeCollabRecordsStore(NewMemoryCollabRecordsStore(), shared, 8)
	defer safe.Close()
	var wg sync.WaitGroup
	var complete atomic.Int64
	start := make(chan struct{})
	for i := 0; i < 50; i++ {
		wg.Go(func() {
			<-start
			ctx, cancel := WithCollabRecordReadBudget(context.Background())
			defer cancel()
			if safe.LookupBatch(ctx, CollabRecordBatch{Scope: capacityKey(i).Scope, Records: true, Dispositions: true}).DispositionsComplete {
				complete.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()
	require.Equal(t, int64(50), complete.Load())
}
func TestCollabCapacityLegacyIncrementAfterDelivery(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCollabRecordsStore()
	k := capacityKey(0)
	_, ok := s.ReserveTreeLocalEnding(ctx, k.Scope, capacityDigest(90))
	require.True(t, ok)
	require.True(t, s.Terminated(ctx, CollabTerminatedRecord{Key: k, Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Hour)}))
	_, ok = s.IncrementTreeLocalEnding(ctx, k.Scope, capacityDigest(91))
	require.False(t, ok)
	require.Equal(t, 1, s.LookupBatch(ctx, CollabRecordBatch{Scope: k.Scope, Dispositions: true}).LocalEndingCount())
}

func TestCollabCapacitySkewedRetention(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCollabRecordsStore()
	counts := [3]int{}
	for i := 0; i < 3500; i++ {
		tenant := 0
		if i%100 >= 90 {
			tenant = 1
		}
		if i%100 == 99 {
			tenant = 2
		}
		k := capacityKey(i)
		k.Scope.Tenant = capacityDigest(tenant)
		expiry := time.Now().Add(CollabRecordTTL)
		for call := 0; call < 32; call++ {
			require.True(t, s.PlaintextDelivered(ctx, CollabPlaintextRecord{Scope: k.Scope, Call: capacityDigest(10000 + call), Arguments: capacityDigest(8), ExpiresAt: expiry}))
		}
		for d := 0; d < 4; d++ {
			x := k
			x.Item = capacityDigest(700 + d)
			require.True(t, s.Consumed(ctx, CollabConsumedRecord{Key: x, Account: capacityDigest(7), ExpiresAt: expiry}))
		}
		if i%10 == 0 {
			_, ok := s.TerminalRejected(ctx, CollabTerminalRejectionRecord{Key: k, Event: capacityDigest(90), Rejection: capacityDigest(8), ExpiresAt: expiry})
			require.True(t, ok)
		}
		counts[tenant]++
		if i == 1499 {
			require.Len(t, s.trees, 1500)
			t.Logf("three-day admitted=%v trees=%d", counts, len(s.trees))
		}
	}
	require.Len(t, s.trees, 3500)
	retained := [3]int{}
	for scope := range s.trees {
		for j := range retained {
			if scope.Tenant == capacityDigest(j) {
				retained[j]++
			}
		}
	}
	require.Equal(t, counts, retained)
	for j := 0; j < 3; j++ {
		k := capacityKey(10000 + j)
		k.Scope.Tenant = capacityDigest(j)
		n, ok := s.ReserveLocalEnding(ctx, []CollabTerminatedRecord{{Key: k, Event: capacityDigest(99), ExpiresAt: time.Now().Add(CollabRecordTTL)}})
		require.True(t, ok)
		require.Equal(t, 1, n)
	}
	t.Logf("seven-day retained=%v new reservations=3/3", retained)
}

func TestCollabCapacityDuplicatePublicationAndTenantFairness(t *testing.T) {
	ctx := context.Background()
	entered := make(chan struct{}, 128)
	release := make(chan struct{})
	var calls atomic.Int64
	shared := &collabSharedTestStore{apply: func(ctx context.Context, m CollabRecordMutation) error {
		calls.Add(1)
		entered <- struct{}{}
		if m.Terminated.Key.Scope.Tenant == capacityDigest(0) {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}}
	safe := NewSafeCollabRecordsStore(NewMemoryCollabRecordsStore(), shared, 1)
	defer safe.Close()
	r := CollabTerminatedRecord{Key: capacityKey(0), Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Hour)}
	var wg sync.WaitGroup
	var accepted atomic.Int64
	start := make(chan struct{})
	for i := 0; i < 256; i++ {
		wg.Go(func() {
			<-start
			if safe.Terminated(ctx, r) {
				accepted.Add(1)
			}
		})
	}
	close(start)
	<-entered
	// A different tenant has independent admission while the duplicate is stalled.
	other := r
	other.Key.Scope.Tenant = capacityDigest(99)
	require.True(t, safe.Terminated(ctx, other))
	require.Equal(t, int64(0), accepted.Load())
	// Wait for all duplicate callers to join, without holding a store lock.
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int64(2), calls.Load())
	close(release)
	wg.Wait()
	require.Equal(t, int64(256), accepted.Load())
}

func TestCollabCapacityMaximumCounterBytes(t *testing.T) {
	s := NewMemoryCollabRecordsStore()
	ctx := context.Background()
	k := capacityKey(0)
	for i := 0; i < 3; i++ {
		n, ok := s.ReserveTreeLocalEnding(ctx, k.Scope, capacityDigest(100+i))
		require.True(t, ok)
		require.Equal(t, i+1, n)
	}
	tree := s.tree(k.Scope, false, time.Now())
	rows, bytes := tree.partitionSize(2)
	require.Equal(t, 3, rows)
	require.LessOrEqual(t, bytes, CollabRecordCounterBytes)
	require.Equal(t, CollabRecordMaxTreeBytes, CollabRecordEvidenceBytes+CollabRecordDispositionBytes+CollabRecordCounterBytes)
	t.Logf("three full-width counters charge=%d partition=%d", bytes, CollabRecordCounterBytes)
}
