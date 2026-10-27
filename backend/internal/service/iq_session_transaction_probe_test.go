package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type iqTargetProbeSeriesTransport struct {
	HTTPUpstream
	requests []*http.Request
	bodies   []map[string]any
}

func (u *iqTargetProbeSeriesTransport) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.respond(req)
}
func (u *iqTargetProbeSeriesTransport) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.respond(req)
}
func (u *iqTargetProbeSeriesTransport) respond(req *http.Request) (*http.Response, error) {
	u.requests = append(u.requests, req)
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	u.bodies = append(u.bodies, body)
	return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}

func TestIQAPIProbeUsesFreshSessionAfterOverridesAndMapping(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "fixture", "header_override_enabled": true,
			"header_overrides": map[string]any{
				"session_id": "stale", "SESSION-ID": "stale", "x-session-affinity": "stale",
				"x-opencode-session": "stale", "x-codex-turn-state": "stale", "idempotency-key": "stale",
				"x-test-preserved": "keep",
			},
			"model_mapping": map[string]any{"gpt-6-astra": "gpt-5.6-sol"},
		},
		Extra: map[string]any{"openai_responses_mode": "force_responses"},
	}
	transport := &iqTargetProbeSeriesTransport{}
	service := &IQCheckService{accounts: &targetProbeAccounts{account: account}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
	challenge := iqcheck.NewChallenge()
	for range 3 {
		result := service.probe(context.Background(), account.ID, iqProbeRequest{
			TargetModel: domain.IQModelAstra, TargetEffort: "low", Challenge: challenge,
			Profile: iqProbeProfile{Model: domain.IQModelAstra, ReasoningEffort: "low"},
		})
		require.Equal(t, "unknown", result.Status)
	}
	require.Len(t, transport.requests, 3)
	seen := map[string]bool{}
	for i, req := range transport.requests {
		cacheKey, ok := transport.bodies[i]["prompt_cache_key"].(string)
		require.True(t, ok)
		require.NotEmpty(t, cacheKey)
		require.False(t, seen[cacheKey], "each physical probe gets a fresh cache identity")
		seen[cacheKey] = true
		require.Equal(t, "gpt-5.6-sol", transport.bodies[i]["model"])
		for _, name := range []string{"session_id", "conversation_id", "session-id", "x-session-id", "x-session-affinity", "x-conversation-id"} {
			require.Equal(t, cacheKey, req.Header.Get(name), name)
		}
		for _, name := range []string{"x-opencode-session", "x-codex-turn-state", "previous_response_id", "idempotency-key", "x-idempotency-key"} {
			require.Empty(t, req.Header.Get(name), name)
		}
		require.Equal(t, []string{"keep"}, req.Header[resolveWireCasing("x-test-preserved")])
		require.Equal(t, "Bearer fixture", req.Header.Get("Authorization"))
		require.Nil(t, req.GetBody)
		require.True(t, HTTPUpstreamSingleAttempt(req.Context()))
	}
}
