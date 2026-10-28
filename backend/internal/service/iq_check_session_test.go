package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestIQOAuthProbeRespectsFingerprintConvergence(t *testing.T) {
	account := newTestOAuthAccount(1, map[string]any{codexFingerprintModeExtraKey: "session"})
	account.Credentials = map[string]any{"access_token": "fixture"}
	transport := &targetProbeTransport{status: 503, body: `{}`}
	service := &IQCheckService{accounts: &targetProbeAccounts{account: account}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
	for range 2 {
		result := service.probe(context.Background(), account.ID)
		require.Equal(t, "unknown", result.Status)
	}
	require.NotEmpty(t, transport.request.Header.Get("session-id"))
	require.Equal(t, transport.request.Header.Get("session-id"), transport.request.Header.Get("session_id"))
	require.NotEmpty(t, transport.request.Header.Get("x-codex-installation-id"))
	metadata, ok := transport.payload["client_metadata"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, transport.request.Header.Get("session-id"), metadata["session_id"])
	require.NotContains(t, transport.payload, "prompt_cache_key", "API cache rotation is not used by OAuth probes")
}
