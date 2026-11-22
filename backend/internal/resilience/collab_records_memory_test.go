package resilience

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func collabTestKey() CollabDeliveryKey {
	return CollabDeliveryKey{Scope: CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}, Thread: "thread", Item: "item", Delivery: "delivery", Digest: "cipher"}
}
func TestCollabMemoryRecordsLifecycleAndIsolation(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCollabRecordsStore()
	key := collabTestKey()
	expiry := time.Now().Add(time.Hour)
	q := CollabRecordQuery{Key: key, Call: "call", Arguments: "arguments"}
	b := CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{q}, Records: true, Dispositions: true}
	r := CollabIssuedRecord{Scope: key.Scope, Call: "call", Digest: key.Digest, Account: "account", Site: "site", Generation: 1, ExpiresAt: expiry}
	token, ok := s.PendingIssued(ctx, r)
	require.True(t, ok)
	require.Equal(t, CollabIssuedStatusPending, s.LookupBatch(ctx, b).Records[q].Issued[0].Status)
	require.True(t, s.ConfirmIssued(ctx, token))
	require.True(t, s.ConfirmIssued(ctx, token))
	require.Equal(t, CollabIssuedStatusConfirmed, s.LookupBatch(ctx, b).Records[q].Issued[0].Status)
	require.True(t, s.PlaintextDelivered(ctx, CollabPlaintextRecord{Scope: key.Scope, Call: "call", Arguments: "arguments", ExpiresAt: expiry}))
	require.True(t, s.Consumed(ctx, CollabConsumedRecord{Key: key, Account: "account", ExpiresAt: expiry}))
	require.True(t, s.Replaced(ctx, CollabReplacedRecord{Key: key, Account: "account", Placeholder: CollabPlaceholderMessage, ExpiresAt: expiry}))
	copies := []CollabDigest{"copy"}
	require.True(t, s.Terminated(ctx, CollabTerminatedRecord{Key: key, Copies: copies, Event: "event", Parent: "parent", ExpiresAt: expiry}))
	copies[0] = "mutated"
	result := s.LookupBatch(ctx, b)
	require.Len(t, result.Records[q].Consumed, 1)
	require.Len(t, result.Records[q].Plaintext, 1)
	require.Equal(t, CollabDigest("copy"), result.Dispositions[q].Terminated[0].Copies[0])
	result.Dispositions[q].Terminated[0].Copies[0] = "changed"
	require.Equal(t, CollabDigest("copy"), s.LookupBatch(ctx, b).Dispositions[q].Terminated[0].Copies[0])
	b.Records = false
	require.Empty(t, s.LookupBatch(ctx, b).Records)
	require.Len(t, s.LookupBatch(ctx, b).Dispositions[q].Terminated, 1)
	count, ok := s.IncrementTreeLocalEnding(ctx, key.Scope, "event")
	require.True(t, ok)
	require.Equal(t, 1, count)
	count, ok = s.IncrementTreeLocalEnding(ctx, key.Scope, "event")
	require.True(t, ok)
	require.Equal(t, 1, count)
	other := b
	other.Scope.Tenant = "other"
	isolated := s.LookupBatch(ctx, other)
	require.Empty(t, isolated.Records)
	require.Empty(t, isolated.Dispositions)
	require.Zero(t, isolated.LocalEndingCount())
	newDelivery := q
	newDelivery.Key.Delivery = "new"
	newDelivery.Key.Item = "new"
	b.Queries = []CollabRecordQuery{newDelivery}
	require.Empty(t, s.LookupBatch(ctx, b).Dispositions[newDelivery])
	b.Queries = []CollabRecordQuery{q}
	b.Records = true
	s.VoidIssued(ctx, token)
	require.False(t, s.ConfirmIssued(ctx, token))
	require.Equal(t, CollabIssuedStatusVoid, s.LookupBatch(ctx, b).Records[q].Issued[0].Status)
	unknown := token
	unknown.Token = "unknown"
	require.False(t, s.ConfirmIssued(ctx, unknown))
	// One concurrent failed delivery cannot invalidate a separate successful one.
	another, ok := s.PendingIssued(ctx, r)
	require.True(t, ok)
	require.True(t, s.ConfirmIssued(ctx, another))
	require.Equal(t, CollabIssuedStatusConfirmed, s.LookupBatch(ctx, b).Records[q].Issued[0].Status)
}
func TestCollabMemoryExpiryCapacityAndConcurrency(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCollabRecordsStore()
	key := collabTestKey()
	require.False(t, s.Consumed(ctx, CollabConsumedRecord{Key: key, Account: "a", ExpiresAt: time.Now().Add(-time.Second)}))
	for i := 0; i < CollabRecordMaxTreeEntries; i++ {
		r := CollabPlaintextRecord{Scope: key.Scope, Call: CollabDigest(fmt.Sprint(i)), Arguments: "args", ExpiresAt: time.Now().Add(time.Hour)}
		require.True(t, s.PlaintextDelivered(ctx, r))
	}
	require.True(t, s.PlaintextDelivered(ctx, CollabPlaintextRecord{Scope: key.Scope, Call: "overflow", Arguments: "args", ExpiresAt: time.Now().Add(time.Hour)}))
	oldQuery := CollabRecordQuery{Key: key, Call: "0"}
	require.Empty(t, s.LookupBatch(ctx, CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{oldQuery}, Records: true}).Records[oldQuery].Plaintext, "oldest evidence is evicted")
	fresh := NewMemoryCollabRecordsStore()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			token, ok := fresh.PendingIssued(ctx, CollabIssuedRecord{Scope: key.Scope, Call: "call", Digest: key.Digest, Account: "account", ExpiresAt: time.Now().Add(time.Hour)})
			if ok {
				fresh.ConfirmIssued(ctx, token)
			}
			fresh.LookupBatch(ctx, CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{{Key: key}}, Records: true})
		})
	}
	wg.Wait()
}
func TestCollabSafeUnionAndVoidDuringRemoteRead(t *testing.T) {
	ctx := context.Background()
	key := collabTestKey()
	q := CollabRecordQuery{Key: key}
	expiry := time.Now().Add(time.Hour)
	local := NewMemoryCollabRecordsStore()
	r := CollabIssuedRecord{Scope: key.Scope, Call: "call", Digest: key.Digest, Account: "account", Generation: 1, ExpiresAt: expiry}
	token, ok := local.PendingIssued(ctx, r)
	require.True(t, ok)
	require.True(t, local.ConfirmIssued(ctx, token))
	require.True(t, local.Consumed(ctx, CollabConsumedRecord{Key: key, Account: "reader", ExpiresAt: expiry}))
	remoteIssue := r
	remoteIssue.Status = CollabIssuedStatusConfirmed
	remoteOther := remoteIssue
	remoteOther.Call = "other-call"
	otherToken := token
	otherToken.Token = "other-token"
	otherToken.Call = remoteOther.Call
	shared := &collabSharedTestStore{lookup: func(context.Context, CollabRecordBatch) (CollabRecordBatchResult, error) {
		local.VoidIssued(ctx, token)
		return CollabRecordBatchResult{Records: map[CollabRecordQuery]CollabRecordLookup{q: {Issued: []CollabIssuedRecord{remoteIssue, remoteOther}, IssuedTokens: []CollabIssuedEntry{{Token: token, Record: remoteIssue}, {Token: otherToken, Record: remoteOther}}, Plaintext: []CollabPlaintextRecord{{Scope: key.Scope, Call: "plain", Arguments: "args", ExpiresAt: expiry}}}}}, nil
	}}
	store := NewSafeCollabRecordsStore(local, shared, 4)
	defer store.Close()
	read, cancel := WithCollabRecordReadBudget(ctx)
	defer cancel()
	result := store.LookupBatch(read, CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{q}, Records: true}).Records[q]
	require.Len(t, result.Issued, 2)
	require.Len(t, result.Consumed, 1)
	require.Len(t, result.Plaintext, 1)
	require.Equal(t, CollabIssuedStatusVoid, result.Issued[0].Status)
	require.Equal(t, CollabIssuedStatusConfirmed, result.Issued[1].Status)
}

type collabBatchTestStore struct {
	collabSharedTestStore
	batches chan []CollabRecordMutation
	release chan struct{}
}

func (s *collabBatchTestStore) ApplyBatch(ctx context.Context, b []CollabRecordMutation) error {
	s.batches <- b
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestCollabSafeWriterBatchesInOrder(t *testing.T) {
	shared := &collabBatchTestStore{batches: make(chan []CollabRecordMutation, 8), release: make(chan struct{})}
	local := NewMemoryCollabRecordsStore()
	store := NewSafeCollabRecordsStore(local, shared, 128)
	defer store.Close()
	ctx := context.Background()
	key := collabTestKey()
	for i := 0; i < 1; i++ {
		store.PlaintextDelivered(ctx, CollabPlaintextRecord{Scope: key.Scope, Call: "first", Arguments: "args", ExpiresAt: time.Now().Add(time.Hour)})
	}
	<-shared.batches
	for i := 0; i < 40; i++ {
		require.True(t, store.PlaintextDelivered(ctx, CollabPlaintextRecord{Scope: key.Scope, Call: CollabDigest(fmt.Sprint(i)), Arguments: "args", ExpiresAt: time.Now().Add(time.Hour)}))
	}
	close(shared.release)
	select {
	case batch := <-shared.batches:
		require.Len(t, batch, 40)
		for i, m := range batch {
			require.Equal(t, CollabDigest(fmt.Sprint(i)), m.Plaintext.Call)
		}
	case <-time.After(time.Second):
		t.Fatal("batch writer did not drain")
	}
}
func TestCollabSharedReadersIgnoringCancellationStayBounded(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	shared := &collabSharedTestStore{lookup: func(context.Context, CollabRecordBatch) (CollabRecordBatchResult, error) {
		<-release
		return CollabRecordBatchResult{}, errors.New("synthetic")
	}}
	s := NewSafeCollabRecordsStore(nil, shared, 1)
	defer s.Close()
	for i := 0; i < 6; i++ {
		parent, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
		ctx, cancel := WithCollabRecordReadBudget(parent)
		start := time.Now()
		s.LookupBatch(ctx, CollabRecordBatch{Records: true})
		require.Less(t, time.Since(start), 100*time.Millisecond)
		cancel()
		stop()
	}
	require.Equal(t, int64(4), shared.reads.Load())
}
func BenchmarkCollabRejectionIndexedLookup(b *testing.B) {
	counter := NewCollabTerminalRejectionCounter(CollabRecordMaxEntries)
	key := collabTestKey()
	now := time.Now()
	for i := 0; i < CollabRecordMaxEntries; i++ {
		k := key
		k.Item = CollabDigest(fmt.Sprint(i))
		counter.Record(CollabTerminalRejectionRecord{Key: k, Rejection: "rejection", Event: "event", ExpiresAt: now.Add(time.Hour)}, now)
	}
	b.ReportAllocs()
	for b.Loop() {
		counter.Counts(key, now)
	}
}

func TestCollabMemoryExpiresStoredEvidence(t *testing.T) {
	s := NewMemoryCollabRecordsStore()
	key := collabTestKey()
	ctx := context.Background()
	require.True(t, s.Consumed(ctx, CollabConsumedRecord{Key: key, Account: "account", ExpiresAt: time.Now().Add(10 * time.Millisecond)}))
	q := CollabRecordQuery{Key: key}
	batch := CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{q}, Records: true}
	require.Len(t, s.LookupBatch(ctx, batch).Records[q].Consumed, 1)
	time.Sleep(15 * time.Millisecond)
	require.Empty(t, s.LookupBatch(ctx, batch).Records[q].Consumed)
}
