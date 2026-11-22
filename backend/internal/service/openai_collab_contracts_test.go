//go:build unit

package service

import (
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabContractDigests(t *testing.T) {
	a := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-test-secret"}}}
	b := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-test-secret"}}}
	first := a.CollabDigest(resilience.CollabDigestDomainCiphertext, "synthetic bytes")
	require.NotEmpty(t, first)
	require.Equal(t, first, b.CollabDigest(resilience.CollabDigestDomainCiphertext, "synthetic bytes"))
	require.NotEqual(t, first, a.CollabDigest(resilience.CollabDigestDomainArguments, "synthetic bytes"))
	require.NotEqual(t, first, a.CollabDigest(resilience.CollabDigestDomainCiphertext, " synthetic bytes"))
	require.NotEqual(t, a.CollabDigest(resilience.CollabDigestDomainCiphertext, string([]byte{0xff})), a.CollabDigest(resilience.CollabDigestDomainCiphertext, string([]byte{0xfe})))
	require.NotEqual(t, a.CollabDigest(resilience.CollabDigestDomainItem, "ab", "c"), a.CollabDigest(resilience.CollabDigestDomainItem, "a", "bc"))
	var absent *OpenAIGatewayService
	require.Empty(t, absent.CollabDigest(resilience.CollabDigestDomainItem, "synthetic"))
	require.Empty(t, (&OpenAIGatewayService{}).CollabDigest(resilience.CollabDigestDomainItem, "synthetic"))
	require.Empty(t, a.CollabDigest("unknown", "synthetic"))
}

func TestCollabSharedPlanSeparatesHistory(t *testing.T) {
	plan := CollabPlan{HistoricalTools: map[string]struct{}{"send_message": {}}, EffectiveTools: map[string]CollabToolState{"send_message": {Available: false, Type: CollabToolTypeFunction, HasMessage: true}}}
	copied := plan.Clone()
	delete(copied.HistoricalTools, "send_message")
	copied.EffectiveTools["send_message"] = CollabToolState{Available: true}
	require.Contains(t, plan.HistoricalTools, "send_message")
	require.False(t, plan.EffectiveTools["send_message"].Available)
}
