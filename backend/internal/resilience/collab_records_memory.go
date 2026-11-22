package resilience

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// CollabRecordStorageIdentity is shared by the local and Redis implementations.
// All variable parts are already keyed digests, never raw request identifiers.
func CollabRecordStorageIdentity(m CollabRecordMutation) (scope CollabRecordScope, field string, expires time.Time) {
	switch m.Kind {
	case CollabRecordMutationPending:
		return m.Issued.Scope, "i:" + string(m.Token.Token), m.Issued.ExpiresAt
	case CollabRecordMutationConfirm, CollabRecordMutationVoid:
		return m.Token.Scope, "i:" + string(m.Token.Token), time.Time{}
	case CollabRecordMutationPlaintext:
		return m.Plaintext.Scope, "p:" + string(m.Plaintext.Call) + ":" + string(m.Plaintext.Arguments), m.Plaintext.ExpiresAt
	case CollabRecordMutationConsumed:
		return m.Consumed.Key.Scope, collabDeliveryField("c", m.Consumed.Key, m.Consumed.Account), m.Consumed.ExpiresAt
	case CollabRecordMutationReplaced:
		return m.Replaced.Key.Scope, collabDeliveryField("r", m.Replaced.Key, m.Replaced.Account), m.Replaced.ExpiresAt
	case CollabRecordMutationTerminated:
		return m.Terminated.Key.Scope, collabDeliveryField("t", m.Terminated.Key, ""), m.Terminated.ExpiresAt
	case CollabRecordMutationTerminalRejected:
		r := m.TerminalRejection
		return r.Key.Scope, collabDeliveryField("j", r.Key, r.Rejection) + ":" + string(r.Event), r.ExpiresAt
	case CollabRecordMutationLocalEnding:
		return m.Scope, "e:" + string(m.Event), m.Issued.ExpiresAt
	}
	return
}
func collabDeliveryField(kind string, k CollabDeliveryKey, extra CollabDigest) string {
	b, _ := json.Marshal([]CollabDigest{k.Thread, k.Item, k.Delivery, k.Digest, extra})
	return kind + ":" + string(b)
}
func validCollabScope(s CollabRecordScope) bool {
	return s.Tenant != "" && s.Group != "" && s.Tree != ""
}

type collabMemoryEntry struct {
	mutation CollabRecordMutation
	charge   int
	written  int64
}
type collabMemoryIndex struct {
	kind   string
	digest CollabDigest
	key    CollabDeliveryKey
}
type collabMemoryTree struct {
	mu             sync.RWMutex
	entries        map[string]collabMemoryEntry
	index          map[collabMemoryIndex]map[string]struct{}
	bytes          int
	expires        atomic.Int64
	protectedUntil atomic.Int64
	lastWritten    atomic.Int64
	active         atomic.Int64
	counter        *CollabTerminalRejectionCounter
}
type MemoryCollabRecordsStore struct {
	mu    sync.RWMutex
	trees map[CollabRecordScope]*collabMemoryTree
}

func NewMemoryCollabRecordsStore() *MemoryCollabRecordsStore {
	return &MemoryCollabRecordsStore{trees: make(map[CollabRecordScope]*collabMemoryTree)}
}

func (s *MemoryCollabRecordsStore) tree(scope CollabRecordScope, create bool, now time.Time) *collabMemoryTree {
	if create {
		tree := s.mutationTree(scope, true, now)
		if tree != nil {
			tree.active.Add(-1)
		}
		return tree
	}
	if !s.mu.TryRLock() {
		return nil
	}
	defer s.mu.RUnlock()
	return s.trees[scope]
}

// Mutation lookup waits for the short registry critical section. Active writers
// pin their tree until their mutation finishes; live dispositions are never evicted.
func (s *MemoryCollabRecordsStore) mutationTree(scope CollabRecordScope, create bool, now time.Time) *collabMemoryTree {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tree := s.trees[scope]; tree != nil {
		tree.active.Add(1)
		return tree
	}
	if !create {
		return nil
	}
	tenantCount := 0
	for key, tree := range s.trees {
		if tree.active.Load() == 0 && tree.expires.Load() <= now.UnixNano() {
			delete(s.trees, key)
			continue
		}
		if key.Tenant == scope.Tenant {
			tenantCount++
		}
	}
	tenantFull := tenantCount >= CollabRecordMaxTenantTrees
	if tenantFull || len(s.trees) >= CollabRecordMaxTrees {
		var oldest CollabRecordScope
		var victim *collabMemoryTree
		for key, tree := range s.trees {
			if (tenantFull && key.Tenant != scope.Tenant) || tree.active.Load() != 0 || tree.protectedUntil.Load() > now.UnixNano() {
				continue
			}
			if victim == nil || tree.lastWritten.Load() < victim.lastWritten.Load() {
				oldest = key
				victim = tree
			}
		}
		if victim == nil {
			return nil
		}
		delete(s.trees, oldest)
	}
	tree := &collabMemoryTree{entries: make(map[string]collabMemoryEntry), index: make(map[collabMemoryIndex]map[string]struct{}), counter: NewCollabTerminalRejectionCounter(CollabRecordMaxTreeEntries)}
	tree.active.Store(1)
	tree.lastWritten.Store(now.UnixNano())
	s.trees[scope] = tree
	return tree
}
func collabMutationIndexes(m CollabRecordMutation) []collabMemoryIndex {
	switch m.Kind {
	case CollabRecordMutationPending:
		return []collabMemoryIndex{{kind: "call", digest: m.Issued.Call}, {kind: "cipher", digest: m.Issued.Digest}}
	case CollabRecordMutationPlaintext:
		return []collabMemoryIndex{{kind: "plain", digest: m.Plaintext.Call}}
	case CollabRecordMutationConsumed:
		return []collabMemoryIndex{{kind: "delivery", key: m.Consumed.Key}}
	case CollabRecordMutationReplaced:
		return []collabMemoryIndex{{kind: "delivery", key: m.Replaced.Key}}
	case CollabRecordMutationTerminated:
		r := m.Terminated
		indexes := []collabMemoryIndex{{kind: "delivery", key: r.Key}, {kind: "event", digest: r.Event}}
		for _, copy := range r.Copies {
			k := r.Key
			k.Item = copy
			k.Digest = ""
			indexes = append(indexes, collabMemoryIndex{kind: "copy", key: k})
		}
		return indexes
	case CollabRecordMutationLocalEnding:
		return []collabMemoryIndex{{kind: "ending"}}
	default:
		return nil
	}
}
func (t *collabMemoryTree) remove(field string) {
	old := t.entries[field]
	t.bytes -= old.charge
	for _, key := range collabMutationIndexes(old.mutation) {
		delete(t.index[key], field)
		if len(t.index[key]) == 0 {
			delete(t.index, key)
		}
	}
	delete(t.entries, field)
}

// ApplyMutation also reconstructs a normalized shared snapshot. No network or
// caller callback runs under either lock; expensive normalization precedes locks.
func (s *MemoryCollabRecordsStore) ApplyMutation(m CollabRecordMutation) bool {
	now := time.Now()
	m, payload, ok := NormalizeCollabRecordMutation(m, now)
	if !ok {
		return false
	}
	scope, field, expires := CollabRecordStorageIdentity(m)
	if !validCollabScope(scope) || field == "" {
		return false
	}
	transition := m.Kind == CollabRecordMutationConfirm || m.Kind == CollabRecordMutationVoid
	if !transition && !expires.After(now) {
		return false
	}
	switch m.Kind {
	case CollabRecordMutationPending:
		if m.Token.Token == "" || m.Issued.Call == "" || m.Issued.Digest == "" || m.Issued.Account == "" {
			return false
		}
		switch m.Issued.Status {
		case "":
			m.Issued.Status = CollabIssuedStatusPending
		case CollabIssuedStatusPending, CollabIssuedStatusConfirmed, CollabIssuedStatusVoid:
		default:
			return false
		}
	case CollabRecordMutationPlaintext:
		if m.Plaintext.Call == "" || m.Plaintext.Arguments == "" {
			return false
		}
	case CollabRecordMutationConsumed:
		if !validCollabDelivery(m.Consumed.Key) || m.Consumed.Account == "" {
			return false
		}
	case CollabRecordMutationReplaced:
		if !validCollabDelivery(m.Replaced.Key) || m.Replaced.Account == "" {
			return false
		}
	case CollabRecordMutationTerminated:
		if !validCollabDelivery(m.Terminated.Key) || m.Terminated.Event == "" {
			return false
		}
	case CollabRecordMutationTerminalRejected:
		if !validCollabDelivery(m.TerminalRejection.Key) || m.TerminalRejection.Event == "" || m.TerminalRejection.Rejection == "" {
			return false
		}
	case CollabRecordMutationLocalEnding:
		if m.Event == "" {
			return false
		}
	}
	tree := s.mutationTree(scope, !transition, now)
	if tree == nil {
		return false
	}
	tree.mu.Lock()
	defer func() { tree.mu.Unlock(); tree.active.Add(-1) }()
	if transition {
		old, exists := tree.entries[field]
		if !exists || old.mutation.Token != m.Token || !old.mutation.Issued.ExpiresAt.After(now) {
			return false
		}
		if m.Kind == CollabRecordMutationVoid {
			old.mutation.Issued.Status = CollabIssuedStatusVoid
		} else if old.mutation.Issued.Status != CollabIssuedStatusVoid {
			old.mutation.Issued.Status = CollabIssuedStatusConfirmed
		} else {
			return false
		}
		tree.entries[field] = old
		return true
	}
	if old, exists := tree.entries[field]; exists {
		if collabRecordExpiry(old.mutation).After(now) {
			return true
		}
		tree.remove(field)
	}

	charge := collabRecordCharge(field, payload)
	part := collabRecordPartition(m.Kind)
	for f, old := range tree.entries {
		if !collabRecordExpiry(old.mutation).After(now) {
			tree.remove(f)
		}
	}
	count, used := tree.partitionSize(part)
	limit := CollabRecordMaxTreeEntries
	if part == 2 {
		limit = CollabTreeLocalEndingLimit
	}
	if part == 0 {
		for count >= limit || used+charge > collabPartitionBytes(part) {
			oldest := ""
			var stamp int64
			for f, old := range tree.entries {
				if collabRecordPartition(old.mutation.Kind) == 0 && (oldest == "" || old.written < stamp) {
					oldest = f
					stamp = old.written
				}
			}
			if oldest == "" {
				break
			}
			tree.remove(oldest)
			count, used = tree.partitionSize(part)
		}
	}
	if count >= limit || used+charge > collabPartitionBytes(part) {
		return false
	}
	if m.Kind == CollabRecordMutationTerminalRejected {
		if _, ok := tree.counter.Record(m.TerminalRejection, now); !ok {
			return false
		}
	}
	tree.entries[field] = collabMemoryEntry{mutation: m, charge: charge, written: now.UnixNano()}
	tree.bytes += charge
	for _, key := range collabMutationIndexes(m) {
		if tree.index[key] == nil {
			tree.index[key] = make(map[string]struct{})
		}
		tree.index[key][field] = struct{}{}
	}
	tree.lastWritten.Store(now.UnixNano())
	if part != 0 && expires.UnixNano() > tree.protectedUntil.Load() {
		tree.protectedUntil.Store(expires.UnixNano())
	}
	if expires.UnixNano() > tree.expires.Load() {
		tree.expires.Store(expires.UnixNano())
	}
	return true
}
func validCollabDelivery(k CollabDeliveryKey) bool {
	return validCollabScope(k.Scope) && k.Thread != "" && k.Item != "" && k.Delivery != "" && k.Digest != ""
}
func (s *MemoryCollabRecordsStore) PendingIssued(_ context.Context, r CollabIssuedRecord) (CollabIssuedToken, bool) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return CollabIssuedToken{}, false
	}
	token := CollabIssuedToken{Token: CollabDigest(hex.EncodeToString(random[:])), Scope: r.Scope, Call: r.Call, Generation: r.Generation}
	r.Status = CollabIssuedStatusPending
	return token, s.ApplyMutation(CollabRecordMutation{Kind: CollabRecordMutationPending, Token: token, Issued: r})
}
func (s *MemoryCollabRecordsStore) ConfirmIssued(_ context.Context, t CollabIssuedToken) bool {
	return s.ApplyMutation(CollabRecordMutation{Kind: CollabRecordMutationConfirm, Token: t})
}
func (s *MemoryCollabRecordsStore) VoidIssued(_ context.Context, t CollabIssuedToken) {
	s.ApplyMutation(CollabRecordMutation{Kind: CollabRecordMutationVoid, Token: t})
}
func (s *MemoryCollabRecordsStore) PlaintextDelivered(_ context.Context, r CollabPlaintextRecord) bool {
	return s.ApplyMutation(CollabRecordMutation{Kind: CollabRecordMutationPlaintext, Plaintext: r})
}
func (s *MemoryCollabRecordsStore) Consumed(_ context.Context, r CollabConsumedRecord) bool {
	return s.ApplyMutation(CollabRecordMutation{Kind: CollabRecordMutationConsumed, Consumed: r})
}
func (s *MemoryCollabRecordsStore) Replaced(_ context.Context, r CollabReplacedRecord) bool {
	return s.ApplyMutation(CollabRecordMutation{Kind: CollabRecordMutationReplaced, Replaced: r})
}
func (s *MemoryCollabRecordsStore) Terminated(_ context.Context, r CollabTerminatedRecord) bool {
	return s.ApplyMutation(CollabRecordMutation{Kind: CollabRecordMutationTerminated, Terminated: r})
}
func (s *MemoryCollabRecordsStore) TerminalRejected(_ context.Context, r CollabTerminalRejectionRecord) (int, bool) {
	if !s.ApplyMutation(CollabRecordMutation{Kind: CollabRecordMutationTerminalRejected, TerminalRejection: r}) {
		return 0, false
	}
	tree := s.tree(r.Key.Scope, false, time.Now())
	if tree == nil {
		return 0, false
	}
	for _, c := range tree.counter.Counts(r.Key, time.Now()) {
		if c.Rejection == r.Rejection {
			return c.Count, true
		}
	}
	return 0, false
}

func (t *collabMemoryTree) partitionSize(part int) (count, bytes int) {
	for _, entry := range t.entries {
		if collabRecordPartition(entry.mutation.Kind) == part {
			count++
			bytes += entry.charge
		}
	}
	return
}
func (s *MemoryCollabRecordsStore) IncrementTreeLocalEnding(ctx context.Context, scope CollabRecordScope, event CollabDigest) (int, bool) {
	return s.ReserveTreeLocalEnding(ctx, scope, event)
}
func (s *MemoryCollabRecordsStore) ReserveTreeLocalEnding(ctx context.Context, scope CollabRecordScope, event CollabDigest) (int, bool) {
	if ctx.Err() != nil || !validCollabScope(scope) || event == "" {
		return CollabTreeLocalEndingLimit, false
	}
	if !s.ApplyMutation(CollabRecordMutation{Kind: CollabRecordMutationLocalEnding, Scope: scope, Event: event}) {
		return CollabTreeLocalEndingLimit, false
	}
	// The mutation is atomic and the successful write proves this count locally.
	tree := s.mutationTree(scope, false, time.Now())
	if tree == nil {
		return CollabTreeLocalEndingLimit, false
	}
	tree.mu.RLock()
	count, _ := tree.partitionSize(2)
	tree.mu.RUnlock()
	tree.active.Add(-1)
	return count, true
}

func (s *MemoryCollabRecordsStore) LookupBatch(ctx context.Context, b CollabRecordBatch) CollabRecordBatchResult {
	result := collabUnknownBatch(b)
	if ctx.Err() != nil || (!b.Records && !b.Dispositions) || !validCollabScope(b.Scope) {
		return result
	}
	b = collabBoundBatch(b)
	if !s.mu.TryRLock() {
		return result
	}
	tree := s.trees[b.Scope]
	capacityKnown := true
	if tree == nil {
		tenantCount := 0
		tenantVictim := false
		globalVictim := false
		now := time.Now().UnixNano()
		for scope, candidate := range s.trees {
			evictable := candidate.active.Load() == 0 && (candidate.expires.Load() <= now || candidate.protectedUntil.Load() <= now)
			if evictable {
				globalVictim = true
			}
			if scope.Tenant == b.Scope.Tenant {
				tenantCount++
				if evictable {
					tenantVictim = true
				}
			}
		}
		capacityKnown = !(tenantCount >= CollabRecordMaxTenantTrees && !tenantVictim) && !(len(s.trees) >= CollabRecordMaxTrees && !globalVictim)
	}
	s.mu.RUnlock()
	if !capacityKnown {
		return result
	}
	result.RecordsComplete = b.Records && !b.recordsTruncated
	result.DispositionsComplete = b.Dispositions && !b.dispositionsTruncated
	if b.Dispositions {
		if tree == nil {
			result.TreeLocalEndingsKnown = true
			result.TreeLocalEndings = 0
		} else if tree.mu.TryRLock() {
			result.TreeLocalEndingsKnown = true
			result.TreeLocalEndings = 0
			for field := range tree.index[collabMemoryIndex{kind: "ending"}] {
				if tree.entries[field].mutation.Issued.ExpiresAt.After(time.Now()) {
					result.TreeLocalEndings++
				}
			}
			tree.mu.RUnlock()
		} else {
			result.DispositionsComplete = false
		}
	}
	// Dispositions are always served first and never spend the evidence allowance.
	for _, q := range b.DispositionQueries {
		if !b.Dispositions {
			break
		}
		if ctx.Err() != nil || q.Key.Scope != b.Scope {
			result.DispositionsComplete = false
			continue
		}
		var rows []CollabRecordMutation
		complete := true
		if tree != nil {
			rows, complete = tree.query(ctx, q, false, true)
		}
		if !complete {
			result.DispositionsComplete = false
			continue
		}
		var d CollabDispositionLookup
		for _, m := range rows {
			switch m.Kind {
			case CollabRecordMutationReplaced:
				if q.Account == "" || q.Account == m.Replaced.Account {
					d.Replaced = append(d.Replaced, m.Replaced)
				}
			case CollabRecordMutationTerminated:
				v := m.Terminated
				v.Copies = slices.Clone(v.Copies)
				d.Terminated = append(d.Terminated, v)
			}
		}
		if tree != nil {
			d.TerminalRejections = tree.counter.Counts(q.Key, time.Now())
		}
		if len(d.Replaced)+len(d.Terminated)+len(d.TerminalRejections) > 0 {
			if result.Dispositions == nil {
				result.Dispositions = make(map[CollabRecordQuery]CollabDispositionLookup)
			}
			result.Dispositions[q] = d
		}
	}
	for _, q := range b.Queries {
		if !b.Records {
			break
		}
		if ctx.Err() != nil || q.Key.Scope != b.Scope {
			result.RecordsComplete = false
			continue
		}
		var rows []CollabRecordMutation
		complete := true
		if tree != nil {
			rows, complete = tree.query(ctx, q, true, false)
		}
		if !complete {
			result.RecordsComplete = false
			continue
		}
		var r CollabRecordLookup
		for _, m := range rows {
			switch m.Kind {
			case CollabRecordMutationPending:
				v := m.Issued
				if q.Account == "" || q.Account == v.Account {
					r.IssuedTokens = append(r.IssuedTokens, CollabIssuedEntry{Token: m.Token, Record: v})
				}
			case CollabRecordMutationPlaintext:
				if q.Arguments == "" || q.Arguments == m.Plaintext.Arguments {
					r.Plaintext = append(r.Plaintext, m.Plaintext)
				}
			case CollabRecordMutationConsumed:
				if q.Account == "" || q.Account == m.Consumed.Account {
					r.Consumed = append(r.Consumed, m.Consumed)
				}
			}
		}
		r.Issued = collabAggregateIssues(r.IssuedTokens)
		if result.Records == nil {
			result.Records = make(map[CollabRecordQuery]CollabRecordLookup)
		}
		result.Records[q] = r
	}
	if ctx.Err() != nil {
		result.RecordsComplete = false
		result.DispositionsComplete = false
		result.TreeLocalEndingsKnown = false
		result.TreeLocalEndings = result.LocalEndingCount()
	}
	return result
}
func (t *collabMemoryTree) query(ctx context.Context, q CollabRecordQuery, records, dispositions bool) ([]CollabRecordMutation, bool) {
	if ctx.Err() != nil || !t.mu.TryRLock() {
		return nil, false
	}
	defer t.mu.RUnlock()
	var indexes []collabMemoryIndex
	if records {
		if q.Call != "" {
			indexes = append(indexes, collabMemoryIndex{kind: "call", digest: q.Call}, collabMemoryIndex{kind: "plain", digest: q.Call})
		}
		if q.Key.Digest != "" {
			indexes = append(indexes, collabMemoryIndex{kind: "cipher", digest: q.Key.Digest})
		}
	}
	if records || dispositions {
		indexes = append(indexes, collabMemoryIndex{kind: "delivery", key: q.Key})
	}
	if dispositions {
		if q.Event != "" {
			indexes = append(indexes, collabMemoryIndex{kind: "event", digest: q.Event})
		}
		copyKey := q.Key
		copyKey.Digest = ""
		indexes = append(indexes, collabMemoryIndex{kind: "copy", key: copyKey})
	}
	seen := make(map[string]bool)
	var rows []CollabRecordMutation
	now := time.Now()
	for _, index := range indexes {
		for field := range t.index[index] {
			if ctx.Err() != nil {
				return rows, false
			}
			m := t.entries[field].mutation
			if !seen[field] && collabRecordExpiry(m).After(now) {
				seen[field] = true
				rows = append(rows, m)
			}
		}
	}
	return rows, true
}

func collabRecordExpiry(m CollabRecordMutation) time.Time {
	switch m.Kind {
	case CollabRecordMutationPending, CollabRecordMutationLocalEnding:
		return m.Issued.ExpiresAt
	case CollabRecordMutationPlaintext:
		return m.Plaintext.ExpiresAt
	case CollabRecordMutationConsumed:
		return m.Consumed.ExpiresAt
	case CollabRecordMutationReplaced:
		return m.Replaced.ExpiresAt
	case CollabRecordMutationTerminated:
		return m.Terminated.ExpiresAt
	case CollabRecordMutationTerminalRejected:
		return m.TerminalRejection.ExpiresAt
	default:
		return time.Time{}
	}
}
