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

func TestCollabRound2EvidenceCannotCrowdOutDispositions(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCollabRecordsStore()
	key := collabTestKey()
	for i := 0; i < CollabRecordMaxTreeEntries; i++ {
		require.True(t, s.PlaintextDelivered(ctx, CollabPlaintextRecord{Scope: key.Scope, Call: CollabDigest(fmt.Sprint(i)), Arguments: "args", ExpiresAt: time.Now().Add(time.Hour)}))
	}
	for i := 0; i < 10; i++ {
		count, ok := s.ReserveTreeLocalEnding(ctx, key.Scope, CollabDigest(fmt.Sprint(i)))
		if i < 3 {
			require.True(t, ok)
			require.Equal(t, i+1, count)
		} else {
			require.False(t, ok)
			require.Equal(t, CollabTreeLocalEndingLimit, count)
		}
	}
	require.True(t, s.Terminated(ctx, CollabTerminatedRecord{Key: key, Event: "event", ExpiresAt: time.Now().Add(time.Hour)}))
	require.True(t, s.Replaced(ctx, CollabReplacedRecord{Key: key, Account: "account", Placeholder: CollabPlaceholderMessage, ExpiresAt: time.Now().Add(time.Hour)}))
	count, ok := s.IncrementTreeLocalEnding(ctx, key.Scope, "after-delivery")
	require.False(t, ok)
	require.Equal(t, CollabTreeLocalEndingLimit, count)
	require.Equal(t, 3, s.LookupBatch(ctx, CollabRecordBatch{Scope: key.Scope, Dispositions: true}).LocalEndingCount())
}
func TestCollabRound2TenantCapacityRecovers(t *testing.T) {
	s := NewMemoryCollabRecordsStore()
	ctx := context.Background()
	key := collabTestKey()
	for i := 0; i < CollabRecordMaxTrees+8; i++ {
		scope := key.Scope
		scope.Tree = CollabDigest(fmt.Sprint(i))
		s.PlaintextDelivered(ctx, CollabPlaintextRecord{Scope: scope, Call: "call", Arguments: "args", ExpiresAt: time.Now().Add(time.Hour)})
	}
	key.Scope.Tenant = "other"
	require.True(t, s.Terminated(ctx, CollabTerminatedRecord{Key: key, Event: "event", ExpiresAt: time.Now().Add(time.Hour)}))
}
func TestCollabRound2ConfirmWaitsForRegistryMutation(t *testing.T) {
	s := NewMemoryCollabRecordsStore()
	ctx := context.Background()
	key := collabTestKey()
	token, ok := s.PendingIssued(ctx, CollabIssuedRecord{Scope: key.Scope, Call: "call", Digest: key.Digest, Account: "account", ExpiresAt: time.Now().Add(time.Hour)})
	require.True(t, ok)
	s.mu.Lock()
	done := make(chan bool, 1)
	go func() { done <- s.ConfirmIssued(ctx, token) }()
	select {
	case ok := <-done:
		s.mu.Unlock()
		require.True(t, ok, "transient contention must not discard confirmation")
		return
	case <-time.After(10 * time.Millisecond):
	}
	s.mu.Unlock()
	require.True(t, <-done)
}
func TestCollabRound2CounterContentionCannotLookLikeZero(t *testing.T) {
	s := NewMemoryCollabRecordsStore()
	ctx := context.Background()
	key := collabTestKey()
	for i := 0; i < 3; i++ {
		_, ok := s.ReserveTreeLocalEnding(ctx, key.Scope, CollabDigest(fmt.Sprint(i)))
		require.True(t, ok)
	}
	tree := s.tree(key.Scope, false, time.Now())
	tree.mu.Lock()
	result := s.LookupBatch(ctx, CollabRecordBatch{Scope: key.Scope, Dispositions: true})
	tree.mu.Unlock()
	require.Equal(t, CollabTreeLocalEndingLimit, result.TreeLocalEndings)
}
func TestCollabRound2FreshReadFailureDropsIssuance(t *testing.T) {
	ctx := context.Background()
	local := NewMemoryCollabRecordsStore()
	key := collabTestKey()
	token, ok := local.PendingIssued(ctx, CollabIssuedRecord{Scope: key.Scope, Call: "call", Digest: key.Digest, Account: "account", ExpiresAt: time.Now().Add(time.Hour)})
	require.True(t, ok)
	require.True(t, local.ConfirmIssued(ctx, token))
	q := CollabRecordQuery{Key: key}
	batch := CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{q}, Records: true}
	remote := local.LookupBatch(ctx, batch)
	tree := local.tree(key.Scope, false, time.Now())
	shared := &collabSharedTestStore{lookup: func(context.Context, CollabRecordBatch) (CollabRecordBatchResult, error) {
		local.VoidIssued(ctx, token)
		tree.mu.Lock()
		return remote, nil
	}}
	safe := NewSafeCollabRecordsStore(local, shared, 8)
	defer safe.Close()
	read, cancel := WithCollabRecordReadBudget(ctx)
	defer cancel()
	result := safe.LookupBatch(read, batch)
	tree.mu.Unlock()
	for _, r := range result.Records[q].Issued {
		require.NotEqual(t, CollabIssuedStatusConfirmed, r.Status)
	}
}
func TestCollabRound2CopiesKeepPrimary(t *testing.T) {
	s := NewMemoryCollabRecordsStore()
	key := collabTestKey()
	copies := make([]CollabDigest, 17)
	for i := range copies {
		copies[i] = CollabDigest(fmt.Sprint(i))
	}
	require.True(t, s.Terminated(context.Background(), CollabTerminatedRecord{Key: key, Event: "event", Copies: copies, ExpiresAt: time.Now().Add(time.Hour)}))
	q := CollabRecordQuery{Key: key}
	require.Len(t, s.LookupBatch(context.Background(), CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{q}, Dispositions: true}).Dispositions[q].Terminated, 1)
}

func TestCollabRound2CompletenessPriorityAndDuplicates(t *testing.T) {
	local := NewMemoryCollabRecordsStore()
	safe := NewSafeCollabRecordsStore(local, nil, 8)
	defer safe.Close()
	key := collabTestKey()
	ctx, cancel := WithCollabRecordReadBudget(context.Background())
	defer cancel()
	require.True(t, safe.Terminated(ctx, CollabTerminatedRecord{Key: key, Event: "event", ExpiresAt: time.Now().Add(time.Hour)}))
	q := CollabRecordQuery{Key: key}
	duplicates := make([]CollabRecordQuery, 300)
	for i := range duplicates {
		duplicates[i] = q
	}
	batch := CollabRecordBatch{Scope: key.Scope, Queries: duplicates, Records: true, Dispositions: true}
	result := safe.LookupBatch(ctx, batch)
	require.True(t, result.DispositionsComplete)
	require.True(t, result.TreeLocalEndingsKnown)
	require.Len(t, result.Dispositions[q].Terminated, 1)
	result = safe.LookupBatch(ctx, batch)
	require.True(t, result.DispositionsComplete)
	require.Len(t, result.Dispositions[q].Terminated, 1)
	for i := range duplicates {
		duplicates[i].Call = CollabDigest(fmt.Sprint(i))
	}
	batch.Queries = duplicates
	batch.DispositionQueries = []CollabRecordQuery{q}
	result = safe.LookupBatch(ctx, batch)
	require.False(t, result.RecordsComplete)
	require.True(t, result.DispositionsComplete)
	require.Len(t, result.Dispositions[q].Terminated, 1)
	cancelled, stop := context.WithCancel(ctx)
	stop()
	result = safe.LookupBatch(cancelled, batch)
	require.False(t, result.TreeLocalEndingsKnown)
	require.False(t, result.DispositionsComplete)
	require.Equal(t, 3, result.LocalEndingCount())
}
func TestCollabRound2ReservationFailuresAndReceiptGate(t *testing.T) {
	key := collabTestKey()
	ctx := context.Background()
	for _, local := range []CollabRecordsStore{nil, collabPanicLocal{}} {
		safe := NewSafeCollabRecordsStore(local, nil, 8)
		count, ok := safe.ReserveTreeLocalEnding(ctx, key.Scope, "event")
		require.False(t, ok)
		require.Equal(t, 3, count)
		safe.Close()
	}
	safe := NewSafeCollabRecordsStore(NewMemoryCollabRecordsStore(), &collabSharedTestStore{}, 8)
	defer safe.Close()
	count, ok := safe.ReserveTreeLocalEnding(ctx, key.Scope, "event")
	require.False(t, ok)
	require.Equal(t, 3, count)
	record := CollabTerminatedRecord{Key: key, Event: "event", ExpiresAt: time.Now().Add(time.Hour)}
	require.False(t, CollabTerminationEffective(record, key, nil, time.Now()))
	require.True(t, CollabTerminationEffective(record, key, []CollabDigest{"event"}, time.Now()))
	changed := key
	changed.Delivery = "new"
	require.False(t, CollabTerminationEffective(record, changed, []CollabDigest{"event"}, time.Now()))
}

func TestCollabRound2DispositionPriorityAfterEvidenceExhaustion(t *testing.T) {
	s := NewMemoryCollabRecordsStore()
	ctx := context.Background()
	key := collabTestKey()
	require.True(t, s.Terminated(ctx, CollabTerminatedRecord{Key: key, Event: "event", ExpiresAt: time.Now().Add(time.Hour)}))
	q := CollabRecordQuery{Key: key}
	evidence := make([]CollabRecordQuery, 300)
	for i := range evidence {
		evidence[i] = CollabRecordQuery{Key: key, Call: CollabDigest(fmt.Sprint(i))}
	}
	safe := NewSafeCollabRecordsStore(s, nil, 4)
	defer safe.Close()
	budget, cancel := WithCollabRecordReadBudget(ctx)
	defer cancel()
	result := safe.LookupBatch(budget, CollabRecordBatch{Scope: key.Scope, Queries: evidence, DispositionQueries: []CollabRecordQuery{q}, Records: true, Dispositions: true})
	require.False(t, result.RecordsComplete)
	require.True(t, result.DispositionsComplete)
	require.True(t, result.TreeLocalEndingsKnown)
	require.Len(t, result.Dispositions[q].Terminated, 1)
	duplicates := make([]CollabRecordQuery, 300)
	for i := range duplicates {
		duplicates[i] = q
	}
	result = safe.LookupBatch(budget, CollabRecordBatch{Scope: key.Scope, Queries: duplicates, Dispositions: true})
	require.True(t, result.DispositionsComplete)
	require.Len(t, result.Dispositions[q].Terminated, 1)
	cancel()
	result = safe.LookupBatch(budget, CollabRecordBatch{Scope: key.Scope, Dispositions: true})
	require.False(t, result.TreeLocalEndingsKnown)
	require.False(t, result.DispositionsComplete)
	require.Equal(t, 3, result.LocalEndingCount())
}
func TestCollabRound2ReservationConcurrentAndFailure(t *testing.T) {
	s := NewSafeCollabRecordsStore(NewMemoryCollabRecordsStore(), nil, 8)
	defer s.Close()
	key := collabTestKey()
	var wg sync.WaitGroup
	var accepted atomic.Int64
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			_, ok := s.ReserveTreeLocalEnding(context.Background(), key.Scope, CollabDigest(fmt.Sprint(i)))
			if ok {
				accepted.Add(1)
			}
		})
	}
	wg.Wait()
	require.Equal(t, int64(3), accepted.Load())
	absent := NewSafeCollabRecordsStore(nil, nil, 1)
	defer absent.Close()
	count, ok := absent.ReserveTreeLocalEnding(context.Background(), key.Scope, "event")
	require.False(t, ok)
	require.Equal(t, 3, count)
	failed := NewSafeCollabRecordsStore(NewMemoryCollabRecordsStore(), &collabSharedTestStore{}, 1)
	defer failed.Close()
	count, ok = failed.ReserveTreeLocalEnding(context.Background(), key.Scope, "event")
	require.False(t, ok)
	require.Equal(t, 3, count)
}
func TestCollabRound2ReceiptRequiredForTermination(t *testing.T) {
	key := collabTestKey()
	record := CollabTerminatedRecord{Key: key, Event: "event", Copies: []CollabDigest{"copy"}, ExpiresAt: time.Now().Add(time.Hour)}
	require.False(t, CollabTerminationEffective(record, key, nil, time.Now()))
	require.False(t, CollabTerminationEffective(record, key, []CollabDigest{"other"}, time.Now()))
	require.True(t, CollabTerminationEffective(record, key, []CollabDigest{"event"}, time.Now()))
	copyKey := key
	copyKey.Item = "copy"
	require.True(t, CollabTerminationEffective(record, copyKey, []CollabDigest{"event"}, time.Now()))
	copyKey.Delivery = "new"
	require.False(t, CollabTerminationEffective(record, copyKey, []CollabDigest{"event"}, time.Now()))
}

type collabRound2ReservationStore struct {
	collabSharedTestStore
	mode string
}

func (s *collabRound2ReservationStore) ReserveTreeLocalEnding(ctx context.Context, _ CollabRecordScope, _ CollabDigest) (int, bool) {
	if s.mode == "panic" {
		panic("synthetic")
	}
	if s.mode == "slow" {
		<-ctx.Done()
	}
	return CollabTreeLocalEndingLimit, false
}
func TestCollabRound2SharedReservationFailures(t *testing.T) {
	for _, mode := range []string{"panic", "slow", "failed"} {
		t.Run(mode, func(t *testing.T) {
			store := NewSafeCollabRecordsStore(NewMemoryCollabRecordsStore(), &collabRound2ReservationStore{mode: mode}, 1)
			defer store.Close()
			started := time.Now()
			count, ok := store.ReserveTreeLocalEnding(context.Background(), collabTestKey().Scope, "event")
			require.False(t, ok)
			require.Equal(t, CollabTreeLocalEndingLimit, count)
			require.Less(t, time.Since(started), time.Second)
		})
	}
}
func TestCollabRound2LocalTenantDispositionsCannotBeEvicted(t *testing.T) {
	s := NewMemoryCollabRecordsStore()
	ctx := context.Background()
	key := collabTestKey()
	for i := 0; i < CollabRecordMaxTenantTrees; i++ {
		k := key
		k.Scope.Tree = CollabDigest(fmt.Sprint(i))
		require.True(t, s.Terminated(ctx, CollabTerminatedRecord{Key: k, Event: "event", ExpiresAt: time.Now().Add(time.Hour)}))
	}
	result := s.LookupBatch(ctx, CollabRecordBatch{Scope: key.Scope, Dispositions: true})
	require.False(t, result.TreeLocalEndingsKnown)
	require.Equal(t, 3, result.LocalEndingCount())
	count, ok := s.ReserveTreeLocalEnding(ctx, key.Scope, "event")
	require.False(t, ok)
	require.Equal(t, 3, count)
	other := key.Scope
	other.Tenant = "other"
	count, ok = s.ReserveTreeLocalEnding(ctx, other, "event")
	require.True(t, ok)
	require.Equal(t, 1, count)
	existing := key
	existing.Scope.Tree = "0"
	q := CollabRecordQuery{Key: existing}
	require.Len(t, s.LookupBatch(ctx, CollabRecordBatch{Scope: existing.Scope, Queries: []CollabRecordQuery{q}, Dispositions: true}).Dispositions[q].Terminated, 1)
}

func TestCollabRound2TimeoutDropsInitialIssuance(t *testing.T) {
	ctx := context.Background()
	local := NewMemoryCollabRecordsStore()
	key := collabTestKey()
	token, ok := local.PendingIssued(ctx, CollabIssuedRecord{Scope: key.Scope, Call: "call", Digest: key.Digest, Account: "account", ExpiresAt: time.Now().Add(time.Hour)})
	require.True(t, ok)
	require.True(t, local.ConfirmIssued(ctx, token))
	shared := &collabSharedTestStore{lookup: func(ctx context.Context, _ CollabRecordBatch) (CollabRecordBatchResult, error) {
		local.VoidIssued(context.Background(), token)
		<-ctx.Done()
		return CollabRecordBatchResult{}, ctx.Err()
	}}
	safe := NewSafeCollabRecordsStore(local, shared, 8)
	defer safe.Close()
	parent, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	read, cancel := WithCollabRecordReadBudget(parent)
	defer cancel()
	q := CollabRecordQuery{Key: key}
	result := safe.LookupBatch(read, CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{q}, Records: true})
	require.False(t, result.RecordsComplete)
	for _, issue := range result.Records[q].Issued {
		require.NotEqual(t, CollabIssuedStatusConfirmed, issue.Status)
	}
}
