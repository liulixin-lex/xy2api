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

// Evidence and dispositions persist asynchronously. Only pre-output ending
// admission waits for shared storage. Close flushes invalidations on shutdown.
// Construct once per service, never per request. Close at service shutdown.
type SafeCollabRecordsStore struct {
	local         CollabRecordsStore
	shared        CollabSharedRecordsStore
	writes        chan CollabRecordMutation
	publicationMu sync.Mutex
	publications  map[collabPublicationKey]*collabPublication
	readers       chan struct{}
	stop          context.Context
	cancel        context.CancelFunc
	once          sync.Once
	deferredMu    sync.Mutex
	deferred      map[collabPublicationKey]CollabRecordMutation
	invalidations map[CollabRecordScope][]CollabRecordMutation
	wake          chan struct{}
}

func NewSafeCollabRecordsStore(local CollabRecordsStore, shared CollabSharedRecordsStore, capacity int) *SafeCollabRecordsStore {
	if local == nil {
		local = NoopCollabRecordsStore{}
	}
	stop, cancel := context.WithCancel(context.Background())
	s := &SafeCollabRecordsStore{publications: make(map[collabPublicationKey]*collabPublication), deferred: make(map[collabPublicationKey]CollabRecordMutation), invalidations: make(map[CollabRecordScope][]CollabRecordMutation), wake: make(chan struct{}, 1), local: local, shared: shared, writes: make(chan CollabRecordMutation, min(max(capacity, 1), 1024)), readers: make(chan struct{}, 64), stop: stop, cancel: cancel}
	if shared != nil {
		go s.runWrites()
		go s.runDeferred()
	}
	return s
}
func (s *SafeCollabRecordsStore) Close() {
	s.once.Do(func() {
		s.cancel()
		if s.shared == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), CollabRecordTimeout)
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer collabRecover(&collabSafety.recordPanics)
			defer close(done)
			s.flushInvalidations(ctx)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			collabSafety.recordErrors.Add(1)
		}
	})
}

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
	select {
	case queue <- normalized:
	default:
		collabSafety.recordDropped.Add(1)
	}
}

func (s *SafeCollabRecordsStore) runWrites() {
	defer collabRecover(&collabSafety.recordPanics)
	for {
		var first CollabRecordMutation
		select {
		case <-s.stop.Done():
			return
		case first = <-s.writes:
		}
		batch := []CollabRecordMutation{first}
	drain:
		for len(batch) < 64 {
			select {
			case next := <-s.writes:
				batch = append(batch, next)
			default:
				break drain
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
		defer collabRecover(&collabSafety.recordPanics)
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
	if ctx.Err() != nil || s.stop.Err() != nil || !s.local.Replaced(ctx, value) {
		return false
	}
	s.deferMutation(CollabRecordMutation{Kind: CollabRecordMutationReplaced, Replaced: value})
	return true
}

func (s *SafeCollabRecordsStore) Terminated(ctx context.Context, value CollabTerminatedRecord) (ok bool) {
	defer collabRecover(&collabSafety.recordPanics)
	value.CopiesTruncated = value.CopiesTruncated || len(value.Copies) > CollabRecordMaxCopies
	value.Copies = slices.Clone(value.Copies[:min(len(value.Copies), CollabRecordMaxCopies)])
	if ctx.Err() != nil || s.stop.Err() != nil || !s.local.Terminated(ctx, value) {
		return false
	}
	s.deferMutation(CollabRecordMutation{Kind: CollabRecordMutationTerminated, Terminated: value})
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
	s.deferInvalidation(CollabRecordMutation{Kind: CollabRecordMutationVoid, Token: token})
}

// Deprecated: only the explicit pre-delivery reservation APIs may add a count.
func (s *SafeCollabRecordsStore) IncrementTreeLocalEnding(context.Context, CollabRecordScope, CollabDigest) (int, bool) {
	return CollabTreeLocalEndingLimit, false
}
func (s *SafeCollabRecordsStore) ReserveTreeLocalEnding(ctx context.Context, scope CollabRecordScope, event CollabDigest) (count int, ok bool) {
	count = CollabTreeLocalEndingLimit
	defer collabRecover(&collabSafety.recordPanics)
	if ctx.Err() != nil || s.stop.Err() != nil {
		return count, false
	}
	local, supported := s.local.(CollabLocalEndingReserver)
	if !supported {
		return count, false
	}
	if s.shared == nil {
		return local.ReserveTreeLocalEnding(ctx, scope, event)
	}
	shared, supported := s.shared.(CollabLocalEndingReserver)
	if !supported {
		return count, false
	}
	count, ok = s.required(ctx, collabPublicationKey{scope: scope, field: "e:" + string(event)}, func(bounded context.Context) (int, bool) { return shared.ReserveTreeLocalEnding(bounded, scope, event) })
	if !ok {
		return CollabTreeLocalEndingLimit, false
	}
	localCount, ok := local.ReserveTreeLocalEnding(ctx, scope, event)
	if !ok {
		return CollabTreeLocalEndingLimit, false
	}
	return max(count, localCount), true
}
func (s *SafeCollabRecordsStore) ReserveLocalEnding(ctx context.Context, records []CollabTerminatedRecord) (count int, ok bool) {
	count = CollabTreeLocalEndingLimit
	defer collabRecover(&collabSafety.recordPanics)
	rows, valid := PrepareCollabLocalEnding(records, time.Now())
	if !valid || ctx.Err() != nil || s.stop.Err() != nil {
		return count, false
	}
	// Freeze caller-owned slices before asynchronous shared I/O.
	records = make([]CollabTerminatedRecord, 0, len(rows)-1)
	for _, m := range rows[1:] {
		records = append(records, m.Terminated)
	}
	local, supported := s.local.(CollabLocalEndingAdmission)
	if !supported {
		return count, false
	}
	if s.shared == nil {
		return local.ReserveLocalEnding(ctx, records)
	}
	shared, supported := s.shared.(CollabLocalEndingAdmission)
	if !supported {
		return count, false
	}
	// Include the complete normalized plan in coalescing identity.
	count, ok = s.required(ctx, collabAdmissionPublicationKey(rows), func(bounded context.Context) (int, bool) { return shared.ReserveLocalEnding(bounded, records) })
	if !ok {
		return CollabTreeLocalEndingLimit, false
	}
	localCount, ok := local.ReserveLocalEnding(ctx, records)
	if !ok {
		return CollabTreeLocalEndingLimit, false
	}
	return max(count, localCount), true
}
func (s *SafeCollabRecordsStore) TerminalRejected(ctx context.Context, record CollabTerminalRejectionRecord) (count int, ok bool) {
	defer collabRecover(&collabSafety.recordPanics)
	if ctx.Err() != nil || s.stop.Err() != nil {
		return 0, false
	}
	count, ok = s.local.TerminalRejected(ctx, record)
	if ok {
		s.deferMutation(CollabRecordMutation{Kind: CollabRecordMutationTerminalRejected, TerminalRejection: record})
	}
	return
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
