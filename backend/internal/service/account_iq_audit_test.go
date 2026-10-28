package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestAccountIQTargetVerdictDoesNotBecomeAccountWideGate(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"model_mapping": map[string]any{"public": "gpt-6-astra"}},
		Extra: map[string]any{"model_rate_limits": map[string]any{
			"iq:gpt-6-astra": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Minute).Format(time.RFC3339)},
		}},
		IQTargets: []domain.IQTargetSummary{{Model: "gpt-6-astra", Enabled: true, Verdict: "degraded"}},
	}
	require.True(t, account.IsSchedulable(), "a target verdict does not change account-wide schedulability")
	require.True(t, account.isModelRateLimitedWithContext(context.Background(), "public"))
	require.False(t, account.isModelRateLimitedWithContext(context.Background(), "gpt-6.1-sol"))
	account.IQTargets[0].Verdict = "smart"
	require.True(t, account.IsSchedulable(), "a later verdict remains scoped to its model gate")
}
