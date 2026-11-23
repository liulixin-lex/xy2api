package resilience

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"sync/atomic"
	"testing"
	"time"
)

func TestCollabRecoveryReservationReceipt(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCollabRecordsStore()
	k := capacityKey(0)
	r := CollabTerminatedRecord{Key: k, Event: capacityDigest(90), ExpiresAt: time.Now().Add(CollabRecordTTL)}
	_, ok := s.ReserveLocalEnding(ctx, []CollabTerminatedRecord{r})
	require.True(t, ok)
	q := CollabRecordQuery{Key: k}
	got := s.LookupBatch(ctx, CollabRecordBatch{Scope: k.Scope, Queries: []CollabRecordQuery{q}, Dispositions: true})
	require.Len(t, got.Dispositions[q].Terminated, 1)
	require.False(t, CollabTerminationEffective(got.Dispositions[q].Terminated[0], k, nil, time.Now()))
	require.True(t, CollabTerminationEffective(got.Dispositions[q].Terminated[0], k, []CollabDigest{r.Event}, time.Now()))
}
func TestCollabRecoveryVoidAfterEviction(t *testing.T) {
	ctx := context.Background()
	local := NewMemoryCollabRecordsStore()
	k := capacityKey(0)
	token, ok := local.PendingIssued(ctx, CollabIssuedRecord{Scope: k.Scope, Call: capacityDigest(8), Digest: k.Digest, Account: capacityDigest(9), ExpiresAt: time.Now().Add(time.Hour)})
	require.True(t, ok)
	require.True(t, local.ConfirmIssued(ctx, token))
	q := CollabRecordQuery{Key: k}
	batch := CollabRecordBatch{Scope: k.Scope, Queries: []CollabRecordQuery{q}, Records: true}
	old := local.LookupBatch(ctx, batch)
	shared := &collabSharedTestStore{lookup: func(context.Context, CollabRecordBatch) (CollabRecordBatchResult, error) {
		for i := 0; i < 64; i++ {
			local.PlaintextDelivered(ctx, CollabPlaintextRecord{Scope: k.Scope, Call: capacityDigest(100 + i), Arguments: capacityDigest(9), ExpiresAt: time.Now().Add(time.Hour)})
		}
		local.VoidIssued(ctx, token)
		return old, nil
	}}
	s := NewSafeCollabRecordsStore(local, shared, 8)
	defer s.Close()
	budget, cancel := WithCollabRecordReadBudget(ctx)
	defer cancel()
	for _, v := range s.LookupBatch(budget, batch).Records[q].Issued {
		require.NotEqual(t, CollabIssuedStatusConfirmed, v.Status)
	}
	require.False(t, local.ConfirmIssued(ctx, token))
}
func TestCollabRecoveryPublicationConflict(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	entered := make(chan struct{}, 2)
	shared := &collabSharedTestStore{apply: func(ctx context.Context, _ CollabRecordMutation) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	s := NewSafeCollabRecordsStore(NewMemoryCollabRecordsStore(), shared, 8)
	defer s.Close()
	a := CollabTerminatedRecord{Key: capacityKey(0), Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Hour)}
	b := a
	b.Copies = []CollabDigest{capacityDigest(100)}
	done := make(chan bool, 1)
	go func() { done <- s.Terminated(ctx, a) }()
	<-entered
	second := make(chan bool, 1)
	go func() { second <- s.Terminated(ctx, b) }()
	time.Sleep(10 * time.Millisecond)
	close(release)
	firstOK, secondOK := <-done, <-second
	require.True(t, firstOK)
	require.False(t, secondOK, "conflicting payload must be refused")
}
func TestCollabRecoveryPublicationsNeverWait(t *testing.T) {
	for _, down := range []bool{false, true} {
		t.Run(map[bool]string{false: "slow", true: "down"}[down], func(t *testing.T) {
			shared := &collabSharedTestStore{apply: func(ctx context.Context, _ CollabRecordMutation) error {
				if down {
					return errors.New("unavailable")
				}
				<-ctx.Done()
				return ctx.Err()
			}}
			s := NewSafeCollabRecordsStore(NewMemoryCollabRecordsStore(), shared, 64)
			defer s.Close()
			ctx := context.Background()
			start := time.Now()
			for i := 0; i < 5; i++ {
				k := capacityKey(i)
				require.True(t, s.Terminated(ctx, CollabTerminatedRecord{Key: k, Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Hour)}))
				require.True(t, s.Replaced(ctx, CollabReplacedRecord{Key: k, Account: capacityDigest(8), Placeholder: CollabPlaceholderMessage, ExpiresAt: time.Now().Add(time.Hour)}))
				_, ok := s.TerminalRejected(ctx, CollabTerminalRejectionRecord{Key: k, Event: capacityDigest(90), Rejection: capacityDigest(8), ExpiresAt: time.Now().Add(time.Hour)})
				require.True(t, ok)
			}
			elapsed := time.Since(start)
			t.Logf("15 local mutations backend=%t elapsed=%s", down, elapsed)
			require.Less(t, elapsed, 50*time.Millisecond)
		})
	}
}

func TestCollabRecoveryInvalidationOverflow(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryCollabRecordsStore()
	k := capacityKey(0)
	token, ok := s.PendingIssued(ctx, CollabIssuedRecord{Scope: k.Scope, Call: capacityDigest(8), Digest: k.Digest, Account: capacityDigest(9), ExpiresAt: time.Now().Add(time.Hour)})
	require.True(t, ok)
	require.True(t, s.ConfirmIssued(ctx, token))
	q := CollabRecordQuery{Key: k}
	b := CollabRecordBatch{Scope: k.Scope, Queries: []CollabRecordQuery{q}, Records: true}
	positive := s.LookupBatch(ctx, b).Records[q]
	for i := 0; i < 100; i++ {
		s.VoidIssued(ctx, CollabIssuedToken{Scope: k.Scope, Call: capacityDigest(100 + i), Token: capacityDigest(200 + i)})
	}
	got := collabMergeRecords(positive, s.LookupBatch(ctx, b).Records[q])
	for _, v := range got.Issued {
		require.NotEqual(t, CollabIssuedStatusConfirmed, v.Status)
	}
	tree := s.tree(k.Scope, false, time.Now())
	require.LessOrEqual(t, tree.bytes, CollabRecordMaxTreeBytes)
}

func TestCollabRecoveryMutationLatencyByKind(t *testing.T) {
	for _, down := range []bool{false, true} {
		s := NewSafeCollabRecordsStore(NewMemoryCollabRecordsStore(), &collabSharedTestStore{apply: func(ctx context.Context, _ CollabRecordMutation) error {
			if down {
				return errors.New("unavailable")
			}
			<-ctx.Done()
			return ctx.Err()
		}}, 64)
		ctx := context.Background()
		k := capacityKey(0)
		expiry := time.Now().Add(time.Hour)
		start := time.Now()
		token, ok := s.PendingIssued(ctx, CollabIssuedRecord{Scope: k.Scope, Call: capacityDigest(8), Digest: k.Digest, Account: capacityDigest(9), ExpiresAt: expiry})
		require.True(t, ok)
		pending := time.Since(start)
		start = time.Now()
		require.True(t, s.ConfirmIssued(ctx, token))
		confirm := time.Since(start)
		start = time.Now()
		s.VoidIssued(ctx, token)
		void := time.Since(start)
		start = time.Now()
		require.True(t, s.PlaintextDelivered(ctx, CollabPlaintextRecord{Scope: k.Scope, Call: capacityDigest(8), Arguments: capacityDigest(9), ExpiresAt: expiry}))
		plain := time.Since(start)
		start = time.Now()
		require.True(t, s.Consumed(ctx, CollabConsumedRecord{Key: k, Account: capacityDigest(9), ExpiresAt: expiry}))
		consumed := time.Since(start)
		start = time.Now()
		require.True(t, s.Replaced(ctx, CollabReplacedRecord{Key: k, Account: capacityDigest(9), Placeholder: CollabPlaceholderMessage, ExpiresAt: expiry}))
		replaced := time.Since(start)
		start = time.Now()
		require.True(t, s.Terminated(ctx, CollabTerminatedRecord{Key: k, Event: capacityDigest(90), ExpiresAt: expiry}))
		terminated := time.Since(start)
		start = time.Now()
		_, ok = s.TerminalRejected(ctx, CollabTerminalRejectionRecord{Key: k, Event: capacityDigest(90), Rejection: capacityDigest(8), ExpiresAt: expiry})
		require.True(t, ok)
		rejected := time.Since(start)
		for _, elapsed := range []time.Duration{pending, confirm, void, plain, consumed, replaced, terminated, rejected} {
			require.Less(t, elapsed, 10*time.Millisecond)
		}
		t.Logf("down=%t pending=%s confirm=%s void=%s plaintext=%s consumed=%s replaced=%s terminated=%s rejected=%s", down, pending, confirm, void, plain, consumed, replaced, terminated, rejected)
		start = time.Now()
		s.Close()
		require.Less(t, time.Since(start), 500*time.Millisecond)
	}
}

func TestCollabRecoveryCloseFlushesCanceledVoid(t *testing.T) {
	ctx := context.Background()
	persisted := NewMemoryCollabRecordsStore()
	k := capacityKey(0)
	token, ok := persisted.PendingIssued(ctx, CollabIssuedRecord{Scope: k.Scope, Call: capacityDigest(8), Digest: k.Digest, Account: capacityDigest(9), ExpiresAt: time.Now().Add(time.Hour)})
	require.True(t, ok)
	require.True(t, persisted.ConfirmIssued(ctx, token))
	entered := make(chan struct{})
	var calls atomic.Int64
	shared := &collabSharedTestStore{apply: func(ctx context.Context, m CollabRecordMutation) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		if !persisted.ApplyMutation(m) {
			return errors.New("refused")
		}
		return nil
	}}
	s := NewSafeCollabRecordsStore(NewMemoryCollabRecordsStore(), shared, 8)
	s.VoidIssued(ctx, token)
	<-entered
	start := time.Now()
	s.Close()
	require.Less(t, time.Since(start), 500*time.Millisecond)
	require.GreaterOrEqual(t, calls.Load(), int64(2))
	q := CollabRecordQuery{Key: k}
	got := persisted.LookupBatch(ctx, CollabRecordBatch{Scope: k.Scope, Queries: []CollabRecordQuery{q}, Records: true})
	for _, v := range got.Records[q].Issued {
		require.NotEqual(t, CollabIssuedStatusConfirmed, v.Status)
	}
}
