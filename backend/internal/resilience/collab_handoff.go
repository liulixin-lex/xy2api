package resilience

import (
	"net/http"
	"slices"
	"sync/atomic"
	"time"
)

// Capture before the existing terminal commits headers, advice suppression or
// terminal ownership. Failure is the original object; do not wrap or mutate it.
// These transient fields must never be logged or persisted as a whole.
type CollabTerminalSnapshot struct {
	Failure   error
	Status    int
	Headers   http.Header
	Advice    RetryAdvice
	AccountID int64
	Rejection CollabEvidence
	Retryable bool
}

type CollabSavedTerminal struct {
	// Snapshot is immutable after capture.
	Snapshot   CollabTerminalSnapshot
	persistent atomic.Bool
	outcome    atomic.Pointer[CollabTerminal]
	delivery   atomic.Bool
}

func NewCollabSavedTerminal(snapshot CollabTerminalSnapshot) *CollabSavedTerminal {
	snapshot.Headers = snapshot.Headers.Clone()
	if snapshot.Rejection.Named != nil {
		named := *snapshot.Rejection.Named
		snapshot.Rejection.Named = &named
	}
	if snapshot.Rejection.Inner != nil {
		inner := *snapshot.Rejection.Inner
		inner.Payload = slices.Clone(inner.Payload)
		snapshot.Rejection.Inner = &inner
	}
	return &CollabSavedTerminal{Snapshot: snapshot}
}

// ClaimPersistentEffects is called at the original terminal boundary, including
// when takeover is skipped. Counts and avoidance run exactly once there, never
// for optional sends. It does not claim delivery or suppress retry advice.
func (s *CollabSavedTerminal) ClaimPersistentEffects() bool {
	return s != nil && s.persistent.CompareAndSwap(false, true)
}

// ResolveOutcome fixes the final outcome before delivery-only effects run.
func (s *CollabSavedTerminal) ResolveOutcome(outcome CollabTerminal) bool {
	return s != nil && s.outcome.CompareAndSwap(nil, &outcome)
}

// ClaimDeliveryEffects defers terminal ownership and advice suppression until
// final resolution. Persistent effects have a separate owner and timing.
func (s *CollabSavedTerminal) ClaimDeliveryEffects() bool {
	return s != nil && s.outcome.Load() != nil && s.delivery.CompareAndSwap(false, true)
}

const CollabRetryableAcceptanceTimeout = 15 * time.Second

// This is an additional gate after all existing local-ending eligibility tests.
// The count excludes the current request: retryable errors require two prior
// terminal rejections of this exact delivery with the same rejection key.
func CollabLocalEndingPermitted(saved *CollabSavedTerminal, delivery CollabDeliveryKey, rejection CollabDigest, prior []CollabTerminalRejectionCount, now time.Time) bool {
	if saved == nil {
		return false
	}
	if !saved.Snapshot.Retryable {
		return true
	}
	if delivery.Delivery == "" || rejection == "" {
		return false
	}
	for _, count := range prior {
		if count.Key == delivery && count.Rejection == rejection && count.ExpiresAt.After(now) && count.Count >= CollabTerminalRejectionThreshold {
			return true
		}
	}
	return false
}

type CollabNamedNext string

const (
	CollabNamedNextUnlocatable CollabNamedNext = "unlocatable"
	CollabNamedNextRepair      CollabNamedNext = "repair"
	CollabNamedNextOwnEndpoint CollabNamedNext = "own_endpoint"
)

// An explicitly named item never becomes unnamed, including after repair.
func CollabNamedNextStep(named CollabNamedItem, handled map[CollabDigest]bool) CollabNamedNext {
	if !named.Located || named.MatchCount != 1 || named.Ambiguous || named.Item == "" {
		return CollabNamedNextUnlocatable
	}
	if handled[named.Item] {
		return CollabNamedNextOwnEndpoint
	}
	return CollabNamedNextRepair
}
