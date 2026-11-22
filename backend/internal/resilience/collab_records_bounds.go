package resilience

import (
	"encoding/json"
	"time"
)

// Encoded rows plus their three indexed field copies occupy at most 128 MiB
// across 4096 trees. Redis allocator/object overhead is additional; at most
// 536576 rows exist (64 evidence + 64 dispositions + 3 reservations/tree). These are shared-cache limits, not per tenant limits.
const (
	CollabRecordMaxTrees         = 4096
	CollabRecordMaxTreeEntries   = 64
	CollabRecordMaxRowBytes      = 4096
	CollabRecordMaxTreeBytes     = 32 << 10
	CollabRecordMaxQueries       = 256
	CollabRecordMaxCopies        = 16
	CollabRecordMaxTenantTrees   = 64
	CollabRecordEvidenceBytes    = 16 << 10
	CollabRecordDispositionBytes = 14 << 10
	CollabRecordCounterBytes     = 2 << 10
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
		if !collabKeyBound(r.Key) || !collabDigestBound(r.Event) || !collabDigestBound(r.Parent) {
			return n, nil, false
		}
		copies := make([]CollabDigest, 0, min(len(r.Copies), CollabRecordMaxCopies))
		for _, copy := range r.Copies[:min(len(r.Copies), CollabRecordMaxCopies)] {
			if collabDigestBound(copy) && copy != "" {
				copies = append(copies, copy)
			}
		}
		r.Copies = copies
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

func collabUniqueQueries(queries []CollabRecordQuery) ([]CollabRecordQuery, bool) {
	unique := make([]CollabRecordQuery, 0, min(len(queries), CollabRecordMaxQueries))
	seen := make(map[CollabRecordQuery]bool)
	for _, q := range queries {
		if seen[q] {
			continue
		}
		if len(unique) == CollabRecordMaxQueries {
			return unique, true
		}
		seen[q] = true
		unique = append(unique, q)
	}
	return unique, false
}
func collabBoundBatch(b CollabRecordBatch) CollabRecordBatch {
	if b.DispositionQueries == nil {
		b.DispositionQueries = b.Queries
	}
	var truncated bool
	b.DispositionQueries, truncated = collabUniqueQueries(b.DispositionQueries)
	b.dispositionsTruncated = b.dispositionsTruncated || truncated
	b.Queries, truncated = collabUniqueQueries(b.Queries)
	b.recordsTruncated = b.recordsTruncated || truncated
	return b
}

// Evidence, dispositions, and the three reservation rows have separate quotas.
func collabRecordPartition(kind CollabRecordMutationKind) int {
	switch kind {
	case CollabRecordMutationPending, CollabRecordMutationConfirm, CollabRecordMutationVoid, CollabRecordMutationPlaintext, CollabRecordMutationConsumed:
		return 0
	case CollabRecordMutationLocalEnding:
		return 2
	default:
		return 1
	}
}
func collabPartitionBytes(part int) int {
	return [3]int{CollabRecordEvidenceBytes, CollabRecordDispositionBytes, CollabRecordCounterBytes}[part]
}

// Charge the JSON and hash, expiry and write-order field bytes, plus headroom for a
// status transition. Redis's object metadata is bounded separately by row count.
func collabRecordCharge(field string, payload []byte) int { return len(payload) + 3*len(field) + 32 }
