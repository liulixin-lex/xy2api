package resilience

import (
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

type collabRecordDeadlineKey struct{}
type collabRecordReadBudget struct{ used atomic.Bool }

// Create once at the first batch read and reuse for the whole request. Nesting
// never renews the budget. Without this context the adapter serves local only.
func WithCollabRecordReadBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Value(collabRecordDeadlineKey{}).(*collabRecordReadBudget); ok {
		return ctx, func() {}
	}
	bounded, cancel := context.WithTimeout(ctx, CollabRecordTimeout)
	return context.WithValue(bounded, collabRecordDeadlineKey{}, &collabRecordReadBudget{}), cancel
}

// One writer and at most four shared readers per store. A full queue drops a
// shared write after the local mutation; no delivery waits for the network.
// Construct once per service, never per request. Close at service shutdown.
type SafeCollabRecordsStore struct {
	local   CollabRecordsStore
	shared  CollabSharedRecordsStore
	writes  chan CollabRecordMutation
	readers chan struct{}
	stop    context.Context
	cancel  context.CancelFunc
	once    sync.Once
}

func NewSafeCollabRecordsStore(local CollabRecordsStore, shared CollabSharedRecordsStore, capacity int) *SafeCollabRecordsStore {
	if local == nil {
		local = NoopCollabRecordsStore{}
	}
	stop, cancel := context.WithCancel(context.Background())
	s := &SafeCollabRecordsStore{local: local, shared: shared, writes: make(chan CollabRecordMutation, min(max(capacity, 1), 1024)), readers: make(chan struct{}, 4), stop: stop, cancel: cancel}
	if shared != nil {
		go s.runWrites()
	}
	return s
}
func (s *SafeCollabRecordsStore) Close() { s.once.Do(s.cancel) }

func (s *SafeCollabRecordsStore) enqueue(mutation CollabRecordMutation) {
	if s.shared == nil {
		return
	}
	if s.stop.Err() != nil {
		collabSafety.recordDropped.Add(1)
		return
	}
	select {
	case s.writes <- mutation:
	default:
		collabSafety.recordDropped.Add(1)
	}
}
func (s *SafeCollabRecordsStore) runWrites() {
	defer collabRecover(&collabSafety.recordPanics)
	for {
		select {
		case <-s.stop.Done():
			return
		case mutation := <-s.writes:
			s.applyShared(mutation)
		}
	}
}
func (s *SafeCollabRecordsStore) applyShared(mutation CollabRecordMutation) {
	defer collabRecover(&collabSafety.recordPanics)
	ctx, cancel := context.WithTimeout(s.stop, CollabRecordTimeout)
	defer cancel()
	if err := s.shared.Apply(ctx, mutation); err != nil {
		collabSafety.recordErrors.Add(1)
	}
}
func (s *SafeCollabRecordsStore) lookupLocal(ctx context.Context, batch CollabRecordBatch) (result CollabRecordBatchResult) {
	defer collabRecover(&collabSafety.recordPanics)
	return s.local.LookupBatch(ctx, batch)
}
func (s *SafeCollabRecordsStore) lookupShared(ctx context.Context, batch CollabRecordBatch) (result CollabRecordBatchResult) {
	defer collabRecover(&collabSafety.recordPanics)
	result, err := s.shared.LookupBatch(ctx, batch)
	if err != nil {
		collabSafety.recordErrors.Add(1)
		return CollabRecordBatchResult{}
	}
	return result
}
func (s *SafeCollabRecordsStore) LookupBatch(ctx context.Context, batch CollabRecordBatch) CollabRecordBatchResult {
	local := s.lookupLocal(ctx, batch)
	deadline, bounded := ctx.Deadline()
	budget, budgeted := ctx.Value(collabRecordDeadlineKey{}).(*collabRecordReadBudget)
	if s.shared == nil || !bounded || !budgeted || ctx.Err() != nil || s.stop.Err() != nil {
		return local
	}
	// Defend against a forged context or a shared implementation ignoring cancel.
	if time.Until(deadline) > CollabRecordTimeout || !budget.used.CompareAndSwap(false, true) {
		return local
	}
	select {
	case s.readers <- struct{}{}:
	default:
		return local
	}
	batch.Queries = slices.Clone(batch.Queries)
	done := make(chan CollabRecordBatchResult, 1)
	go func() {
		defer collabRecover(&collabSafety.recordPanics)
		defer func() { <-s.readers }()
		done <- s.lookupShared(ctx, batch)
	}()
	select {
	case <-ctx.Done():
		return local
	case remote := <-done:
		if ctx.Err() != nil {
			return local
		}
		// Local state wins over delayed shared writes, including pending voids.
		remote.Records = maps.Clone(remote.Records)
		remote.Dispositions = maps.Clone(remote.Dispositions)
		if remote.Records == nil && len(local.Records) > 0 {
			remote.Records = make(map[CollabRecordQuery]CollabRecordLookup)
		}
		if remote.Dispositions == nil && len(local.Dispositions) > 0 {
			remote.Dispositions = make(map[CollabRecordQuery]CollabDispositionLookup)
		}
		maps.Copy(remote.Records, local.Records)
		maps.Copy(remote.Dispositions, local.Dispositions)
		remote.TreeLocalEndings = max(remote.TreeLocalEndings, local.TreeLocalEndings)
		return remote
	}
}

func (s *SafeCollabRecordsStore) ConfirmIssued(ctx context.Context, value CollabIssuedToken) (ok bool) {
	defer collabRecover(&collabSafety.recordPanics)
	if !s.local.ConfirmIssued(ctx, value) {
		return false
	}
	s.enqueue(CollabRecordMutation{Kind: CollabRecordMutationConfirm, Token: value})
	return true
}

func (s *SafeCollabRecordsStore) PlaintextDelivered(ctx context.Context, value CollabPlaintextRecord) (ok bool) {
	defer collabRecover(&collabSafety.recordPanics)
	if !s.local.PlaintextDelivered(ctx, value) {
		return false
	}
	s.enqueue(CollabRecordMutation{Kind: CollabRecordMutationPlaintext, Plaintext: value})
	return true
}

func (s *SafeCollabRecordsStore) Consumed(ctx context.Context, value CollabConsumedRecord) (ok bool) {
	defer collabRecover(&collabSafety.recordPanics)
	if !s.local.Consumed(ctx, value) {
		return false
	}
	s.enqueue(CollabRecordMutation{Kind: CollabRecordMutationConsumed, Consumed: value})
	return true
}

func (s *SafeCollabRecordsStore) Replaced(ctx context.Context, value CollabReplacedRecord) (ok bool) {
	defer collabRecover(&collabSafety.recordPanics)
	if !s.local.Replaced(ctx, value) {
		return false
	}
	s.enqueue(CollabRecordMutation{Kind: CollabRecordMutationReplaced, Replaced: value})
	return true
}

func (s *SafeCollabRecordsStore) Terminated(ctx context.Context, value CollabTerminatedRecord) (ok bool) {
	defer collabRecover(&collabSafety.recordPanics)
	value.Copies = slices.Clone(value.Copies)
	if !s.local.Terminated(ctx, value) {
		return false
	}
	s.enqueue(CollabRecordMutation{Kind: CollabRecordMutationTerminated, Terminated: value})
	return true
}

func (s *SafeCollabRecordsStore) PendingIssued(ctx context.Context, record CollabIssuedRecord) (token CollabIssuedToken, ok bool) {
	defer collabRecover(&collabSafety.recordPanics)
	token, ok = s.local.PendingIssued(ctx, record)
	if ok {
		s.enqueue(CollabRecordMutation{Kind: CollabRecordMutationPending, Issued: record, Token: token})
	}
	return token, ok
}
func (s *SafeCollabRecordsStore) VoidIssued(ctx context.Context, token CollabIssuedToken) {
	defer collabRecover(&collabSafety.recordPanics)
	s.local.VoidIssued(ctx, token)
	s.enqueue(CollabRecordMutation{Kind: CollabRecordMutationVoid, Token: token})
}
func (s *SafeCollabRecordsStore) IncrementTreeLocalEnding(ctx context.Context, scope CollabRecordScope, event CollabDigest) (count int, ok bool) {
	defer collabRecover(&collabSafety.recordPanics)
	count, ok = s.local.IncrementTreeLocalEnding(ctx, scope, event)
	if ok {
		s.enqueue(CollabRecordMutation{Kind: CollabRecordMutationLocalEnding, Scope: scope, Event: event})
	}
	return count, ok
}

func (s *SafeCollabRecordsStore) TerminalRejected(ctx context.Context, record CollabTerminalRejectionRecord) (count int, ok bool) {
	defer collabRecover(&collabSafety.recordPanics)
	count, ok = s.local.TerminalRejected(ctx, record)
	if ok {
		s.enqueue(CollabRecordMutation{Kind: CollabRecordMutationTerminalRejected, TerminalRejection: record})
	}
	return count, ok
}
