//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

func TestOpenAIShareCapabilityReasonAndOnlySelection(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "only capability", true: "mixed model"}[mixed], func(t *testing.T) {
			env := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
			for _, id := range []int64{1, 2} {
				a := env.account(id)
				a.Extra = map[string]any{capabilityLearnedResponsesKey: map[string]any{"fingerprint": capabilityFingerprint(a), "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}}
			}
			if mixed {
				env.account(2).Extra = nil
				env.account(2).Credentials = map[string]any{"model_mapping": map[string]any{"different-model": "different-model"}}
			}
			ctx := resilience.WithCapabilityRequest(context.Background(), "responses", false)
			gid := shareTestGroupID
			in := openAIShareSelection{groupID: &gid, platform: PlatformOpenAI, model: "gpt-5.1", transport: OpenAIUpstreamTransportAny}
			cands, _, stats, err := env.svc.shareOpenAICandidates(ctx, in, 0)
			require.NoError(t, err)
			require.Equal(t, sharesched.ReasonCapabilityMismatch, cands[0].Reason)
			require.Equal(t, !mixed, stats.capabilityOnly())
			selection, _, err := env.svc.SelectAccountWithScheduler(ctx, &gid, "", "capability", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
			require.Nil(t, selection)
			require.Error(t, err)
			require.Equal(t, !mixed, IsCapabilityOnlySelectionError(err))
		})
	}
}
