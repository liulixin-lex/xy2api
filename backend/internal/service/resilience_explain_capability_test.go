//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResilienceExplainCapabilityRequestScope(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scoped   bool
		stream   bool
		disabled bool
		blocked  bool
	}{
		{name: "default displays learning"},
		{name: "non-streaming excludes", scoped: true, blocked: true},
		{name: "streaming stays healthy", scoped: true, stream: true},
		{name: "disabled displays learning", scoped: true, disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, accounts, _ := newFallbackFixture(t)
			cfg := *resilience.Current()
			cfg.Capability.StreamOnlyLearning = !tc.disabled
			resilience.Publish(cfg)
			expiry := time.Now().UTC().Add(time.Minute)
			account := healthyAccount(1)
			account.Type = AccountTypeAPIKey
			account.Extra = map[string]any{capabilityLearnedResponsesKey: map[string]any{
				"expires_at": expiry.Format(time.RFC3339Nano), "fingerprint": capabilityFingerprint(&account),
			}}
			accounts.byGroup[10] = []Account{account}
			ctx := context.Background()
			if tc.scoped {
				ctx = resilience.WithCapabilityRequest(ctx, resilience.CapabilityProtocolResponses, tc.stream)
			}
			out, err := r.Explain(ctx, 10, "", "gpt-5.5")
			require.NoError(t, err)
			raw, err := json.Marshal(out)
			require.NoError(t, err)
			require.Equal(t, "stream_only", gjson.GetBytes(raw, "accounts.0.capability_learned.0.mode").String())
			if tc.blocked {
				require.Zero(t, out.HealthyCount)
				require.Nil(t, out.LastResort)
				require.Equal(t, resilience.ReasonCapabilityStreamOnly, out.Accounts[0].Reason)
				require.Equal(t, "none", out.Accounts[0].Tier, "capability is not a global penalty")
				require.NotNil(t, out.Accounts[0].RecoverAt)
				require.True(t, expiry.Equal(*out.Accounts[0].RecoverAt))
				require.InDelta(t, 60, gjson.GetBytes(raw, "accounts.0.capability_ttl_seconds").Int(), 1)
				require.True(t, out.FallbackGroup.WouldMove)
			} else {
				require.Equal(t, 1, out.HealthyCount)
				require.Empty(t, out.Accounts[0].Reason)
				require.False(t, out.FallbackGroup.WouldMove)
			}
			fallback, err := r.Explain(ctx, 20, "", "gpt-5.5")
			require.NoError(t, err)
			raw, err = json.Marshal(fallback)
			require.NoError(t, err)
			require.Equal(t, "[]", gjson.GetBytes(raw, "accounts.0.capability_learned").Raw)
		})
	}
}

func TestResilienceExplainCapabilityExcludesSoftButPreservesHardReason(t *testing.T) {
	r, _, accounts, _ := newFallbackFixture(t)
	expiry := time.Now().UTC().Add(time.Hour)
	soft := softAccount(1)
	soft.Type = AccountTypeAPIKey
	soft.Extra = map[string]any{capabilityLearnedResponsesKey: map[string]any{
		"expires_at": expiry.Format(time.RFC3339Nano), "fingerprint": capabilityFingerprint(&soft),
	}}
	hard := soft
	hard.ID = 2
	hard.TempUnschedulableReason = resilience.BalancePauseReason("insufficient account balance")
	accounts.byGroup[10] = []Account{soft, hard}
	ctx := resilience.WithCapabilityRequest(context.Background(), resilience.CapabilityProtocolResponses, false)
	out, err := r.Explain(ctx, 10, "", "gpt-5.5")
	require.NoError(t, err)
	require.Zero(t, out.HealthyCount)
	require.Nil(t, out.LastResort, "a learned soft account cannot be used as the last resort for non-streaming")
	require.Equal(t, resilience.ReasonCapabilityStreamOnly, out.Accounts[0].Reason)
	require.Equal(t, "soft", out.Accounts[0].Tier)
	require.Equal(t, "balance_exhausted", out.Accounts[1].Reason)
	require.Equal(t, "hard", out.Accounts[1].Tier)
	require.Nil(t, out.Accounts[1].RecoverAt, "keep the hard penalty's unknown recovery")

	// The fallback must also honor the requested mode.
	accounts.byGroup[20] = []Account{soft}
	r.lastResortGroups.entries = nil
	out, err = r.Explain(ctx, 10, "", "gpt-5.5")
	require.NoError(t, err)
	require.False(t, out.FallbackGroup.FallbackHealthy)
	require.False(t, out.FallbackGroup.WouldMove)
}
