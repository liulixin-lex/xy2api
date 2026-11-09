//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHaiku55MessagesRules(t *testing.T) {
	for _, tc := range []struct{ name, body, typ, want string }{
		{"text prefill", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"prefix"}]}`, AccountTypeAPIKey, "assistant prefill"},
		{"block prefill thinking off", `{"thinking":{"type":"disabled"},"messages":[{"role":"assistant","content":[{"type":"text","text":"prefix"}]}]}`, AccountTypeOAuth, "assistant prefill"},
		{"user after assistant", `{"messages":[{"role":"assistant","content":"done"},{"role":"user","content":"next"}]}`, AccountTypeAPIKey, ""},
		{"tool continuation", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_x","name":"lookup","input":{}}]}]}`, AccountTypeAPIKey, ""},
		{"signed continuation", `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"text","text":"continue"}]}]}`, AccountTypeAPIKey, ""},
		{"credit continuation", `{"fallback_credit_token":"fixture","messages":[{"role":"assistant","content":"continue"}]}`, AccountTypeAPIKey, ""},
		{"legacy 0124", `{"tools":[{"type":"computer_20250124","name":"computer"}]}`, AccountTypeAPIKey, "computer_20250124"},
		{"legacy 0124 bedrock", `{"tools":[{"type":"computer_20250124","name":"computer"}]}`, AccountTypeBedrock, "computer_20250124"},
		{"legacy 1124", `{"tools":[{"type":"computer_20251124","name":"computer"}]}`, AccountTypeAPIKey, "computer_20251124"},
		{"bedrock 1124 accepted", `{"tools":[{"type":"computer_20251124","name":"computer"}]}`, AccountTypeBedrock, ""},
		{"vertex 1124 deferred", `{"tools":[{"type":"computer_20251124","name":"computer"}]}`, AccountTypeServiceAccount, ""},
		{"toolset", `{"tools":[{"type":"computer_toolset_20260801"}]}`, AccountTypeAPIKey, ""},
		{"custom computer", `{"tools":[{"name":"computer","input_schema":{"type":"object"}}]}`, AccountTypeAPIKey, ""},
		{"unknown version", `{"tools":[{"type":"computer_20990101","name":"computer"}]}`, AccountTypeAPIKey, ""},
		{"old 1022 deferred", `{"tools":[{"type":"computer_20241022","name":"computer"}]}`, AccountTypeAPIKey, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{Platform: PlatformAnthropic, Type: tc.typ}
			for _, model := range []string{"claude-haiku-5-5", "anthropic.claude-haiku-5-5"} {
				err := validateClaude55RequestForAccount([]byte(tc.body), model, account)
				if tc.want == "" {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, tc.want)
				}
			}
			for _, model := range []string{"claude-haiku-4-5", "claude-haiku-5-5-preview", "custom-haiku"} {
				require.NoError(t, validateClaude55RequestForAccount([]byte(tc.body), model, account))
			}
		})
	}
}

func TestHaiku55MessagesRulesRejectBeforeUpstream(t *testing.T) {
	for _, typ := range []string{AccountTypeAPIKey, AccountTypeOAuth, AccountTypeSetupToken, AccountTypeBedrock} {
		for _, payload := range []string{
			`"messages":[{"role":"assistant","content":"prefix"}]`,
			`"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"computer_20250124","name":"computer"}]`,
		} {
			t.Run(typ+payload, func(t *testing.T) {
				account := &Account{ID: 1, Platform: PlatformAnthropic, Type: typ, Credentials: map[string]any{"model_mapping": map[string]any{"public-haiku": "claude-haiku-5-5"}}}
				model := "claude-haiku-5-5"
				if typ == AccountTypeAPIKey || typ == AccountTypeBedrock {
					model = "public-haiku"
				}
				raw := []byte(`{"model":"` + model + `","max_tokens":64,` + payload + `}`)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				upstream := &anthropicHTTPUpstreamRecorder{}
				svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}
				_, err := svc.Forward(context.Background(), c, account, &ParsedRequest{Model: model, Body: NewRequestBodyRef(raw)})
				require.Error(t, err)
				require.Equal(t, http.StatusBadRequest, c.Writer.Status())
				require.Nil(t, upstream.lastReq)
				events, _ := c.Get(OpsUpstreamErrorsKey)
				require.Empty(t, events)
				require.Empty(t, c.GetString(OpsUpstreamErrorMessageKey))
				t.Logf("status=%d upstream_called=%t error=%v", c.Writer.Status(), upstream.lastReq != nil, err)
			})
		}
	}
}

func TestHaiku55BridgePrefillRejectsConvertedMessages(t *testing.T) {
	for _, chat := range []bool{false, true} {
		body := `{"model":"public-haiku","input":[{"type":"message","role":"assistant","content":"prefix"}]}`
		if chat {
			body = `{"model":"public-haiku","messages":[{"role":"assistant","content":"prefix"}]}`
		}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(namespaceToolAnthropicStream()))}}
		svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}
		account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"public-haiku": "claude-haiku-5-5"}}}
		var err error
		if chat {
			_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(body), nil)
		} else {
			_, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(body), nil)
		}
		require.ErrorContains(t, err, "assistant prefill")
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Nil(t, upstream.lastReq)
	}
}

func TestHaiku55BedrockOfficialModelID(t *testing.T) {
	for _, region := range []string{"us-east-1", "eu-west-1", "ap-southeast-2"} {
		a := &Account{Platform: PlatformAnthropic, Type: AccountTypeBedrock, Credentials: map[string]any{"aws_region": region, "aws_force_global": "true"}}
		id, ok := ResolveBedrockModelID(a, "claude-haiku-5-5")
		require.True(t, ok)
		require.Equal(t, "anthropic.claude-haiku-5-5", id)
	}
}

func TestHaiku55CountTokensDoesNotApplyGenerationRules(t *testing.T) {
	raw := []byte(`{"model":"claude-haiku-5-5","messages":[{"role":"assistant","content":"prefix"}],"tools":[{"type":"computer_20250124","name":"computer"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":42}`))}}
	cfg := &config.Config{}
	svc := &GatewayService{cfg: cfg, httpUpstream: upstream, responseHeaderFilter: compileResponseHeaderFilter(cfg), rateLimitService: &RateLimitService{}}
	a := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}, Extra: map[string]any{"anthropic_passthrough": true}}
	require.NoError(t, svc.ForwardCountTokens(context.Background(), c, a, &ParsedRequest{Model: "claude-haiku-5-5", Body: NewRequestBodyRef(raw)}))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, upstream.lastReq)
}

func TestHaiku55BedrockLegacy1124ForwardAllowed(t *testing.T) {
	raw := []byte(`{"model":"claude-haiku-5-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"computer_20251124","name":"computer","display_width_px":1024,"display_height_px":768}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("anthropic-beta", "computer-use-2025-11-24")
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))}}
	cfg := &config.Config{}
	svc := &GatewayService{cfg: cfg, httpUpstream: upstream, responseHeaderFilter: compileResponseHeaderFilter(cfg), rateLimitService: &RateLimitService{}}
	a := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeBedrock, Credentials: map[string]any{"auth_mode": "apikey", "api_key": "fixture", "aws_region": "us-east-1"}}
	_, err := svc.Forward(context.Background(), c, a, &ParsedRequest{Model: "claude-haiku-5-5", Body: NewRequestBodyRef(raw)})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "computer_20251124", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Contains(t, upstream.lastReq.URL.Path, "anthropic.claude-haiku-5-5")
	t.Logf("status=%d upstream_called=%t tool=%s", rec.Code, upstream.lastReq != nil, gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
}
