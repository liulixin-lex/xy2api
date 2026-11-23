package resilience

import (
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
	Allowed              bool
	HoldFirstAccount     bool
	RevocableHold        bool
	ImmediateTargetOnly  bool
	FirstEventTimeout    time.Duration
	TotalOptionalWait    time.Duration
	CompensateLegacyTime bool
	FallbackOnAnyFailure bool
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
	return CollabSlotPolicy{Allowed: wait > 0, HoldFirstAccount: wait > 0 && hold,
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

// CollabSendReservation identifies a send in request observations.
type CollabSendReservation struct {
	Seq       CollabSendSequence
	AccountID int64
	Action    CollabAction
	Optional  bool
}
