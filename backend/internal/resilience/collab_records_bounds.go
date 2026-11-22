package resilience

import (
	"encoding/json"
	"slices"
	"time"
)

// Encoded rows plus their two indexed field copies occupy at most 128 MiB
// across 4096 trees. Redis allocator/object overhead is additional; at most
// 262144 rows exist. These are shared-cache limits, not per tenant limits.
const (
	CollabRecordMaxTrees       = 4096
	CollabRecordMaxTreeEntries = 64
	CollabRecordMaxRowBytes    = 4096
	CollabRecordMaxTreeBytes   = 32 << 10
	CollabRecordMaxQueries     = 256
	CollabRecordMaxCopies      = 16
)

func collabDigestBound(d CollabDigest) bool { return len(d) <= 64 }
func collabScopeBound(s CollabRecordScope) bool {
	return collabDigestBound(s.Tenant) && collabDigestBound(s.Group) && collabDigestBound(s.Tree)
}
func collabKeyBound(k CollabDeliveryKey) bool {
	return collabScopeBound(k.Scope) && collabDigestBound(k.Thread) && collabDigestBound(k.Item) && collabDigestBound(k.Delivery) && collabDigestBound(k.Digest)
}

// NormalizeCollabRecordMutation removes unrelated fields, bounds every digest
// and collection before copying/encoding, and clamps the persisted expiry.
// Use the returned value at local admission, before enqueue, and at shared I/O.
func NormalizeCollabRecordMutation(m CollabRecordMutation, now time.Time) (CollabRecordMutation, []byte, bool) {
	n := CollabRecordMutation{Kind: m.Kind}
	switch m.Kind {
	case CollabRecordMutationPending:
		r := m.Issued
		if !collabScopeBound(r.Scope) || !collabDigestBound(r.Call) || !collabDigestBound(r.Digest) || !collabDigestBound(r.Account) || !collabDigestBound(r.Site) || !collabDigestBound(m.Token.Token) || m.Token.Scope != r.Scope || m.Token.Call != r.Call || m.Token.Generation != r.Generation {
			return n, nil, false
		}
		switch r.Status {
		case "", CollabIssuedStatusPending, CollabIssuedStatusConfirmed, CollabIssuedStatusVoid:
		default:
			return n, nil, false
		}
		n.Token = m.Token
		n.Issued = r
	case CollabRecordMutationConfirm, CollabRecordMutationVoid:
		if !collabScopeBound(m.Token.Scope) || !collabDigestBound(m.Token.Token) || !collabDigestBound(m.Token.Call) {
			return n, nil, false
		}
		n.Token = m.Token
	case CollabRecordMutationPlaintext:
		r := m.Plaintext
		if !collabScopeBound(r.Scope) || !collabDigestBound(r.Call) || !collabDigestBound(r.Arguments) {
			return n, nil, false
		}
		n.Plaintext = r
	case CollabRecordMutationConsumed:
		r := m.Consumed
		if !collabKeyBound(r.Key) || !collabDigestBound(r.Account) {
			return n, nil, false
		}
		n.Consumed = r
	case CollabRecordMutationReplaced:
		r := m.Replaced
		if !collabKeyBound(r.Key) || !collabDigestBound(r.Account) {
			return n, nil, false
		}
		switch r.Placeholder {
		case CollabPlaceholderUnreadTask, CollabPlaceholderProcessedTask, CollabPlaceholderMessage, CollabPlaceholderToolOutput, CollabPlaceholderCompaction, CollabPlaceholderLegacy:
		default:
			return n, nil, false
		}
		n.Replaced = r
	case CollabRecordMutationTerminated:
		r := m.Terminated
		if !collabKeyBound(r.Key) || !collabDigestBound(r.Event) || !collabDigestBound(r.Parent) || len(r.Copies) > CollabRecordMaxCopies {
			return n, nil, false
		}
		for _, copy := range r.Copies {
			if !collabDigestBound(copy) {
				return n, nil, false
			}
		}
		r.Copies = slices.Clone(r.Copies)
		n.Terminated = r
	case CollabRecordMutationTerminalRejected:
		r := m.TerminalRejection
		if !collabKeyBound(r.Key) || !collabDigestBound(r.Rejection) || !collabDigestBound(r.Event) {
			return n, nil, false
		}
		n.TerminalRejection = r
	case CollabRecordMutationLocalEnding:
		if !collabScopeBound(m.Scope) || !collabDigestBound(m.Event) {
			return n, nil, false
		}
		n.Scope = m.Scope
		n.Event = m.Event
		n.Issued.ExpiresAt = m.Issued.ExpiresAt
		if n.Issued.ExpiresAt.IsZero() {
			n.Issued.ExpiresAt = now.Add(CollabRecordTTL)
		}
	default:
		return n, nil, false
	}
	limit := now.Add(CollabRecordTTL)
	clamp := func(t time.Time) time.Time {
		if t.After(limit) {
			return limit
		}
		return t
	}
	switch n.Kind {
	case CollabRecordMutationPending, CollabRecordMutationLocalEnding:
		n.Issued.ExpiresAt = clamp(n.Issued.ExpiresAt)
	case CollabRecordMutationPlaintext:
		n.Plaintext.ExpiresAt = clamp(n.Plaintext.ExpiresAt)
	case CollabRecordMutationConsumed:
		n.Consumed.ExpiresAt = clamp(n.Consumed.ExpiresAt)
	case CollabRecordMutationReplaced:
		n.Replaced.ExpiresAt = clamp(n.Replaced.ExpiresAt)
	case CollabRecordMutationTerminated:
		n.Terminated.ExpiresAt = clamp(n.Terminated.ExpiresAt)
	case CollabRecordMutationTerminalRejected:
		n.TerminalRejection.ExpiresAt = clamp(n.TerminalRejection.ExpiresAt)
	}
	payload, err := json.Marshal(n)
	return n, payload, err == nil && len(payload) <= CollabRecordMaxRowBytes
}
func collabBoundBatch(b CollabRecordBatch) CollabRecordBatch {
	b.Queries = b.Queries[:min(len(b.Queries), CollabRecordMaxQueries)]
	return b
}

// Charge the JSON and both hash/sorted-set field bytes, plus headroom for a
// status transition. Redis's object metadata is bounded separately by row count.
func collabRecordCharge(field string, payload []byte) int { return len(payload) + 2*len(field) + 32 }
