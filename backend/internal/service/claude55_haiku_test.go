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

func TestValidateClaude55RequestHaiku(t *testing.T) {
	accepted := []string{
		``,
		`,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}`,
		`,"thinking":{"type":"disabled"}`,
		`,"thinking":{"type":"disabled"},"output_config":{"effort":"high"}`,
		`,"tool_choice":{"type":"any"}`,
		`,"tool_choice":{"type":"tool","name":"lookup"}`,
		`,"temperature":1`,
		`,"top_p":0.99`,
	}
	for _, field := range accepted {
		body := []byte(`{"model":"claude-haiku-5-5","messages":[]` + field + `}`)
		require.NoError(t, validateClaude55Request(body, "claude-haiku-5-5"), field)
	}
	rejected := map[string]string{
		`,"thinking":{"type":"enabled","budget_tokens":1024}`:                "requires adaptive thinking",
		`,"thinking":{"type":"disabled"},"output_config":{"effort":"xhigh"}`: "low, medium or high effort",
		`,"thinking":{"type":"disabled","block_binding":{}}`:                 "block_binding",
		`,"thinking":{"type":"between_tools"}`:                               "between_tools",
		`,"temperature":0.2`:                                                 "non-default temperature",
		`,"top_p":1`:                                                         "non-default top_p",
		`,"top_k":5`:                                                         "top_k",
		`,"temperature":1,"top_p":0.99`:                                      "together",
	}
	for field, want := range rejected {
		body := []byte(`{"model":"claude-haiku-5-5","messages":[]` + field + `}`)
		require.ErrorContains(t, validateClaude55Request(body, "anthropic.claude-haiku-5-5"), want, field)
	}

	// Neighbouring models keep their rules.
	require.NoError(t, validateClaude55Request([]byte(`{"thinking":{"type":"enabled","budget_tokens":1024},"temperature":0.2,"top_k":5}`), "claude-haiku-4-5"))
	require.ErrorContains(t, validateClaude55Request([]byte(`{"tool_choice":{"type":"any"}}`), "claude-opus-5-5"), "forced tool_choice")
	require.ErrorContains(t, validateClaude55Request([]byte(`{"thinking":{"type":"disabled"}}`), "claude-opus-5-5"), "claude-opus-5-5 requires adaptive thinking")
	require.ErrorContains(t, validateClaude55Request([]byte(`{"tool_choice":{"type":"any"}}`), "claude-sonnet-5-5"), "forced tool_choice")
	require.NoError(t, validateClaude55Request([]byte(`{"temperature":1,"top_p":0.99}`), "claude-sonnet-5-5"))
}

func TestHaiku55NativeMessagesRejectsInvalidParametersLocally(t *testing.T) {
	for _, field := range []string{
		`"thinking":{"type":"enabled","budget_tokens":1024}`,
		`"temperature":0.7`,
		`"temperature":1,"top_p":0.99`,
		`"top_k":1`,
	} {
		for _, count := range []bool{false, true} {
			model := "public-haiku"
			account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"model_mapping": map[string]any{model: "claude-haiku-5-5"}}}
			body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hello"}],` + field + `}`)
			parsed := &ParsedRequest{Model: model, Body: NewRequestBodyRef(body)}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			svc := &GatewayService{}
			var err error
			if count {
				err = svc.ForwardCountTokens(context.Background(), c, account, parsed)
			} else {
				_, err = svc.Forward(context.Background(), c, account, parsed)
			}
			require.Error(t, err, field)
			require.Equal(t, http.StatusBadRequest, rec.Code, field)
			require.Contains(t, rec.Body.String(), "invalid_request_error", field)
			require.Contains(t, rec.Body.String(), "claude-haiku-5-5", field)
		}
	}
}

func TestHaiku55BridgeConvertsToAdaptiveThinking(t *testing.T) {
	run := func(t *testing.T, chat bool, model, body string) (*httptest.ResponseRecorder, *anthropicHTTPUpstreamRecorder, error) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(namespaceToolAnthropicStream()))}}
		svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}
		account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-key", "model_mapping": map[string]any{"public-claude": model}}}
		var err error
		if chat {
			_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(body), nil)
		} else {
			_, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(body), nil)
		}
		return rec, upstream, err
	}
	for _, chat := range []bool{false, true} {
		body := `{"model":"public-claude","input":"hello","reasoning":{"effort":"xhigh"},"tool_choice":"required","tools":[{"type":"function","name":"lookup","function":{"name":"lookup"}}]}`
		invalid := `{"model":"public-claude","input":"hello","temperature":0.7}`
		if chat {
			body = `{"model":"public-claude","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"xhigh","tool_choice":"required","tools":[{"type":"function","function":{"name":"lookup"}}]}`
			invalid = `{"model":"public-claude","messages":[{"role":"user","content":"hello"}],"temperature":0.7}`
		}
		_, upstream, err := run(t, chat, "claude-haiku-5-5", body)
		require.NoError(t, err)
		require.NotNil(t, upstream.lastReq)
		require.Equal(t, "claude-haiku-5-5", gjson.GetBytes(upstream.lastBody, "model").String())
		require.Equal(t, "adaptive", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
		require.Equal(t, "xhigh", gjson.GetBytes(upstream.lastBody, "output_config.effort").String())
		require.False(t, gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Exists())
		require.Equal(t, "any", gjson.GetBytes(upstream.lastBody, "tool_choice.type").String(), "Haiku 5.5 accepts forced tool use")

		rec, upstream, err := run(t, chat, "claude-haiku-5-5", invalid)
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "non-default temperature")
		require.Nil(t, upstream.lastReq)

		// Haiku 4.5 keeps the manual thinking budget.
		_, upstream, err = run(t, chat, "claude-haiku-4-5", strings.Replace(body, `"xhigh"`, `"high"`, 1))
		require.NoError(t, err)
		require.Equal(t, "enabled", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
		require.True(t, gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Exists())
	}
}

func TestClaudeOAuthMimicryDoesNotPairTemperatureWithTopP(t *testing.T) {
	for _, model := range []string{"claude-haiku-5-5", "claude-sonnet-5-5", "claude-sonnet-4-6", "claude-opus-5-5"} {
		withTopP, _ := normalizeClaudeOAuthRequestBody([]byte(`{"model":"`+model+`","messages":[],"top_p":0.99}`), model, claudeOAuthNormalizeOptions{})
		require.False(t, gjson.GetBytes(withTopP, "temperature").Exists(), model)
		require.Equal(t, 0.99, gjson.GetBytes(withTopP, "top_p").Float(), model)

		plain, _ := normalizeClaudeOAuthRequestBody([]byte(`{"model":"`+model+`","messages":[]}`), model, claudeOAuthNormalizeOptions{})
		if model == "claude-opus-5-5" {
			require.False(t, gjson.GetBytes(plain, "temperature").Exists(), "Opus 5.5 never gets an injected temperature")
		} else {
			require.Equal(t, int64(1), gjson.GetBytes(plain, "temperature").Int(), model)
		}
	}
	// The tool_choice exception for an empty tool list stays limited to Opus and Sonnet 5.5.
	haiku, _ := normalizeClaudeOAuthRequestBody([]byte(`{"model":"claude-haiku-5-5","messages":[],"tool_choice":{"type":"auto"}}`), "claude-haiku-5-5", claudeOAuthNormalizeOptions{})
	require.False(t, gjson.GetBytes(haiku, "tool_choice").Exists())
	sonnet, _ := normalizeClaudeOAuthRequestBody([]byte(`{"model":"claude-sonnet-5-5","messages":[],"tool_choice":{"type":"auto"}}`), "claude-sonnet-5-5", claudeOAuthNormalizeOptions{})
	require.True(t, gjson.GetBytes(sonnet, "tool_choice").Exists())
}

func TestHaiku55OAuthForwardSendsTopPWithoutTemperature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		svc, _, base := cacheFixture(t)
		account := *base
		account.Type = typ
		account.Credentials = map[string]any{"access_token": "fixture"}
		raw := []byte(`{"model":"claude-haiku-5-5","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"top_p":0.99}`)
		parsed, err := ParseGatewayRequest(NewRequestBodyRef(raw), PlatformAnthropic)
		require.NoError(t, err)
		upstream := ttlWireUpstream(false)
		svc.httpUpstream = upstream
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		_, err = svc.Forward(context.Background(), c, &account, parsed)
		require.NoError(t, err, typ)
		require.NotNil(t, upstream.lastReq, typ)
		require.Equal(t, 0.99, gjson.GetBytes(upstream.lastBody, "top_p").Float(), typ)
		require.False(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists(), typ)
	}
}
