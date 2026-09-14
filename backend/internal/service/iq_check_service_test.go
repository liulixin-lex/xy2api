package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type iqProbeAccounts struct {
	AccountRepository
	account *Account
}

func (r *iqProbeAccounts) GetByID(context.Context, int64) (*Account, error) { return r.account, nil }

type iqProbeTransport struct {
	HTTPUpstream
	status   int
	body     string
	failure  error
	requests []*http.Request
	bodies   []map[string]any
}

func (u *iqProbeTransport) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.requests = append(u.requests, req)
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	u.bodies = append(u.bodies, body)
	if u.failure != nil {
		return nil, u.failure
	}
	return &http.Response{StatusCode: u.status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(u.body))}, nil
}
func TestIQCheckProbe(t *testing.T) {
	for _, tc := range []struct {
		name         string
		httpStatus   int
		body, status string
		failure      error
	}{
		{"smart", 200, `{"choices":[{"index":0,"message":{"content":"{\"answer\":21}"},"finish_reason":"stop"}]}`, "smart", nil},
		{"explained", 200, `{"choices":[{"index":0,"message":{"content":"答案是21。解释如下。"},"finish_reason":"stop"}]}`, "smart", nil},
		{"wrong", 200, `{"choices":[{"index":0,"message":{"content":"29"},"finish_reason":"stop"}]}`, "degraded", nil},
		{"unauthorized", 401, "", "unknown", nil}, {"quota", 429, "", "unknown", nil}, {"server", 503, "", "unknown", nil}, {"unsupported", 400, "", "unknown", nil}, {"empty", 200, "{}", "unknown", nil}, {"timeout", 0, "", "unknown", context.DeadlineExceeded}, {"disconnected", 0, "", "unknown", io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Extra: map[string]any{"openai_responses_mode": "force_chat_completions"}, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://example.test", "model_mapping": map[string]any{iqcheck.Model: "other-model"}}}
			transport := &iqProbeTransport{status: tc.httpStatus, body: tc.body, failure: tc.failure}
			s := &IQCheckService{accounts: &iqProbeAccounts{account: a}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
			for i := 0; i < 2; i++ {
				result := s.probe(context.Background(), 1)
				require.Equal(t, tc.status, result.Status, result)
				require.Equal(t, iqcheck.Model, transport.bodies[i]["model"])
				require.Equal(t, false, transport.bodies[i]["store"])
				require.Equal(t, iqcheck.Effort, transport.bodies[i]["reasoning_effort"])
				require.NotContains(t, transport.bodies[i], "previous_response_id")
				require.NotContains(t, transport.bodies[i], "conversation")
			}
			require.Equal(t, transport.bodies[0], transport.bodies[1])
			require.Equal(t, StatusActive, a.Status)
			require.True(t, a.Schedulable)
		})
	}
}
func TestIQCheckSchedulingGate(t *testing.T) {
	a := Account{Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, IQCheck: domain.IQCheck{Enabled: true, Status: "smart"}}
	require.True(t, a.IsSchedulable())
	a.IQCheck.Status = "degraded"
	require.False(t, a.IsSchedulable())
	require.False(t, a.IsCredentialUsableForShadow())
	a.IQCheck.Status = "smart"
	require.True(t, a.IsSchedulable())
	a.IQCheck.Status = "unknown"
	require.True(t, a.IsSchedulable())
	a.Schedulable = false
	require.False(t, a.IsSchedulable())
	a.IQCheck.Enabled = false
	require.False(t, a.IsSchedulable())
	a.Schedulable = true
	a.Status = "inactive"
	require.False(t, a.IsSchedulable())
	a.Status = StatusActive
	future := time.Now().Add(time.Hour)
	a.RateLimitResetAt = &future
	require.False(t, a.IsSchedulable())
	a.RateLimitResetAt = nil
	past := time.Now().Add(-time.Hour)
	a.ExpiresAt = &past
	a.AutoPauseOnExpired = true
	require.False(t, a.IsSchedulable())
}

func TestIQCheckOAuthPayloadAndStaleCandidate(t *testing.T) {
	for _, kind := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
		t.Run(kind, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: kind, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "fixture", "api_key": "fixture"}}
			transport := &iqProbeTransport{status: 200, body: `{"status":"completed","output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"{\"answer\":21}"}]}]}`}
			s := &IQCheckService{accounts: &iqProbeAccounts{account: a}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
			result := s.probe(context.Background(), 1)
			require.Equal(t, "smart", result.Status)
			require.Equal(t, iqcheck.Model, transport.bodies[0]["model"])
			require.Equal(t, map[string]any{"effort": iqcheck.Effort}, transport.bodies[0]["reasoning"])
			require.Empty(t, transport.requests[0].Header.Get("Session_id"))
			require.Empty(t, transport.requests[0].Header.Get("Conversation_id"))
		})
	}
	cached := Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, IQCheck: domain.IQCheck{Enabled: true, Status: "smart"}}
	fresh := cached
	fresh.IQCheck.Status = "degraded"
	gateway := &OpenAIGatewayService{accountRepo: &iqProbeAccounts{account: &fresh}, schedulerSnapshot: &SchedulerSnapshotService{}}
	require.Nil(t, gateway.recheckSelectedOpenAIAccountFromDB(context.Background(), &cached, nil, PlatformOpenAI, iqcheck.Model, false, ""))
}
