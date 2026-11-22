package resilience

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollabFixReadBudgetAndQueryCap(t *testing.T) {
	s := NewMemoryCollabRecordsStore()
	ctx := context.Background()
	key := collabTestKey()
	require.True(t, s.Consumed(ctx, CollabConsumedRecord{Key: key, Account: "account", ExpiresAt: time.Now().Add(time.Hour)}))
	queries := make([]CollabRecordQuery, 32768)
	for i := range queries {
		queries[i] = CollabRecordQuery{Key: key, Call: CollabDigest(fmt.Sprint(i))}
	}
	batch := CollabRecordBatch{Scope: key.Scope, Queries: queries, Records: true}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.Empty(t, s.LookupBatch(cancelled, batch), "cancelled local reads must do no work")
	result := s.LookupBatch(ctx, batch)
	require.LessOrEqual(t, len(result.Records), 256, "one request cannot monopolize record locks")
}
func TestCollabFixTokenMergePreservesIndependentConfirmation(t *testing.T) {
	ctx := context.Background()
	local := NewMemoryCollabRecordsStore()
	remote := NewMemoryCollabRecordsStore()
	key := collabTestKey()
	issue := CollabIssuedRecord{Scope: key.Scope, Call: "call", Digest: key.Digest, Account: "account", Generation: 1, ExpiresAt: time.Now().Add(time.Hour)}
	localToken, ok := local.PendingIssued(ctx, issue)
	require.True(t, ok)
	local.VoidIssued(ctx, localToken)
	remoteToken, ok := remote.PendingIssued(ctx, issue)
	require.True(t, ok)
	require.True(t, remote.ConfirmIssued(ctx, remoteToken))
	q := CollabRecordQuery{Key: key}
	batch := CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{q}, Records: true}
	shared := &collabSharedTestStore{lookup: func(ctx context.Context, b CollabRecordBatch) (CollabRecordBatchResult, error) {
		return remote.LookupBatch(ctx, b), nil
	}}
	safe := NewSafeCollabRecordsStore(local, shared, 8)
	defer safe.Close()
	read, cancel := WithCollabRecordReadBudget(ctx)
	defer cancel()
	result := safe.LookupBatch(read, batch)
	require.Len(t, result.Records[q].Issued, 1)
	require.Equal(t, CollabIssuedStatusConfirmed, result.Records[q].Issued[0].Status)
	// A delayed pending snapshot of this SAME token must not downgrade confirmation.
	local2 := NewMemoryCollabRecordsStore()
	require.True(t, local2.ApplyMutation(CollabRecordMutation{Kind: CollabRecordMutationPending, Token: remoteToken, Issued: issue}))
	safe2 := NewSafeCollabRecordsStore(local2, shared, 8)
	defer safe2.Close()
	read2, cancel2 := WithCollabRecordReadBudget(ctx)
	defer cancel2()
	require.Equal(t, CollabIssuedStatusConfirmed, safe2.LookupBatch(read2, batch).Records[q].Issued[0].Status)
}
func TestCollabFixRecordsOffDisablesOrdering(t *testing.T) {
	settings := Defaults()
	settings.Collab.Ladder = true
	settings.Collab.RecordRouting = true
	settings.Collab.Records = false
	key := collabTestKey()
	candidates := []int64{1, 2}
	item := CollabRoutingItem{Key: key, Protected: true, UnfinishedTask: true, Records: CollabRecordLookup{Consumed: []CollabConsumedRecord{{Key: key, Account: "reader", ExpiresAt: time.Now().Add(time.Hour)}}}}
	result := OrderCollabFailoverCandidates(&settings, candidates, map[int64]CollabDigest{2: "reader"}, []CollabRoutingItem{item}, time.Now())
	require.Equal(t, candidates, result)
	require.Same(t, &candidates[0], &result[0])
}

func TestCollabFixContendedReadsNeverBlockDelivery(t *testing.T) {
	s := NewMemoryCollabRecordsStore()
	ctx := context.Background()
	key := collabTestKey()
	require.True(t, s.Consumed(ctx, CollabConsumedRecord{Key: key, Account: "account", ExpiresAt: time.Now().Add(time.Hour)}))
	tree := s.tree(key.Scope, false, time.Now())
	tree.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.LookupBatch(ctx, CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{{Key: key}}, Records: true})
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		tree.mu.Unlock()
		t.Fatal("optional read waited on tree lock")
	}
	// A blocked tree does not hold the service registry or another tree's writes.
	other := key
	other.Scope.Tree = "other"
	require.True(t, s.Consumed(ctx, CollabConsumedRecord{Key: other, Account: "account", ExpiresAt: time.Now().Add(time.Hour)}))
	tree.mu.Unlock()
}

func TestCollabFixNormalizationBeforeQueue(t *testing.T) {
	received := make(chan CollabRecordMutation, 4)
	shared := &collabSharedTestStore{apply: func(_ context.Context, m CollabRecordMutation) error { received <- m; return nil }}
	safe := NewSafeCollabRecordsStore(NewMemoryCollabRecordsStore(), shared, 8)
	defer safe.Close()
	key := collabTestKey()
	expiry := time.Now().Add(2 * CollabRecordTTL)
	require.True(t, safe.Terminated(context.Background(), CollabTerminatedRecord{Key: key, Event: "event", Copies: []CollabDigest{"copy"}, ExpiresAt: expiry}))
	select {
	case m := <-received:
		require.LessOrEqual(t, time.Until(m.Terminated.ExpiresAt), CollabRecordTTL)
		require.Len(t, m.Terminated.Copies, 1)
	case <-time.After(time.Second):
		t.Fatal("normalized write missing")
	}
	before := CollabSafetySnapshot().RecordDropped
	safe.enqueue(CollabRecordMutation{Kind: CollabRecordMutationTerminated, Terminated: CollabTerminatedRecord{Key: key, Event: "event", Copies: make([]CollabDigest, 4096), ExpiresAt: expiry}})
	require.Equal(t, before, CollabSafetySnapshot().RecordDropped)
	select {
	case m := <-received:
		require.Equal(t, key, m.Terminated.Key)
		require.LessOrEqual(t, len(m.Terminated.Copies), CollabRecordMaxCopies)
	case <-time.After(time.Second):
		t.Fatal("primary termination must survive excess copies")
	}
	require.Empty(t, safe.writes)
}

func TestCollabFixSameTokenVoidIsFinal(t *testing.T) {
	key := collabTestKey()
	ctx := context.Background()
	local := NewMemoryCollabRecordsStore()
	remote := NewMemoryCollabRecordsStore()
	r := CollabIssuedRecord{Scope: key.Scope, Call: "call", Digest: key.Digest, Account: "account", Generation: 1, ExpiresAt: time.Now().Add(time.Hour)}
	token, ok := local.PendingIssued(ctx, r)
	require.True(t, ok)
	require.True(t, remote.ApplyMutation(CollabRecordMutation{Kind: CollabRecordMutationPending, Token: token, Issued: r}))
	require.True(t, remote.ConfirmIssued(ctx, token))
	local.VoidIssued(ctx, token)
	q := CollabRecordQuery{Key: key}
	b := CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{q}, Records: true}
	merged := collabMergeRecords(remote.LookupBatch(ctx, b).Records[q], local.LookupBatch(ctx, b).Records[q])
	require.Len(t, merged.IssuedTokens, 1)
	require.Equal(t, CollabIssuedStatusVoid, merged.Issued[0].Status)
}

func BenchmarkCollabFixBoundedLocalRead(b *testing.B) {
	s := NewMemoryCollabRecordsStore()
	key := collabTestKey()
	for i := 0; i < CollabRecordMaxTreeEntries; i++ {
		s.Consumed(context.Background(), CollabConsumedRecord{Key: key, Account: CollabDigest(fmt.Sprint(i)), ExpiresAt: time.Now().Add(time.Hour)})
	}
	queries := make([]CollabRecordQuery, 32768)
	for i := range queries {
		queries[i] = CollabRecordQuery{Key: key, Call: CollabDigest(fmt.Sprint(i))}
		queries[i].Key.Digest = "missing"
	}
	batch := CollabRecordBatch{Scope: key.Scope, Queries: queries, Records: true}
	b.ReportAllocs()
	for b.Loop() {
		s.LookupBatch(context.Background(), batch)
	}
}

func TestCollabFixQueryBudgetIsSharedAcrossReads(t *testing.T) {
	s := NewSafeCollabRecordsStore(NewMemoryCollabRecordsStore(), nil, 8)
	defer s.Close()
	key := collabTestKey()
	ctx, cancel := WithCollabRecordReadBudget(context.Background())
	defer cancel()
	require.True(t, s.Consumed(ctx, CollabConsumedRecord{Key: key, Account: "account", ExpiresAt: time.Now().Add(time.Hour)}))
	queries := make([]CollabRecordQuery, CollabRecordMaxQueries)
	for i := range queries {
		queries[i] = CollabRecordQuery{Key: key, Call: CollabDigest(fmt.Sprint(i))}
	}
	batch := CollabRecordBatch{Scope: key.Scope, Queries: queries, Records: true}
	require.Len(t, s.LookupBatch(ctx, batch).Records, CollabRecordMaxQueries)
	nested, stop := WithCollabRecordReadBudget(ctx)
	defer stop()
	require.Len(t, s.LookupBatch(nested, batch).Records, CollabRecordMaxQueries, "duplicates do not spend budget again")
	extra := batch
	extra.Queries = []CollabRecordQuery{{Key: key, Call: "new-query"}}
	result := s.LookupBatch(nested, extra)
	require.False(t, result.RecordsComplete)
	require.Empty(t, result.Records)
}
