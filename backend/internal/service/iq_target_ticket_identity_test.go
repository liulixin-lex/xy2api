package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/stretchr/testify/require"
)

func TestIQTicketIdentityForResultTracksOnlyEffectiveJudgments(t *testing.T) {
	unknown, judged := iqTicketIdentityForResult(iqcheck.Result{
		Status: "unknown", StateTicketID: "ignored", StateFingerprint: "ignored-fingerprint",
	})
	require.False(t, judged)
	require.Empty(t, unknown)

	got, judged := iqTicketIdentityForResult(iqcheck.Result{
		Status: "degraded", StateTicketID: "ticket", StateFingerprint: "fingerprint",
		StateRevision: "revision", StateModel: "gpt-6-astra",
		Diagnostic: &iqcheck.Diagnostic{StateProtection: "protected"},
	})
	require.True(t, judged)
	require.Equal(t, IQTicketIdentity{
		StateTicketID: "ticket", StateFingerprint: "fingerprint", StateRevision: "revision",
		StateModel: "gpt-6-astra", StateProtection: "protected",
	}, got)
	require.Equal(t, "model_mismatch", iqTargetRoundReason("degraded", []string{"wrong_answer", "model_mismatch"}))
}

func TestCodexTicketIQIdentitiesCurrentRejectsAnyStaleQuestion(t *testing.T) {
	now := time.Now()
	a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{}}
	require.True(t, CodexTicketIQIdentitiesCurrent(a, nil, now))
	require.False(t, CodexTicketIQIdentitiesCurrent(a, []IQTicketIdentity{{
		StateTicketID: "previous-ticket", StateModel: "gpt-6-astra",
	}}, now))
}
