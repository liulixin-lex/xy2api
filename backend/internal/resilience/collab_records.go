package resilience

import (
	"context"
	"time"
)

const (
	CollabRecordTTL            = 7 * 24 * time.Hour
	CollabRecordTimeout        = 250 * time.Millisecond
	CollabRecordMaxEntries     = 65536
	CollabTreeLocalEndingLimit = 3
)

// Scope is tenant + group + tree. Account identity is account + stable target,
// never a token or credential version. All persisted identifiers are HMACs.
type CollabRecordScope struct {
	Tenant CollabDigest
	Group  CollabDigest
	Tree   CollabDigest
}

type CollabDeliveryKey struct {
	Scope    CollabRecordScope
	Thread   CollabDigest
	Item     CollabDigest
	Delivery CollabDigest
	Digest   CollabDigest
}

type CollabRecordQuery struct {
	Key       CollabDeliveryKey
	Account   CollabDigest
	Call      CollabDigest
	Arguments CollabDigest
	Event     CollabDigest
}

type CollabIssuedRecord struct {
	Scope               CollabRecordScope
	Call                CollabDigest
	Digest              CollabDigest
	Account             CollabDigest
	Site                CollabDigest
	ExplicitlyEncrypted bool
	Status              CollabIssuedStatus
	Generation          CollabGeneration
	ExpiresAt           time.Time
}

type CollabIssuedToken struct {
	// Token distinguishes concurrent requests with the same call/generation.
	Token      CollabDigest
	Scope      CollabRecordScope
	Call       CollabDigest
	Generation CollabGeneration
}

type CollabPlaintextRecord struct {
	Scope     CollabRecordScope
	Call      CollabDigest
	Arguments CollabDigest
	ExpiresAt time.Time
}

type CollabConsumedRecord struct {
	Key       CollabDeliveryKey
	Account   CollabDigest
	ExpiresAt time.Time
}

type CollabReplacedRecord struct {
	Key         CollabDeliveryKey
	Account     CollabDigest
	Placeholder CollabPlaceholder
	ExpiresAt   time.Time
}

type CollabTerminatedRecord struct {
	Key       CollabDeliveryKey
	Copies    []CollabDigest
	Event     CollabDigest
	Parent    CollabDigest
	ExpiresAt time.Time
}

// Token snapshots are merged before deriving Issued. A void cannot erase a
// different request's confirmed delivery.
type CollabIssuedEntry struct {
	Token  CollabIssuedToken
	Record CollabIssuedRecord
}
type CollabRecordLookup struct {
	IssuedTokens []CollabIssuedEntry
	Issued       []CollabIssuedRecord
	Plaintext    []CollabPlaintextRecord
	Consumed     []CollabConsumedRecord
}

type CollabDispositionLookup struct {
	TerminalRejections []CollabTerminalRejectionCount
	Replaced           []CollabReplacedRecord
	Terminated         []CollabTerminatedRecord
}

// Local methods are strictly in-process and bounded: no network I/O, waits or
// callbacks. SafeCollabRecordsStore is the request-facing adapter: it recovers
// panics, batches reads under one request deadline and queues shared writes.
// Pending issues are visible locally before a frame, but cannot create a lease
// until confirmation. Dispositions remain independent of the records switch.
type CollabRecordsStore interface {
	TerminalRejected(context.Context, CollabTerminalRejectionRecord) (int, bool)
	LookupBatch(context.Context, CollabRecordBatch) CollabRecordBatchResult
	PendingIssued(context.Context, CollabIssuedRecord) (CollabIssuedToken, bool)
	ConfirmIssued(context.Context, CollabIssuedToken) bool
	VoidIssued(context.Context, CollabIssuedToken)
	PlaintextDelivered(context.Context, CollabPlaintextRecord) bool
	Consumed(context.Context, CollabConsumedRecord) bool
	Replaced(context.Context, CollabReplacedRecord) bool
	Terminated(context.Context, CollabTerminatedRecord) bool
	// Increment is idempotent per event and occurs only after full delivery.
	IncrementTreeLocalEnding(context.Context, CollabRecordScope, CollabDigest) (int, bool)
}

type NoopCollabRecordsStore struct{}

func (NoopCollabRecordsStore) TerminalRejected(context.Context, CollabTerminalRejectionRecord) (int, bool) {
	return 0, false
}

func (NoopCollabRecordsStore) LookupBatch(context.Context, CollabRecordBatch) CollabRecordBatchResult {
	return CollabRecordBatchResult{}
}
func (NoopCollabRecordsStore) PendingIssued(context.Context, CollabIssuedRecord) (CollabIssuedToken, bool) {
	return CollabIssuedToken{}, false
}
func (NoopCollabRecordsStore) ConfirmIssued(context.Context, CollabIssuedToken) bool { return false }
func (NoopCollabRecordsStore) VoidIssued(context.Context, CollabIssuedToken)         {}
func (NoopCollabRecordsStore) PlaintextDelivered(context.Context, CollabPlaintextRecord) bool {
	return false
}
func (NoopCollabRecordsStore) Consumed(context.Context, CollabConsumedRecord) bool     { return false }
func (NoopCollabRecordsStore) Replaced(context.Context, CollabReplacedRecord) bool     { return false }
func (NoopCollabRecordsStore) Terminated(context.Context, CollabTerminatedRecord) bool { return false }
func (NoopCollabRecordsStore) IncrementTreeLocalEnding(context.Context, CollabRecordScope, CollabDigest) (int, bool) {
	return 0, false
}

// A single batch includes all original items, disposition lookups and the tree
// counter. Flags preserve independent records/disposition gates.
type CollabRecordBatch struct {
	Scope        CollabRecordScope
	Queries      []CollabRecordQuery
	Records      bool
	Dispositions bool
}
type CollabRecordBatchResult struct {
	Records          map[CollabRecordQuery]CollabRecordLookup
	Dispositions     map[CollabRecordQuery]CollabDispositionLookup
	TreeLocalEndings int
}

type CollabRecordMutationKind string

const (
	CollabRecordMutationTerminalRejected CollabRecordMutationKind = "terminal_rejected"
	CollabRecordMutationPending          CollabRecordMutationKind = "pending"
	CollabRecordMutationConfirm          CollabRecordMutationKind = "confirm"
	CollabRecordMutationVoid             CollabRecordMutationKind = "void"
	CollabRecordMutationPlaintext        CollabRecordMutationKind = "plaintext"
	CollabRecordMutationConsumed         CollabRecordMutationKind = "consumed"
	CollabRecordMutationReplaced         CollabRecordMutationKind = "replaced"
	CollabRecordMutationTerminated       CollabRecordMutationKind = "terminated"
	CollabRecordMutationLocalEnding      CollabRecordMutationKind = "local_ending"
)

type CollabRecordMutation struct {
	TerminalRejection CollabTerminalRejectionRecord `json:",omitzero"`
	Kind              CollabRecordMutationKind      `json:",omitzero"`
	Token             CollabIssuedToken             `json:",omitzero"`
	Issued            CollabIssuedRecord            `json:",omitzero"`
	Plaintext         CollabPlaintextRecord         `json:",omitzero"`
	Consumed          CollabConsumedRecord          `json:",omitzero"`
	Replaced          CollabReplacedRecord          `json:",omitzero"`
	Terminated        CollabTerminatedRecord        `json:",omitzero"`
	Scope             CollabRecordScope             `json:",omitzero"`
	Event             CollabDigest                  `json:",omitzero"`
}

// The shared implementation is called only on bounded background workers.
// Apply must be idempotent and preserve pending/confirm/void ordering. An
// unknown confirmation cannot fabricate an issue; overflow is absence.
type CollabSharedRecordsStore interface {
	LookupBatch(context.Context, CollabRecordBatch) (CollabRecordBatchResult, error)
	Apply(context.Context, CollabRecordMutation) error
}

const CollabTerminalRejectionThreshold = 2

// Rejection is a keyed digest of the normalized rejection identity, including
// status/code/family. Delivery identity is distinct even for identical content.
// Event identifies the ingress terminal, making repeat callbacks idempotent.
type CollabTerminalRejectionRecord struct {
	Key       CollabDeliveryKey
	Rejection CollabDigest
	Event     CollabDigest
	ExpiresAt time.Time
}
type CollabTerminalRejectionCount struct {
	Key       CollabDeliveryKey
	Rejection CollabDigest
	Count     int
	ExpiresAt time.Time
}
