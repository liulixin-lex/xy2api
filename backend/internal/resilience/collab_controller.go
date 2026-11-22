package resilience

import (
	"context"
	"time"
)

const (
	CollabOptionalSendLimit         = 3
	CollabOptionalFirstEventTimeout = 60 * time.Second
)

// Optional sends cannot consume an existing repair quota. Floor repairs keep
// their own existing limits; there is deliberately no combined send ceiling.
type CollabQuotas struct {
	OptionalWaitBudget time.Duration
	OptionalWaitSpent  time.Duration
	OptionalSends      int
	CipherSwitchUsed   bool
	ProbeUsed          map[int64]bool
	NamedUsed          map[CollabFamily]bool
	NamedItems         map[CollabDigest]bool
	DropUsed           bool
	ReplaceAllUsed     map[int64]bool
	PendingRetryUsed   bool
	FloorEntered       bool
}

type CollabOrder string

const (
	CollabOrderLegacyFirst   CollabOrder = "legacy_first"
	CollabOrderLosslessFirst CollabOrder = "lossless_first"
)

func (s *Settings) CollabOrder() CollabOrder {
	if s.CollabLosslessFirstOn() {
		return CollabOrderLosslessFirst
	}
	return CollabOrderLegacyFirst
}

// Deadline presence is explicit: an absent deadline is not an expired budget.
// ClientIdleRemaining bounds both orders. Lossless-first additionally requires
// enough idle headroom to restore the entire elapsed optional time to legacy.
type CollabOptionalBudget struct {
	SavedRetryable         bool
	UserHasWaiters         bool
	HasClientIdleDeadline  bool
	Order                  CollabOrder
	HasDeadline            bool
	Remaining              time.Duration
	ClientIdleRemaining    time.Duration
	LegacyTimeToRestore    time.Duration
	CompensationAvailable  bool
	FirstAccountSpareSlots bool
	FirstAccountHasWaiters bool
	HoldRevoked            bool
}

type CollabSlotPolicy struct {
	CommitHeaders           bool
	CommitHeartbeats        bool
	AcceptanceSignalTimeout time.Duration
	StopOnUserWaiters       bool
	Allowed                 bool
	HoldFirstAccount        bool
	RevocableHold           bool
	ImmediateTargetOnly     bool
	FirstEventTimeout       time.Duration
	TotalOptionalWait       time.Duration
	CompensateLegacyTime    bool
	FallbackOnAnyFailure    bool
}

// One budget covers selection, batched records, sending and cancellation.
// A lossless-first hold is rechecked before each optional action and revoked
// as soon as a waiter appears or capacity disappears; then resume legacy.
func CollabOptionalPolicy(in CollabOptionalBudget) CollabSlotPolicy {
	wait := CollabOptionalFirstEventTimeout
	if in.Order != CollabOrderLosslessFirst && in.SavedRetryable {
		wait = CollabRetryableAcceptanceTimeout
	}
	if in.HasClientIdleDeadline {
		wait = min(wait, max(time.Duration(0), in.ClientIdleRemaining))
	}
	if in.HasDeadline {
		wait = min(wait, max(time.Duration(0), in.Remaining))
	}
	hold := in.FirstAccountSpareSlots && !in.FirstAccountHasWaiters && !in.HoldRevoked
	lossless := in.Order == CollabOrderLosslessFirst
	if lossless {
		if in.HasClientIdleDeadline {
			wait = min(wait, max(time.Duration(0), in.ClientIdleRemaining-in.LegacyTimeToRestore))
		}
		if !in.CompensationAvailable || !hold {
			wait = 0
		}
	}
	if in.UserHasWaiters {
		wait = 0
	}
	return CollabSlotPolicy{AcceptanceSignalTimeout: wait, StopOnUserWaiters: true, Allowed: wait > 0, HoldFirstAccount: wait > 0 && hold,
		RevocableHold: true, ImmediateTargetOnly: true, FirstEventTimeout: wait,
		TotalOptionalWait: wait, CompensateLegacyTime: lossless && wait > 0, FallbackOnAnyFailure: true}
}

// LegacyCompensation returns the time to restore to the original repair budget.
// Its caller measures the whole optional phase, including reads and cancellation.
func (p CollabSlotPolicy) LegacyCompensation(elapsed time.Duration) time.Duration {
	if !p.CompensateLegacyTime {
		return 0
	}
	return min(p.TotalOptionalWait, max(time.Duration(0), elapsed))
}

// Saved retains the exact existing terminal before it commits. The writer
// uses its original failure, headers and retry advice if takeover fails.
type CollabLegacyEndpoint struct {
	Saved             *CollabSavedTerminal
	NewCipherEvidence bool
}

type CollabAccountSelection struct {
	Preferences        []CollabAccountPreference
	ExcludedAccountIDs []int64
	ImmediateOnly      bool
}

type CollabAdmittedAccount struct {
	AccountID int64
	Records   CollabRecordBatchResult
}

type CollabCandidateInput struct {
	Records   CollabRecordBatchResult
	Original  []byte
	Derived   []byte
	AccountID int64
	Evidence  CollabEvidence
}

type CollabCandidatePlan struct {
	Action          CollabAction
	AccountID       int64
	Body            []byte
	Edits           []CollabEdit
	Positions       map[CollabDigest]CollabInputPosition
	PendingLearning []CollabLearning
	Optional        bool
	Slots           CollabSlotPolicy
}

// Plan must not mutate input, quotas, flags, learning, counters or events.
// CommitOnAdopt is a distinct operation, called only once after adoption.
// Optional quota and switch usage are charged only by ConfirmSend, never Plan.
type CollabCandidate interface {
	Plan(CollabCandidateInput) (CollabCandidatePlan, bool)
	CommitOnAdopt(context.Context, CollabSendSequence, CollabCandidatePlan)
}

type CollabSendReservation struct {
	Seq       CollabSendSequence
	AccountID int64
	Action    CollabAction
	Optional  bool
}

type CollabSettlement struct {
	Optional   bool
	Abandoned  bool
	Seq        CollabSendSequence
	Generation CollabGeneration
	Result     CollabSendResult
	Evidence   CollabEvidence
	// Original status/code/retryability remain owned by the existing path.
	Failure error
}

type CollabNextStep struct {
	Selection *CollabAccountSelection
	Saved     *CollabSavedTerminal
	Kind      CollabNextKind
	Candidate *CollabCandidatePlan
	Reason    CollabReason
}

type CollabTerminal struct {
	Mode          CollabFinalMode
	Reason        CollabReason
	Undeliverable bool
}

// ReserveSend allocates a unique sequence; ConfirmSend charges only real sends;
// ReleaseSend releases an unsent reservation. Settle seals a confirmed sequence
// at most once. ClaimTerminal is one-way, including after a failed local write.
// EnterFloor resumes the untouched legacy path for lossless-first. In
// legacy-first it returns the saved error; it must not replay finished repairs.
// New failures must preserve that path's status, code and retryability.
type CollabController interface {
	// LegacyFirst takes over only at this endpoint (or newly claimed evidence).
	// Implementations preserve endpoint.Saved.Snapshot.Failure by identity until terminal.
	TakeOver(CollabLegacyEndpoint) bool
	// Selection is resolved by the handler before planning against original bytes
	// plus the admitted account's records. No candidate may assume a target.
	AccountAdmitted(CollabAdmittedAccount) bool
	Propose(CollabCandidate) bool
	ReserveSend(CollabCandidatePlan) (CollabSendReservation, bool)
	ConfirmSend(CollabSendSequence) bool
	ReleaseSend(CollabSendSequence)
	Settle(CollabSettlement) bool
	NextStep() CollabNextStep
	ClaimTerminal(CollabTerminal) bool
	EnterFloor(CollabReason)
}

// The no-op controller never plans, adopts, sends or claims a terminal. Legacy
// remains the explicit next step even when the caller enables every switch.
type NoopCollabController struct{}

func (NoopCollabController) TakeOver(CollabLegacyEndpoint) bool         { return false }
func (NoopCollabController) AccountAdmitted(CollabAdmittedAccount) bool { return false }

func (NoopCollabController) Propose(CollabCandidate) bool { return false }
func (NoopCollabController) ReserveSend(CollabCandidatePlan) (CollabSendReservation, bool) {
	return CollabSendReservation{}, false
}
func (NoopCollabController) ConfirmSend(CollabSendSequence) bool { return false }
func (NoopCollabController) ReleaseSend(CollabSendSequence)      {}
func (NoopCollabController) Settle(CollabSettlement) bool        { return false }
func (NoopCollabController) NextStep() CollabNextStep {
	return CollabNextStep{Kind: CollabNextKindLegacy, Reason: CollabReasonNone}
}
func (NoopCollabController) ClaimTerminal(CollabTerminal) bool { return false }
func (NoopCollabController) EnterFloor(CollabReason)           {}

// NoopCollabCandidate is safe to propose even before an action is implemented.
type NoopCollabCandidate struct{}

func (NoopCollabCandidate) Plan(CollabCandidateInput) (CollabCandidatePlan, bool) {
	return CollabCandidatePlan{}, false
}
func (NoopCollabCandidate) CommitOnAdopt(context.Context, CollabSendSequence, CollabCandidatePlan) {}
