//go:build unit

package service

import (
	"errors"
	"testing"
)

func TestFoundationSelectionExhaustion(t *testing.T) {
	if (&SelectionExhaustion{}).Known {
		t.Fatal("zero means known")
	}
	cause := &HTTPContinuationError{Status: 410, Message: "expired"}
	stats := &SelectionExhaustion{Known: true, Reasons: map[SelectionReason]int{SelectionReasonQueueFull: 2}, LastCause: cause}
	err := openAINoAvailableSelectionError{message: "original", capabilityOnly: true, exhaustion: stats}
	if !errors.Is(err, ErrNoAvailableAccounts) || !errors.Is(err, cause) || !err.CapabilityOnly() || err.Error() != "original" {
		t.Fatal(err)
	}
	var got *SelectionExhaustion
	var last *HTTPContinuationError
	if !errors.As(err, &got) || !errors.As(err, &last) || last != cause {
		t.Fatal(got, last)
	}
	got.Reasons[SelectionReasonQueueFull] = 9
	if stats.Reasons[SelectionReasonQueueFull] != 2 {
		t.Fatal("reason alias")
	}
	if !errors.Is(openAINoAvailableSelectionError{}, ErrNoAvailableAccounts) {
		t.Fatal("legacy chain changed")
	}
}
