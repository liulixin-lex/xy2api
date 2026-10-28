//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestExplicitModelUnsupportedLimitCannotUseLastResort(t *testing.T) {
	ctx := withResilienceLastResortGate(context.Background())
	account := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				"gpt-5.4": map[string]any{
					"rate_limit_reset_at": time.Now().Add(30 * time.Minute).Format(time.RFC3339),
					"reason":              upstreamModelUnsupported400Reason,
				},
			},
		},
	}

	require.True(t, account.modelUnsupportedRateLimitActive(ctx, "gpt-5.4", time.Now()))
	require.False(t, lastResortModelLimitSoft(ctx, account, "gpt-5.4", false))
	require.False(t, account.IsSchedulableForModelWithContext(ctx, "gpt-5.4"))
	tier, _, reason := resiliencePenaltyOf(account, "gpt-5.4", time.Now())
	require.Equal(t, resilience.TierHard, tier)
	require.Equal(t, "model_unsupported", reason)
}
