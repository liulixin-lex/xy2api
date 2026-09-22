package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func requireIQProbePayloadIsolation(t *testing.T, account *Account, bodies []map[string]any) {
	t.Helper()
	if account.Type != AccountTypeAPIKey {
		require.Equal(t, bodies[0], bodies[1])
		return
	}
	seen := map[string]bool{}
	var original map[string]any
	for _, body := range bodies {
		key, ok := body["prompt_cache_key"].(string)
		require.True(t, ok)
		_, err := uuid.Parse(key)
		require.NoError(t, err)
		require.False(t, seen[key], "each actual attempt must get a fresh cache identity")
		seen[key] = true
		business := make(map[string]any, len(body))
		for k, v := range body {
			if k != "prompt_cache_key" {
				business[k] = v
			}
		}
		if original != nil {
			require.Equal(t, original, business, "question, effort, format and model must stay unchanged")
		}
		original = business
	}
}

func TestIQAPIProbeFreshSessionAfterOverridesAndMapping(t *testing.T) {
	for _, mode := range []string{"force_responses", "force_chat_completions"} {
		t.Run(mode, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "fixture", "header_override_enabled": true,
					"header_overrides": map[string]any{"session_id": "fixed", "SESSION-ID": "fixed", "x-session-affinity": "fixed", "x-opencode-session": "fixed", "x-codex-turn-state": "fixed", "x-codex-turn-metadata": `{"session_id":"fixed"}`, "x-test-preserved": "keep", "idempotency-key": "fixed", "X-Idempotency-Key": "fixed"},
					"model_mapping":    map[string]any{"gpt-6-astra": "gpt-5.6-sol"}},
				Extra: map[string]any{"openai_responses_mode": mode}, IQCheck: domain.IQCheck{Model: "gpt-6-astra"}}
			u := &iqProbeTransport{status: 503, body: `{}`}
			gateway := &OpenAIGatewayService{cfg: &config.Config{}}
			s := &IQCheckService{accounts: &iqProbeAccounts{account: a}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: u, openaiGatewayService: gateway}}
			// Reusing a claim simulates an initial attempt, persisted retry and a later round.
			claim := IQCheckClaim{AccountID: 1, Profile: a.IQCheck.Profile()}
			for range 3 {
				require.Equal(t, "unknown", s.probe(context.Background(), 1, claim).Status)
			}
			require.Len(t, u.requests, 3, "identity isolation must not add upstream calls")
			requireIQProbePayloadIsolation(t, a, u.bodies)
			for i, req := range u.requests {
				key := u.bodies[i]["prompt_cache_key"]
				for _, name := range []string{"session_id", "conversation_id", "session-id", "x-session-id", "x-session-affinity", "x-conversation-id"} {
					require.Equal(t, key, req.Header.Get(name), name)
				}
				for _, name := range []string{"x-opencode-session", "x-codex-turn-state", "x-codex-turn-metadata", "previous_response_id"} {
					require.Empty(t, req.Header.Get(name), name)
				}
				require.Equal(t, "keep", req.Header["x-test-preserved"][0])
				require.Equal(t, "Bearer fixture", req.Header.Get("Authorization"))
				require.Equal(t, "gpt-5.6-sol", u.bodies[i]["model"])
				for name := range req.Header {
					require.NotContains(t, []string{"idempotency-key", "x-idempotency-key"}, strings.ToLower(name))
				}
				require.Nil(t, req.GetBody)
				require.True(t, HTTPUpstreamSingleAttempt(req.Context()))
				require.NotContains(t, u.bodies[i], "previous_response_id")
			}
		})
	}
}

func TestIQOAuthProbeRespectsFingerprintConvergence(t *testing.T) {
	for _, mode := range []string{"off", "device", "session", "full"} {
		t.Run(mode, func(t *testing.T) {
			a := newTestOAuthAccount(1, map[string]any{codexFingerprintModeExtraKey: mode})
			a.Credentials = map[string]any{"access_token": "fixture"}
			u := &iqProbeTransport{status: 503, body: `{}`}
			s := &IQCheckService{accounts: &iqProbeAccounts{account: a}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: u}}
			for range 2 {
				s.probe(context.Background(), 1)
			}
			require.Len(t, u.requests, 2)
			for i, req := range u.requests {
				require.NotContains(t, u.bodies[i], "prompt_cache_key", "API rotation must never enter OAuth")
				if mode == "off" {
					require.Empty(t, req.Header.Get("session-id"))
					require.NotContains(t, u.bodies[i], "client_metadata")
					continue
				}
				metadata, ok := u.bodies[i]["client_metadata"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, req.Header.Get("x-codex-installation-id"), metadata["x-codex-installation-id"])
				require.Equal(t, u.requests[0].Header.Get("x-codex-installation-id"), req.Header.Get("x-codex-installation-id"))
				if mode == "device" {
					require.Empty(t, req.Header.Get("session-id"))
					continue
				}
				require.NotEmpty(t, req.Header.Get("session-id"))
				require.Equal(t, u.requests[0].Header.Get("session-id"), req.Header.Get("session-id"))
				require.Equal(t, req.Header.Get("session-id"), metadata["session_id"])
				require.Equal(t, req.Header.Get("x-client-request-id"), metadata["thread_id"])
				require.NotEmpty(t, metadata["turn_id"])
				if i > 0 {
					first, ok := u.bodies[0]["client_metadata"].(map[string]any)
					require.True(t, ok)
					require.NotEqual(t, first["turn_id"], metadata["turn_id"])
				}
			}
		})
	}
}
