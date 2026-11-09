//go:build unit

package service

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Test compatibility helpers keep the existing assertions on timeout values.
// Production callers use the plan-returning functions to preserve attribution.

// openAIFirstOutputTimeoutFor returns the first-output timeout of an OpenAI
// attempt, measured from attemptStart: under the request budget the effort
// tier (or model override) bounded by the budget left at attemptStart,
// otherwise the 0.3.0 configuration. The stream handler asks again when the
// upstream headers arrive; measuring the budget from that moment instead
// cut a slow-header attempt the instant its headers came (staging KA-1).
func (s *OpenAIGatewayService) openAIFirstOutputTimeoutFor(ctx context.Context, accountID int64, model, reasoningEffort string, attemptStart time.Time) time.Duration {
	timeout, _ := s.openAIFirstOutputPlanFor(ctx, accountID, model, reasoningEffort, attemptStart)
	return timeout
}

// resilienceFirstEventTimeout returns the first-upstream-event timeout of a
// streaming Anthropic or Gemini attempt, or zero when none applies.
func resilienceFirstEventTimeout(ctx context.Context, accountID int64, platform string) time.Duration {
	timeout, _ := resilienceFirstEventPlan(ctx, accountID, platform)
	return timeout
}

func resilienceProfileAttemptStart(ctx context.Context, accountID int64, fallback time.Time) time.Time {
	profile := resilience.StateFrom(ctx).AttemptProfile()
	if profile.AccountID == accountID && !profile.AttemptStart.IsZero() {
		return profile.AttemptStart
	}
	if !fallback.IsZero() {
		return fallback
	}
	return time.Now()
}

func resilienceFirstEventPlan(ctx context.Context, accountID int64, platform string) (time.Duration, resilience.TimeoutPlan) {
	started := resilienceProfileAttemptStart(ctx, accountID, time.Time{})
	return resilienceFirstEventPlanAt(ctx, accountID, platform, started)
}
