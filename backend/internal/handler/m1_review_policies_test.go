//go:build unit

package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type m1ReviewPolicyUpstream struct {
	body  []byte
	calls int
}

func (u *m1ReviewPolicyUpstream) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	var err error
	u.body, err = io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	u.calls++
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}` + "\n\ndata: [DONE]\n\n"))}, nil
}
func (u *m1ReviewPolicyUpstream) DoWithTLS(r *http.Request, p string, a int64, c int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, a, c)
}
func m1ReviewPolicyFixture(t *testing.T, typ string) (*service.OpenAIGatewayService, *service.Account, *gin.Context, *m1ReviewPolicyUpstream) {
	t.Helper()
	up := &m1ReviewPolicyUpstream{}
	cfg := &config.Config{}
	svc := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, up, nil, nil, nil, nil, nil, nil, nil, nil)
	svc.StopOpenAICodexTicketHarvester()
	account := &service.Account{ID: 11, Platform: service.PlatformOpenAI, Type: typ, Concurrency: 1, Credentials: map[string]any{"access_token": "oauth-test", "api_key": "fixture", "chatgpt_account_id": "test-account"}, Extra: map[string]any{"use_responses_api": true}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	service.SetOpenAIClientTransport(c, service.OpenAIClientTransportHTTP)
	return svc, account, c, up
}
func TestM1ReviewMessagesDefaultPolicy(t *testing.T) {
	for _, typ := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		for _, mode := range []string{"downgrade", "deny", "mapping-deny", "mapping-high"} {
			svc, account, c, up := m1ReviewPolicyFixture(t, typ)
			key := &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI, MaxReasoningEffort: "low", MaxReasoningEffortOverLimit: mode}}
			if strings.HasPrefix(mode, "mapping-") {
				key.Group.MaxReasoningEffort = ""
				key.Group.ReasoningEffortMappings = []service.ReasoningEffortMapping{{From: "medium", To: strings.TrimPrefix(mode, "mapping-")}}
			}
			body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
			bindOpenAIReasoningEffortPolicyForMessagesRequest(c, key, body)
			result, err := svc.ForwardAsAnthropic(c.Request.Context(), c, account, body, "", "")
			require.NoError(t, err, typ+mode)
			require.NotNil(t, result)
			require.Equal(t, 1, up.calls)
			require.Equal(t, "medium", gjson.GetBytes(up.body, "reasoning.effort").String())
		}
	}
}
