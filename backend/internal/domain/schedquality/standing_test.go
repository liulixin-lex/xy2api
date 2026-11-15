//go:build unit

package schedquality

import (
	"testing"
	"time"
)

func TestStandingExempt(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		view StateView
		auto bool
		want Standing
	}{
		{"normal", StateView{}, true, StandFront},
		{"failing", StateView{Failing: true}, true, StandBack},
		{"paused", StateView{Failing: true}, false, StandFront},
		{"group-exempt", StateView{Failing: true, GroupOverride: OverrideRef{Mode: OverrideExempt}}, true, StandFront},
		{"expired-group-falls-back-to-global", StateView{Failing: true, GroupOverride: OverrideRef{Mode: OverrideExempt, Expires: now}, AllOverride: OverrideRef{Mode: OverrideExempt}}, true, StandFront},
		{"expired-exemption", StateView{Failing: true, GroupOverride: OverrideRef{Mode: OverrideExempt, Expires: now}}, true, StandBack},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := StandingOf(tc.view, tc.auto, now); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
