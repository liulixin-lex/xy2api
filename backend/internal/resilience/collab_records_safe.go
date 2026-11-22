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
type collabRecordReadBudget struct {
	used                  atomic.Bool
	mu                    sync.Mutex
	records, dispositions map[CollabRecordQuery]bool
}

// Create once at the first batch read and reuse for the whole request. Nesting
// never renews the budget. Without this context the adapter serves local only.
func WithCollabRecordReadBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Value(collabRecordDeadlineKey{}).(*collabRecordReadBudget); ok {
		return ctx, func() {}
	}
	bounded, cancel := context.WithTimeout(ctx, CollabRecordTimeout)
	return context.WithValue(bounded, collabRecordDeadlineKey{}, &collabRecordReadBudget{}), cancel
}

// One writer and at most four shared reads/reservations per store. Separate
// bounded queues reserve disposition throughput. A full queue drops a
// shared write after the local mutation; no delivery waits for the network.
// Construct once per service, never per request. Close at service shutdown.
type SafeCollabRecordsStore struct {
	local        CollabRecordsStore
	shared       CollabSharedRecordsStore
	writes       chan CollabRecordMutation
	dispositions chan CollabRecordMutation
	readers      chan struct{}
	stop         context.Context
	cancel       context.CancelFunc
	once         sync.Once
}

func NewSafeCollabRecordsStore(local CollabRecordsStore, shared CollabSharedRecordsStore, capacity int) *SafeCollabRecordsStore {
	if local == nil {
		local = NoopCollabRecordsStore{}
	}
	stop, cancel := context.WithCancel(context.Background())
	s := &SafeCollabRecordsStore{dispositions: make(chan CollabRecordMutation, 256), local: local, shared: shared, writes: make(chan CollabRecordMutation, min(max(capacity, 1), 1024)), readers: make(chan struct{}, 4), stop: stop, cancel: cancel}
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
	normalized, _, ok := NormalizeCollabRecordMutation(mutation, time.Now())
	if !ok {
		collabSafety.recordDropped.Add(1)
		return
	}
	queue := s.writes
	if collabRecordPartition(normalized.Kind) != 0 {
		queue = s.dispositions
	}
	select {
	case queue <- normalized:
	default:
		collabSafety.recordDropped.Add(1)
	}
}

func (s *SafeCollabRecordsStore) runWrites() {
	for {
		var first CollabRecordMutation
		select {
		case <-s.stop.Done():
			return
		case first = <-s.dispositions:
		default:
			select {
			case <-s.stop.Done():
				return
			case first = <-s.dispositions:
			case first = <-s.writes:
			}
		}
		batch := []CollabRecordMutation{first}
	drain:
		for len(batch) < 64 {
			select {
			case next := <-s.dispositions:
				batch = append(batch, next)
			default:
				select {
				case next := <-s.writes:
					batch = append(batch, next)
				default:
					break drain
				}
			}
		}
		s.applySharedBatch(batch)
	}
}

// Batch support is optional so existing shared-store implementations keep working.
type CollabSharedRecordsBatchWriter interface {
	ApplyBatch(context.Context, []CollabRecordMutation) error
}

func (s *SafeCollabRecordsStore) applySharedBatch(batch []CollabRecordMutation) {
	defer collabRecover(&collabSafety.recordPanics)
	if writer, ok := s.shared.(CollabSharedRecordsBatchWriter); ok {
		ctx, cancel := context.WithTimeout(s.stop, CollabRecordTimeout)
		defer cancel()
		if writer.ApplyBatch(ctx, batch) != nil {
			collabSafety.recordErrors.Add(1)
		}
		return
	}
	for _, mutation := range batch {
		if s.stop.Err() != nil {
			return
		}
		s.applyShared(mutation)
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
	result = collabUnknownBatch(batch)
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

	batch = collabBoundBatch(batch)
	if budget, ok := ctx.Value(collabRecordDeadlineKey{}).(*collabRecordReadBudget); ok {
		budget.mu.Lock()
		if batch.Dispositions {
			batch.DispositionQueries, batch.dispositionsTruncated = collabBudgetQueries(batch.DispositionQueries, &budget.dispositions, batch.dispositionsTruncated)
		}
		if batch.Records {
			batch.Queries, batch.recordsTruncated = collabBudgetQueries(batch.Queries, &budget.records, batch.recordsTruncated)
		}
		budget.mu.Unlock()
	}
	if ctx.Err() != nil {
		return collabUnknownBatch(batch)
	}
	local := s.lookupLocal(ctx, batch)
	deadline, bounded := ctx.Deadline()
	budget, budgeted := ctx.Value(collabRecordDeadlineKey{}).(*collabRecordReadBudget)
	if s.shared == nil || !bounded || !budgeted || ctx.Err() != nil || s.stop.Err() != nil {
		return s.withoutShared(local, batch)
	}
	// Defend against a forged context or a shared implementation ignoring cancel.
	if time.Until(deadline) > CollabRecordTimeout || !budget.used.CompareAndSwap(false, true) {
		return s.withoutShared(local, batch)
	}
	select {
	case s.readers <- struct{}{}:
	default:
		return s.withoutShared(local, batch)
	}
	batch.Queries = slices.Clone(batch.Queries)
	batch.DispositionQueries = slices.Clone(batch.DispositionQueries)
	done := make(chan CollabRecordBatchResult, 1)
	go func() {
		defer func() { <-s.readers }()
		done <- s.lookupShared(ctx, batch)
	}()
	select {
	case <-ctx.Done():
		return s.withoutShared(local, batch)
	case remote := <-done:
		if ctx.Err() != nil {
			return s.withoutShared(local, batch)
		}
		fresh := s.lookupLocal(ctx, batch)
		if !fresh.RecordsComplete {
			collabDropUnfreshIssues(&local)
			// Clone maps before changing a shared snapshot.
			remote.Records = maps.Clone(remote.Records)
			collabDropUnfreshIssues(&remote)
		}
		for q, r := range fresh.Records {
			if local.Records == nil {
				local.Records = make(map[CollabRecordQuery]CollabRecordLookup)
			}
			local.Records[q] = collabMergeRecords(local.Records[q], r)
		}
		for q, d := range fresh.Dispositions {
			if local.Dispositions == nil {
				local.Dispositions = make(map[CollabRecordQuery]CollabDispositionLookup)
			}
			local.Dispositions[q] = collabMergeDispositions(local.Dispositions[q], d)
		}
		local.TreeLocalEndings = max(local.TreeLocalEndings, fresh.TreeLocalEndings)
		// Local state wins over delayed shared writes, including pending voids.
		remote.Records = maps.Clone(remote.Records)
		remote.Dispositions = maps.Clone(remote.Dispositions)
		if remote.Records == nil && len(local.Records) > 0 {
			remote.Records = make(map[CollabRecordQuery]CollabRecordLookup)
		}
		if remote.Dispositions == nil && len(local.Dispositions) > 0 {
			remote.Dispositions = make(map[CollabRecordQuery]CollabDispositionLookup)
		}
		for query, records := range local.Records {
			remote.Records[query] = collabMergeRecords(remote.Records[query], records)
		}
		for query, dispositions := range local.Dispositions {
			remote.Dispositions[query] = collabMergeDispositions(remote.Dispositions[query], dispositions)
		}
		remote.RecordsComplete = remote.RecordsComplete && fresh.RecordsComplete
		remote.DispositionsComplete = remote.DispositionsComplete && fresh.DispositionsComplete
		remote.TreeLocalEndingsKnown = remote.TreeLocalEndingsKnown && fresh.TreeLocalEndingsKnown
		remote.TreeLocalEndings = max(remote.TreeLocalEndings, local.TreeLocalEndings, fresh.TreeLocalEndings)
		if batch.Dispositions && !remote.TreeLocalEndingsKnown {
			remote.TreeLocalEndings = CollabTreeLocalEndingLimit
		}
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
	value.Copies = slices.Clone(value.Copies[:min(len(value.Copies), CollabRecordMaxCopies)])
	if !s.local.Terminated(ctx, value) {
		return false
	}
	s.enqueue(CollabRecordMutation{Kind: CollabRecordMutationTerminated, Terminated: value})
	return true
}

func (s *SafeCollabRecordsStore) PendingIssued(ctx context.Context, record CollabIssuedRecord) (token CollabIssuedToken, ok bool) {
	defer collabRecover(&collabSafety.recordPanics)
	record.Status = CollabIssuedStatusPending
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

func (s *SafeCollabRecordsStore) IncrementTreeLocalEnding(ctx context.Context, scope CollabRecordScope, event CollabDigest) (int, bool) {
	return s.ReserveTreeLocalEnding(ctx, scope, event)
}
func (s *SafeCollabRecordsStore) ReserveTreeLocalEnding(ctx context.Context, scope CollabRecordScope, event CollabDigest) (count int, ok bool) {
	count = CollabTreeLocalEndingLimit
	defer collabRecover(&collabSafety.recordPanics)
	if ctx.Err() != nil || s.stop.Err() != nil {
		return count, false
	}
	if s.shared == nil {
		count, ok = s.local.IncrementTreeLocalEnding(ctx, scope, event)
		if !ok {
			count = CollabTreeLocalEndingLimit
		}
		return
	}
	reserver, supported := s.shared.(CollabLocalEndingReserver)
	if !supported {
		return count, false
	}
	select {
	case s.readers <- struct{}{}:
	default:
		return count, false
	}
	bounded, cancel := context.WithTimeout(ctx, CollabRecordTimeout)
	defer cancel()
	type reservation struct {
		count int
		ok    bool
	}
	done := make(chan reservation, 1)
	go func() {
		defer func() { <-s.readers }()
		result := reservation{count: CollabTreeLocalEndingLimit}
		func() {
			defer collabRecover(&collabSafety.recordPanics)
			result.count, result.ok = reserver.ReserveTreeLocalEnding(bounded, scope, event)
		}()
		done <- result
	}()
	select {
	case <-bounded.Done():
		return count, false
	case result := <-done:
		if !result.ok || bounded.Err() != nil {
			return count, false
		}
		localCount, localOK := s.local.IncrementTreeLocalEnding(ctx, scope, event)
		if !localOK {
			return count, false
		}
		return max(localCount, result.count), true
	}
}

func (s *SafeCollabRecordsStore) TerminalRejected(ctx context.Context, record CollabTerminalRejectionRecord) (count int, ok bool) {
	defer collabRecover(&collabSafety.recordPanics)
	count, ok = s.local.TerminalRejected(ctx, record)
	if ok {
		s.enqueue(CollabRecordMutation{Kind: CollabRecordMutationTerminalRejected, TerminalRejection: record})
	}
	return count, ok
}

func collabBudgetQueries(queries []CollabRecordQuery, seen *map[CollabRecordQuery]bool, truncated bool) ([]CollabRecordQuery, bool) {
	if *seen == nil {
		*seen = make(map[CollabRecordQuery]bool)
	}
	out := make([]CollabRecordQuery, 0, len(queries))
	for _, q := range queries {
		if !(*seen)[q] && len(*seen) >= CollabRecordMaxQueries {
			truncated = true
			continue
		}
		(*seen)[q] = true
		out = append(out, q)
	}
	return out, truncated
}
func (s *SafeCollabRecordsStore) withoutShared(r CollabRecordBatchResult, b CollabRecordBatch) CollabRecordBatchResult {
	if s.shared != nil {
		collabDropUnfreshIssues(&r)
		r.RecordsComplete = false
		r.DispositionsComplete = false
		r.TreeLocalEndingsKnown = false
		if b.Dispositions {
			r.TreeLocalEndings = CollabTreeLocalEndingLimit
		}
	}
	return r
}
func collabDropUnfreshIssues(result *CollabRecordBatchResult) {
	for query, records := range result.Records {
		var tombstones []CollabIssuedEntry
		for _, entry := range records.IssuedTokens {
			if entry.Record.Status == CollabIssuedStatusVoid {
				tombstones = append(tombstones, entry)
			}
		}
		records.IssuedTokens = tombstones
		records.Issued = collabAggregateIssues(tombstones)
		result.Records[query] = records
	}
	result.RecordsComplete = false
}
