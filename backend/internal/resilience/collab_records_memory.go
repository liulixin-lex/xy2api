package resilience

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sync"
	"time"
)

// Each tree has its own small bound; the product bounds the entire store.
const CollabRecordMaxTrees = 256
const CollabRecordMaxTreeEntries = CollabRecordMaxEntries / CollabRecordMaxTrees

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

type collabMemoryTree struct {
	entries map[string]CollabRecordMutation
	expires time.Time
}
type MemoryCollabRecordsStore struct {
	mu      sync.Mutex
	trees   map[CollabRecordScope]*collabMemoryTree
	counter *CollabTerminalRejectionCounter
}

func NewMemoryCollabRecordsStore() *MemoryCollabRecordsStore {
	return &MemoryCollabRecordsStore{trees: make(map[CollabRecordScope]*collabMemoryTree), counter: NewCollabTerminalRejectionCounter(CollabRecordMaxEntries)}
}

// ApplyMutation is also used to decode a bounded shared snapshot. Confirm never
// creates an issue; a void is final for that token, including replayed pending.
func (s *MemoryCollabRecordsStore) ApplyMutation(m CollabRecordMutation) bool {
	scope, field, expires := CollabRecordStorageIdentity(m)
	now := time.Now()
	if !validCollabScope(scope) || field == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tree := s.trees[scope]
	if tree != nil && !tree.expires.After(now) {
		delete(s.trees, scope)
		tree = nil
	}
	if m.Kind == CollabRecordMutationConfirm || m.Kind == CollabRecordMutationVoid {
		if tree == nil {
			return false
		}
		old, ok := tree.entries[field]
		if !ok || old.Token != m.Token || !old.Issued.ExpiresAt.After(now) {
			return false
		}
		if m.Kind == CollabRecordMutationVoid {
			old.Issued.Status = CollabIssuedStatusVoid
		} else if old.Issued.Status != CollabIssuedStatusVoid {
			old.Issued.Status = CollabIssuedStatusConfirmed
		} else {
			return false
		}
		tree.entries[field] = old
		return true
	}
	if m.Kind == CollabRecordMutationLocalEnding {
		if m.Event == "" {
			return false
		}
		// The mutation carries its expiry in Issued for the fixed wire contract.
		if expires.IsZero() {
			expires = now.Add(CollabRecordTTL)
		}
		m.Issued.ExpiresAt = expires
	}
	if !expires.After(now) {
		return false
	}
	expires = collabEarlierTime(expires, now.Add(CollabRecordTTL))
	switch m.Kind {
	case CollabRecordMutationPending:
		if m.Token.Token == "" || m.Token.Scope != scope || m.Token.Call != m.Issued.Call || m.Token.Generation != m.Issued.Generation || m.Issued.Call == "" || m.Issued.Digest == "" || m.Issued.Account == "" {
			return false
		}
		m.Issued.ExpiresAt = expires
	case CollabRecordMutationPlaintext:
		m.Plaintext.ExpiresAt = expires
		if m.Plaintext.Call == "" || m.Plaintext.Arguments == "" {
			return false
		}
	case CollabRecordMutationConsumed:
		m.Consumed.ExpiresAt = expires
		if !validCollabDelivery(m.Consumed.Key) || m.Consumed.Account == "" {
			return false
		}
	case CollabRecordMutationReplaced:
		m.Replaced.ExpiresAt = expires
		if !validCollabDelivery(m.Replaced.Key) || m.Replaced.Account == "" {
			return false
		}
	case CollabRecordMutationTerminated:
		m.Terminated.ExpiresAt = expires
		if !validCollabDelivery(m.Terminated.Key) || m.Terminated.Event == "" {
			return false
		}
		m.Terminated.Copies = slices.Clone(m.Terminated.Copies[:min(len(m.Terminated.Copies), CollabRecordMaxTreeEntries)])
	case CollabRecordMutationTerminalRejected:
		if !validCollabDelivery(m.TerminalRejection.Key) {
			return false
		}
		m.TerminalRejection.ExpiresAt = expires
	}
	if tree == nil {
		if len(s.trees) >= CollabRecordMaxTrees {
			for k, v := range s.trees {
				if !v.expires.After(now) {
					delete(s.trees, k)
				}
			}
		}
		if len(s.trees) >= CollabRecordMaxTrees {
			return false
		}
		tree = &collabMemoryTree{entries: make(map[string]CollabRecordMutation)}
		s.trees[scope] = tree
	}
	if old, ok := tree.entries[field]; ok {
		oldExpiry := collabRecordExpiry(old)
		if oldExpiry.After(now) {
			return true
		}
		delete(tree.entries, field)
	}
	if len(tree.entries) >= CollabRecordMaxTreeEntries {
		for k, v := range tree.entries {
			exp := collabRecordExpiry(v)
			if !exp.After(now) {
				delete(tree.entries, k)
			}
		}
	}
	if len(tree.entries) >= CollabRecordMaxTreeEntries {
		return false
	}
	if m.Kind == CollabRecordMutationTerminalRejected {
		if _, ok := s.counter.Record(m.TerminalRejection, now); !ok {
			return false
		}
	}
	tree.entries[field] = m
	if expires.After(tree.expires) {
		tree.expires = expires
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
	for _, c := range s.counter.Counts(r.Key, time.Now()) {
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
func (s *MemoryCollabRecordsStore) LookupBatch(_ context.Context, b CollabRecordBatch) CollabRecordBatchResult {
	result := CollabRecordBatchResult{}
	if !b.Records && !b.Dispositions {
		return result
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	tree := s.trees[b.Scope]
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
		if q.Key.Scope != b.Scope {
			continue
		}
		var r CollabRecordLookup
		var d CollabDispositionLookup
		for _, m := range tree.entries {
			expires := collabRecordExpiry(m)
			if !expires.After(now) {
				continue
			}
			if b.Records {
				switch m.Kind {
				case CollabRecordMutationPending:
					v := m.Issued
					if (q.Call != "" && q.Call == v.Call || q.Key.Digest != "" && q.Key.Digest == v.Digest) && (q.Account == "" || q.Account == v.Account) {
						r.Issued = append(r.Issued, v)
					}
				case CollabRecordMutationPlaintext:
					v := m.Plaintext
					if q.Call == v.Call && (q.Arguments == "" || q.Arguments == v.Arguments) {
						r.Plaintext = append(r.Plaintext, v)
					}
				case CollabRecordMutationConsumed:
					v := m.Consumed
					if q.Key == v.Key && (q.Account == "" || q.Account == v.Account) {
						r.Consumed = append(r.Consumed, v)
					}
				}
			}
			if b.Dispositions {
				switch m.Kind {
				case CollabRecordMutationReplaced:
					v := m.Replaced
					if q.Key == v.Key && (q.Account == "" || q.Account == v.Account) {
						d.Replaced = append(d.Replaced, v)
					}
				case CollabRecordMutationTerminated:
					v := m.Terminated
					if q.Key == v.Key || (q.Event != "" && q.Event == v.Event) || (q.Key.Thread == v.Key.Thread && q.Key.Delivery == v.Key.Delivery && slices.Contains(v.Copies, q.Key.Item)) {
						v.Copies = slices.Clone(v.Copies)
						d.Terminated = append(d.Terminated, v)
					}
				}
			}
		}
		if b.Records {
			// Concurrent tokens for one call are independent. An active token
			// survives another token's void; emit a tombstone only if all are void.
			issues := make(map[collabIssueIdentity]CollabIssuedRecord)
			for _, issue := range r.Issued {
				key := collabIssueID(issue)
				old, exists := issues[key]
				if !exists || issue.Status == CollabIssuedStatusConfirmed || old.Status == CollabIssuedStatusVoid {
					issues[key] = issue
				}
			}
			r.Issued = nil
			for _, issue := range issues {
				r.Issued = append(r.Issued, issue)
			}
			result.Records[q] = r
		}
		if b.Dispositions {
			d.TerminalRejections = s.counter.Counts(q.Key, now)
			result.Dispositions[q] = d
		}
	}
	if b.Dispositions {
		for _, m := range tree.entries {
			if m.Kind == CollabRecordMutationLocalEnding && m.Issued.ExpiresAt.After(now) {
				result.TreeLocalEndings++
			}
		}
		result.TreeLocalEndings = min(result.TreeLocalEndings, CollabTreeLocalEndingLimit)
	}
	return result
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
