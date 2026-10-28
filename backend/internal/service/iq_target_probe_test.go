package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type targetProbeAccounts struct {
	AccountRepository
	account *Account
}

func (r *targetProbeAccounts) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

type targetProbeTransport struct {
	status       int
	body         string
	headers      http.Header
	request      *http.Request
	payload      map[string]any
	waitFirst    bool
	firstStarted chan struct{}
	calls        int
}

func (u *targetProbeTransport) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.respond(req)
}
func (u *targetProbeTransport) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.respond(req)
}
func (u *targetProbeTransport) respond(req *http.Request) (*http.Response, error) {
	u.calls++
	u.request = req
	var payload map[string]any
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		return nil, err
	}
	u.payload = payload
	if u.waitFirst && u.calls == 1 {
		if u.firstStarted != nil {
			close(u.firstStarted)
		}
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	headers := u.headers
	if headers == nil {
		headers = make(http.Header)
	}
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json")
	}
	return &http.Response{StatusCode: u.status, Header: headers, Body: io.NopCloser(strings.NewReader(u.body))}, nil
}

func TestIQTargetProbeUsesCanonicalModelAndAccountSpecificDeclarationSource(t *testing.T) {
	challenge := iqcheck.NewChallenge()
	answer := fmt.Sprintf(`{"answer":%d}`, challenge.Answer())
	for _, tc := range []struct {
		name       string
		kind       string
		wantStatus string
		wantModel  string
	}{
		{name: "oauth prefers response header", kind: AccountTypeOAuth, wantStatus: "degraded", wantModel: "gpt-5.6-luna"},
		{name: "api key prefers response body", kind: AccountTypeAPIKey, wantStatus: "smart", wantModel: "gpt-6-astra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{
				ID: 1, Platform: PlatformOpenAI, Type: tc.kind,
				Credentials: map[string]any{"api_key": "fixture", "access_token": "fixture", "base_url": "https://example.test", "model_mapping": map[string]any{"public-alias": "gpt-6-astra"}},
				Extra:       map[string]any{"openai_responses_mode": "force_chat_completions"},
			}
			responseBody := fmt.Sprintf(`{"model":"gpt-6-astra","choices":[{"index":0,"message":{"content":%q},"finish_reason":"stop"}]}`, answer)
			if tc.kind == AccountTypeOAuth {
				responseBody = fmt.Sprintf(`{"status":"completed","model":"gpt-6-astra","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]}`, answer)
			}
			transport := &targetProbeTransport{
				status:  http.StatusOK,
				headers: http.Header{"Content-Type": {"application/json"}, "Openai-Model": {"gpt-5.6-luna"}},
				body:    responseBody,
			}
			svc := &IQCheckService{accounts: &targetProbeAccounts{account: account}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
			outboundModel := CanonicalIQOutboundModel(account, "public-alias")
			result := svc.probe(context.Background(), account.ID, iqProbeRequest{
				TargetModel: outboundModel, TargetEffort: "low", Challenge: challenge,
				Profile: iqProbeProfile{Model: outboundModel, ReasoningEffort: "low"},
			})
			require.Equal(t, "gpt-6-astra", transport.payload["model"])
			require.Equal(t, tc.wantStatus, result.Status)
			require.Equal(t, tc.wantModel, result.ReportedModel)
			if tc.kind == AccountTypeOAuth {
				require.Equal(t, "model_mismatch", result.Reason)
				require.NotNil(t, result.Diagnostic.ModelIdentity)
				require.Contains(t, result.Diagnostic.ModelIdentity.Flags, "source_disagreement")
			}
		})
	}
}
