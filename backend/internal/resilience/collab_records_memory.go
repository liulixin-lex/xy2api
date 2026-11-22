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
}
type collabMemoryIndex struct {
	kind   string
	digest CollabDigest
	key    CollabDeliveryKey
}
type collabMemoryTree struct {
	mu      sync.RWMutex
	entries map[string]collabMemoryEntry
	index   map[collabMemoryIndex]map[string]struct{}
	bytes   int
	expires atomic.Int64
	counter *CollabTerminalRejectionCounter
}
type MemoryCollabRecordsStore struct {
	mu    sync.RWMutex
	trees map[CollabRecordScope]*collabMemoryTree
}

func NewMemoryCollabRecordsStore() *MemoryCollabRecordsStore {
	return &MemoryCollabRecordsStore{trees: make(map[CollabRecordScope]*collabMemoryTree)}
}
func (s *MemoryCollabRecordsStore) tree(scope CollabRecordScope, create bool, now time.Time) *collabMemoryTree {
	if !create {
		if !s.mu.TryRLock() {
			return nil
		}
		tree := s.trees[scope]
		s.mu.RUnlock()
		return tree
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tree := s.trees[scope]; tree != nil && tree.expires.Load() > now.UnixNano() {
		return tree
	}
	delete(s.trees, scope)
	if len(s.trees) >= CollabRecordMaxTrees {
		for k, t := range s.trees {
			if t.expires.Load() <= now.UnixNano() {
				delete(s.trees, k)
			}
		}
	}
	if len(s.trees) >= CollabRecordMaxTrees {
		return nil
	}
	tree := &collabMemoryTree{entries: make(map[string]collabMemoryEntry), index: make(map[collabMemoryIndex]map[string]struct{}), counter: NewCollabTerminalRejectionCounter(CollabRecordMaxTreeEntries)}
	tree.expires.Store(now.Add(CollabRecordTTL).UnixNano())
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
	tree := s.tree(scope, !transition, now)
	if tree == nil {
		return false
	}
	tree.mu.Lock()
	defer tree.mu.Unlock()
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
	if len(tree.entries) >= CollabRecordMaxTreeEntries || tree.bytes+charge > CollabRecordMaxTreeBytes {
		for f, old := range tree.entries {
			if !collabRecordExpiry(old.mutation).After(now) {
				tree.remove(f)
			}
		}
	}
	if len(tree.entries) >= CollabRecordMaxTreeEntries || tree.bytes+charge > CollabRecordMaxTreeBytes {
		return false
	}
	if m.Kind == CollabRecordMutationTerminalRejected {
		if _, ok := tree.counter.Record(m.TerminalRejection, now); !ok {
			return false
		}
	}
	tree.entries[field] = collabMemoryEntry{mutation: m, charge: charge}
	tree.bytes += charge
	for _, key := range collabMutationIndexes(m) {
		if tree.index[key] == nil {
			tree.index[key] = make(map[string]struct{})
		}
		tree.index[key][field] = struct{}{}
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
func (s *MemoryCollabRecordsStore) IncrementTreeLocalEnding(ctx context.Context, scope CollabRecordScope, event CollabDigest) (int, bool) {
	ok := s.ApplyMutation(CollabRecordMutation{Kind: CollabRecordMutationLocalEnding, Scope: scope, Event: event})
	return s.LookupBatch(ctx, CollabRecordBatch{Scope: scope, Dispositions: true}).TreeLocalEndings, ok
}

func (s *MemoryCollabRecordsStore) LookupBatch(ctx context.Context, b CollabRecordBatch) CollabRecordBatchResult {
	result := CollabRecordBatchResult{}
	if ctx.Err() != nil || (!b.Records && !b.Dispositions) {
		return result
	}
	b = collabBoundBatch(b)
	tree := s.tree(b.Scope, false, time.Now())
	if tree == nil {
		return result
	}
	if b.Records {
		result.Records = make(map[CollabRecordQuery]CollabRecordLookup)
	}
	if b.Dispositions {
		result.Dispositions = make(map[CollabRecordQuery]CollabDispositionLookup)
	}
	for _, q := range b.Queries {
		if ctx.Err() != nil {
			break
		}
		if q.Key.Scope != b.Scope {
			continue
		}
		// A contended optional lookup is absence; it must never wait behind writes.
		rows := tree.query(ctx, q, b.Records, b.Dispositions)
		var r CollabRecordLookup
		var d CollabDispositionLookup
		for _, m := range rows {
			switch m.Kind {
			case CollabRecordMutationPending:
				v := m.Issued
				if b.Records && (q.Account == "" || q.Account == v.Account) {
					r.IssuedTokens = append(r.IssuedTokens, CollabIssuedEntry{Token: m.Token, Record: v})
				}
			case CollabRecordMutationPlaintext:
				if b.Records && (q.Arguments == "" || q.Arguments == m.Plaintext.Arguments) {
					r.Plaintext = append(r.Plaintext, m.Plaintext)
				}
			case CollabRecordMutationConsumed:
				if b.Records && (q.Account == "" || q.Account == m.Consumed.Account) {
					r.Consumed = append(r.Consumed, m.Consumed)
				}
			case CollabRecordMutationReplaced:
				if b.Dispositions && (q.Account == "" || q.Account == m.Replaced.Account) {
					d.Replaced = append(d.Replaced, m.Replaced)
				}
			case CollabRecordMutationTerminated:
				if b.Dispositions {
					v := m.Terminated
					v.Copies = slices.Clone(v.Copies)
					d.Terminated = append(d.Terminated, v)
				}
			}
		}
		if b.Records {
			r.Issued = collabAggregateIssues(r.IssuedTokens)
			result.Records[q] = r
		}
		if b.Dispositions && ctx.Err() == nil {
			d.TerminalRejections = tree.counter.Counts(q.Key, time.Now())
			result.Dispositions[q] = d
		}
	}
	if b.Dispositions && ctx.Err() == nil && tree.mu.TryRLock() {
		now := time.Now()
		for field := range tree.index[collabMemoryIndex{kind: "ending"}] {
			if tree.entries[field].mutation.Issued.ExpiresAt.After(now) {
				result.TreeLocalEndings++
			}
		}
		tree.mu.RUnlock()
		result.TreeLocalEndings = min(result.TreeLocalEndings, CollabTreeLocalEndingLimit)
	}
	return result
}
func (t *collabMemoryTree) query(ctx context.Context, q CollabRecordQuery, records, dispositions bool) []CollabRecordMutation {
	if ctx.Err() != nil || !t.mu.TryRLock() {
		return nil
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
				return rows
			}
			m := t.entries[field].mutation
			if !seen[field] && collabRecordExpiry(m).After(now) {
				seen[field] = true
				rows = append(rows, m)
			}
		}
	}
	return rows
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
