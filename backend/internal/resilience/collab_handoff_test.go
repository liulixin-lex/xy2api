package resilience

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollabSavedTerminalSnapshotIsImmutable(t *testing.T) {
	failure := errors.New("synthetic original failure")
	headers := http.Header{"Retry-After": []string{"3"}}
	body := []byte("synthetic inner error")
	input := CollabTerminalSnapshot{Failure: failure, Status: 503, Headers: headers, Advice: RetryAdvice{Seconds: 3}, AccountID: 2, Retryable: true,
		Rejection: CollabEvidence{Grade: CollabEvidenceGradeVerified, Named: &CollabNamedItem{Identity: "synthetic"}, Inner: &CollabInnerError{Payload: body, Validated: true}}}
	saved := NewCollabSavedTerminal(input)
	headers.Set("Retry-After", "9")
	body[0] = 'x'
	input.Rejection.Named.Identity = "changed"
	require.Equal(t, "3", saved.Snapshot.Headers.Get("Retry-After"))
	require.Equal(t, "synthetic inner error", string(saved.Snapshot.Rejection.Inner.Payload))
	require.Equal(t, "synthetic", saved.Snapshot.Rejection.Named.Identity)
	require.Same(t, failure, saved.Snapshot.Failure)
	require.Equal(t, 503, saved.Snapshot.Status)
	require.Equal(t, int64(2), saved.Snapshot.AccountID)
	require.Equal(t, 3, saved.Snapshot.Advice.Seconds)
	require.True(t, saved.Snapshot.Retryable)

}

func TestCollabTakeoverDeadlineAndUserWaiters(t *testing.T) {
	for _, retryable := range []bool{false, true} {
		budget := CollabOptionalBudget{Order: CollabOrderLegacyFirst, SavedRetryable: retryable}
		p := CollabOptionalPolicy(budget)
		want := time.Minute
		if retryable {
			want = 15 * time.Second
		}
		require.Equal(t, want, p.FirstEventTimeout)
		require.Equal(t, want, p.TotalOptionalWait)
		budget.UserHasWaiters = true
		require.False(t, CollabOptionalPolicy(budget).Allowed, "skip or stop as soon as a user waits")
	}
	lossless := CollabOptionalBudget{Order: CollabOrderLosslessFirst, SavedRetryable: true, CompensationAvailable: true, FirstAccountSpareSlots: true}
	require.Equal(t, time.Minute, CollabOptionalPolicy(lossless).TotalOptionalWait)
	lossless.UserHasWaiters = true
	require.False(t, CollabOptionalPolicy(lossless).Allowed)
}

type collabCounterStore struct {
	NoopCollabRecordsStore
	counter *CollabTerminalRejectionCounter
	now     time.Time
}

func (s *collabCounterStore) TerminalRejected(_ context.Context, record CollabTerminalRejectionRecord) (int, bool) {
	return s.counter.Record(record, s.now)
}
func (s *collabCounterStore) LookupBatch(_ context.Context, batch CollabRecordBatch) CollabRecordBatchResult {
	result := CollabRecordBatchResult{}
	if batch.Dispositions {
		result.Dispositions = make(map[CollabRecordQuery]CollabDispositionLookup)
		for _, query := range batch.Queries {
			result.Dispositions[query] = CollabDispositionLookup{TerminalRejections: s.counter.Counts(query.Key, s.now)}
		}
	}
	return result
}

func TestCollabLocalEndingPriorRejectionsAndAsyncCounter(t *testing.T) {
	retryable := NewCollabSavedTerminal(CollabTerminalSnapshot{Retryable: true})
	permanent := NewCollabSavedTerminal(CollabTerminalSnapshot{Retryable: false})
	now := time.Now()
	delivery := CollabDeliveryKey{Scope: CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}, Thread: "thread", Item: "item-a", Delivery: "delivery-a", Digest: "content"}
	prior := []CollabTerminalRejectionCount{{Key: delivery, Rejection: "rejection-a", Count: 2, ExpiresAt: now.Add(time.Hour)}}
	require.True(t, CollabLocalEndingPermitted(permanent, delivery, "rejection-a", nil, now))
	require.False(t, CollabLocalEndingPermitted(retryable, delivery, "rejection-a", nil, now))
	prior[0].Count = 1
	require.False(t, CollabLocalEndingPermitted(retryable, delivery, "rejection-a", prior, now))
	prior[0].Count = 2
	require.True(t, CollabLocalEndingPermitted(retryable, delivery, "rejection-a", prior, now))
	require.False(t, CollabLocalEndingPermitted(retryable, delivery, "other-rejection", prior, now))
	require.False(t, CollabLocalEndingPermitted(retryable, CollabDeliveryKey{Delivery: "other"}, "rejection-a", prior, now))
	require.False(t, CollabLocalEndingPermitted(retryable, delivery, "rejection-a", prior, now.Add(2*time.Hour)))
	require.False(t, CollabLocalEndingPermitted(nil, delivery, "rejection-a", prior, now))
	counter := NewCollabTerminalRejectionCounter(2)
	key := delivery
	record := CollabTerminalRejectionRecord{Key: key, Rejection: "rejection-a", Event: "event-a", ExpiresAt: now.Add(time.Hour)}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	shared := &collabSharedTestStore{apply: func(ctx context.Context, m CollabRecordMutation) error {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}}
	local := &collabCounterStore{counter: counter, now: now}
	store := NewSafeCollabRecordsStore(local, shared, 4)
	defer store.Close()
	count, ok := store.TerminalRejected(context.Background(), record)
	require.True(t, ok)
	require.Equal(t, 1, count)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("shared writer did not start")
	}
	count, ok = store.TerminalRejected(context.Background(), record)
	require.True(t, ok)
	require.Equal(t, 1, count, "one terminal must not count twice")
	record.Event = "event-b"
	count, ok = store.TerminalRejected(context.Background(), record)
	require.True(t, ok)
	require.Equal(t, 2, count, "local mutation succeeds while shared store waits")
	record.Event = "event-c"
	count, ok = counter.Record(record, now)
	require.True(t, ok)
	require.Equal(t, 2, count, "saturates at the eligibility threshold")
	query := CollabRecordQuery{Key: key}
	result := store.LookupBatch(context.Background(), CollabRecordBatch{Scope: key.Scope, Queries: []CollabRecordQuery{query}, Dispositions: true})
	require.Equal(t, 2, result.Dispositions[query].TerminalRejections[0].Count)
	other := record
	other.Key.Item = "item-b"
	other.Key.Delivery = "delivery-b"
	require.Empty(t, counter.Counts(other.Key, now), "identical content in a new delivery does not inherit terminal history")
	other.Rejection = "rejection-b"
	count, ok = counter.Record(other, now)
	require.True(t, ok)
	require.Equal(t, 1, count)
	overflow := record
	overflow.Rejection = "rejection-c"
	_, ok = counter.Record(overflow, now)
	require.False(t, ok, "bounded capacity fails as absence")
	require.Empty(t, counter.Counts(key, now.Add(2*time.Hour)), "expired history does not permit local ending")
	close(release)
}

func TestCollabBeforeFailureRoutingGates(t *testing.T) {
	settings := Defaults()
	settings.Collab.Ladder = true
	settings.Collab.TreeAffinity = true
	settings.Collab.RecordRouting = true
	require.True(t, settings.CollabRecordRoutingOn(), "failover ordering remains available")
	require.False(t, settings.CollabLeaseMigrationSuppressionOn())
	require.False(t, settings.CollabRedispatchBorrowOn())
	settings.Collab.LosslessFirst = true
	require.True(t, settings.CollabLeaseMigrationSuppressionOn())
	require.True(t, settings.CollabRedispatchBorrowOn())
	settings.Collab.TreeAffinity = false
	require.False(t, settings.CollabLeaseMigrationSuppressionOn())
	require.True(t, settings.CollabRedispatchBorrowOn())
	settings.Collab.RecordRouting = false
	require.False(t, settings.CollabRedispatchBorrowOn())
	settings.Collab.TreeAffinity = true
	settings.Collab.RecordRouting = true
	settings.Collab.Ladder = false
	require.False(t, settings.CollabLeaseMigrationSuppressionOn())
	require.False(t, settings.CollabRedispatchBorrowOn())
	settings.Collab.Ladder = true
	settings.Enabled = false
	require.False(t, settings.CollabLeaseMigrationSuppressionOn())
	require.False(t, settings.CollabRedispatchBorrowOn())
}

func TestCollabHandledNamedItemOwnEndpoint(t *testing.T) {
	named := ResolveCollabNamedItem(CollabNamedItem{Identity: "synthetic"}, []CollabOriginalItem{{Identity: "synthetic", Item: "digest"}})
	require.Equal(t, CollabNamedNextRepair, CollabNamedNextStep(named, nil))
	require.Equal(t, CollabNamedNextOwnEndpoint, CollabNamedNextStep(named, map[CollabDigest]bool{"digest": true}))
	named.MatchCount = 2
	require.Equal(t, CollabNamedNextUnlocatable, CollabNamedNextStep(named, map[CollabDigest]bool{"digest": true}))
}

func TestCollabTerminalCounterSharedFailuresAreContained(t *testing.T) {
	for _, panicWrite := range []bool{false, true} {
		completed := make(chan struct{})
		before := CollabSafetySnapshot()
		shared := &collabSharedTestStore{apply: func(context.Context, CollabRecordMutation) error {
			defer close(completed)
			if panicWrite {
				panic("synthetic")
			}
			return errors.New("synthetic")
		}}
		now := time.Now()
		local := &collabCounterStore{counter: NewCollabTerminalRejectionCounter(1), now: now}
		store := NewSafeCollabRecordsStore(local, shared, 1)
		record := CollabTerminalRejectionRecord{Key: CollabDeliveryKey{Scope: CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}, Thread: "thread", Delivery: "delivery", Item: "item", Digest: "content"}, Rejection: "rejection", Event: "event", ExpiresAt: now.Add(time.Hour)}
		count, ok := store.TerminalRejected(context.Background(), record)
		require.True(t, ok)
		require.Equal(t, 1, count)
		select {
		case <-completed:
		case <-time.After(time.Second):
			t.Fatal("shared mutation did not run")
		}
		require.Eventually(t, func() bool {
			after := CollabSafetySnapshot()
			return after.RecordPanics > before.RecordPanics || after.RecordErrors > before.RecordErrors
		}, time.Second, time.Millisecond)
		require.Equal(t, 1, local.counter.Counts(record.Key, now)[0].Count)
		store.Close()
	}
}

func TestCollabTerminalCounterIncompleteKeysStayLocal(t *testing.T) {
	for _, missing := range []string{"tenant", "group", "tree", "thread", "digest"} {
		t.Run(missing, func(t *testing.T) {
			complete := CollabDeliveryKey{Scope: CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}, Thread: "thread", Item: "item", Delivery: "delivery", Digest: "content"}
			key := complete
			switch missing {
			case "tenant":
				key.Scope.Tenant = ""
			case "group":
				key.Scope.Group = ""
			case "tree":
				key.Scope.Tree = ""
			case "thread":
				key.Thread = ""
			case "digest":
				key.Digest = ""
			}
			now := time.Now()
			local := &collabCounterStore{counter: NewCollabTerminalRejectionCounter(2), now: now}
			var sharedCalls, incompleteCalls atomic.Int64
			shared := &collabSharedTestStore{apply: func(_ context.Context, m CollabRecordMutation) error {
				sharedCalls.Add(1)
				if m.TerminalRejection.Key != complete {
					incompleteCalls.Add(1)
				}
				return nil
			}}
			store := NewSafeCollabRecordsStore(local, shared, 4)
			defer store.Close()
			record := CollabTerminalRejectionRecord{Key: key, Rejection: "rejection", Event: "event", ExpiresAt: now.Add(time.Hour)}
			ctx := context.Background()
			for _, event := range []string{"event", "event", "second-event"} {
				record.Event = CollabDigest(event)
				count, ok := store.TerminalRejected(ctx, record)
				require.True(t, ok)
				want := 1
				if event == "second-event" {
					want = 2
				}
				require.Equal(t, want, count)
			}
			counts := local.counter.Counts(key, now)
			require.Len(t, counts, 1)
			require.Equal(t, 2, counts[0].Count)
			store.deferredMu.Lock()
			pending := len(store.deferred)
			store.deferredMu.Unlock()
			require.Zero(t, pending, "incomplete identities must not enter the shared write queue")
			require.Zero(t, sharedCalls.Load())

			// A complete control proves the shared worker runs. Waiting for its
			// queue to drain makes the zero-incomplete-write assertion definite.
			record.Key = complete
			count, ok := store.TerminalRejected(ctx, record)
			require.True(t, ok)
			require.Equal(t, 1, count)
			require.Eventually(t, func() bool {
				store.deferredMu.Lock()
				defer store.deferredMu.Unlock()
				return len(store.deferred) == 0 && sharedCalls.Load() > 0
			}, time.Second, time.Millisecond)
			require.Equal(t, int64(1), sharedCalls.Load())
			require.Zero(t, incompleteCalls.Load())
		})
	}
}
