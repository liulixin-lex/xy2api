package resilience

import (
	"net/http"
	"slices"
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
	Snapshot CollabTerminalSnapshot
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

const CollabRetryableAcceptanceTimeout = 15 * time.Second

// This is an additional gate after all existing local-ending eligibility tests.
// The count excludes the current request: retryable errors require two prior
// terminal rejections of this exact delivery with the same rejection key.
func CollabLocalEndingPermitted(saved *CollabSavedTerminal, delivery CollabDeliveryKey, rejection CollabDigest, prior []CollabTerminalRejectionCount, now time.Time) bool {
	if saved == nil {
		return false
	}
	return collabLocalEndingRetryPermitted(saved.Snapshot.Retryable, delivery, rejection, prior, now)
}

func collabLocalEndingRetryPermitted(retryable bool, delivery CollabDeliveryKey, rejection CollabDigest, prior []CollabTerminalRejectionCount, now time.Time) bool {
	if !retryable {
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
