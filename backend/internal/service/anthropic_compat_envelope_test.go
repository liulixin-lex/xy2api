//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Fixtures were captured by running the adapter from e893fef5f (v0.6.3).
func TestAnthropicCompatEnvelopeGoldenMatchesAcceptedShape(t *testing.T) {
	raw, err := os.ReadFile("testdata/anthropic_compat_envelope_v063.json")
	require.NoError(t, err)
	var cases []struct{ Name, Body, System string }
	require.NoError(t, json.Unmarshal(raw, &cases))
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			body := []byte(tc.Body)
			wire, err := compatInjectBody(body, &claudeCodeCompatProfile, "derived")
			require.NoError(t, err)
			require.Equal(t, tc.System, gjson.GetBytes(wire, "system").Raw)
			for _, field := range []string{"tools", "messages"} {
				require.Equal(t, gjson.GetBytes(body, field).Raw, gjson.GetBytes(wire, field).Raw, field)
			}
			repeat, err := compatInjectBody(wire, &claudeCodeCompatProfile, "derived")
			require.NoError(t, err)
			require.Equal(t, wire, repeat)
		})
	}
}

func TestAnthropicCompatEnvelopeDropsAllBillingAndDuplicateIdentity(t *testing.T) {
	first := map[string]any{"type": "text", "text": claudeCodeSystemPrompt, "cache_control": map[string]string{"type": "ephemeral"}}
	duplicate := map[string]any{"type": "text", "text": claudeCodeSystemPrompt, "cache_control": map[string]string{"type": "transient"}}
	body, err := json.Marshal(map[string]any{"system": []any{
		map[string]string{"type": "text", "text": "business-before"},
		map[string]string{"type": "text", "text": "x-anthropic-billing-header:unknown-format"},
		first, duplicate,
		map[string]string{"type": "text", "text": "business-after"},
	}})
	require.NoError(t, err)
	wire, err := compatInjectBody(body, &claudeCodeCompatProfile, "derived")
	require.NoError(t, err)
	system := gjson.GetBytes(wire, "system").Array()
	require.Len(t, system, 4)
	require.Equal(t, claudeCodeSystemPrompt, system[1].Get("text").String())
	require.Equal(t, "ephemeral", system[1].Get("cache_control.type").String())
	require.Equal(t, "business-before", system[2].Get("text").String())
	require.Equal(t, "business-after", system[3].Get("text").String())
	retry, err := compatInjectBody(wire, &claudeCodeCompatProfile, "derived")
	require.NoError(t, err)
	require.Equal(t, gjson.GetBytes(wire, "system").Raw, gjson.GetBytes(retry, "system").Raw)
}

func TestAnthropicCompatSessionAnchorPrecedence(t *testing.T) {
	cases := []struct{ name, explicit, metadata, sticky, want string }{
		{"header", "header", "{\"session_id\":\"metadata\"}", "sticky", "header"},
		{"json", "", "{\"session_id\":\"metadata\"}", "sticky", "metadata"},
		{"legacy", "", "user_" + strings.Repeat("a", 64) + "_account__session_123e4567-e89b-12d3-a456-426614174000", "sticky", "123e4567-e89b-12d3-a456-426614174000"},
		{"sticky", "", "", "sticky", "sticky"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"metadata":{"user_id":` + mustJSONString(tc.metadata) + `}}`)
			headers := make(http.Header)
			ctx := WithAnthropicCompatRequest(context.Background(), 1, 2, body, "messages", tc.explicit, headers, "")
			ctx = WithAnthropicCompatSessionHash(ctx, tc.sticky)
			require.Equal(t, tc.want, compatSessionAnchor(ctx, compatRequest(ctx)))
		})
	}
}

func TestAnthropicCompatEveryClaudeModelUsesMappedCapability(t *testing.T) {
	account := compatTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"external-haiku": "claude-haiku-4-5-20251001", "other-vendor": "gpt-5"}
	require.True(t, anthropicCompatModelSupported(account, "external-haiku"))
	require.False(t, anthropicCompatModelSupported(account, "other-vendor"))
	require.True(t, claudeCodeCompatProfile.SupportsModel("claude-fable-5-2"))
	require.False(t, claudeCodeCompatProfile.SupportsModel("gpt-5"))
}

func TestAnthropicCompatSignatureRetryRebuildsEnvelope(t *testing.T) {
	cfg := &config.Config{}
	rectifier := `{"enabled":true,"apikey_signature_enabled":true}`
	settings := NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{SettingKeyRectifierSettings: rectifier}}, cfg)
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{
		{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}`))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"msg_retry","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))},
	}}
	svc := &GatewayService{cfg: cfg, httpUpstream: upstream, settingService: settings, tlsFPProfileService: &TLSFingerprintProfileService{}, deferredService: &DeferredService{}}
	account := compatTestAccount()
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":32,"stream":false,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"thinking","thinking":"old","signature":"bad-signature"},{"type":"text","text":"answer"}]},{"role":"user","content":"continue"}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
	ctx := WithAnthropicCompatRequest(c.Request.Context(), 11, 22, body, "messages", "session-1", c.Request.Header, "")
	c.Request = c.Request.WithContext(ctx)
	result, err := svc.Forward(ctx, c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requestBodies, 2)
	firstIdentity := gjson.GetBytes(upstream.requestBodies[0], "metadata.user_id").String()
	for _, wire := range upstream.requestBodies {
		require.True(t, strings.HasPrefix(gjson.GetBytes(wire, "system.0.text").String(), "x-anthropic-billing-header:"))
		require.Equal(t, claudeCodeSystemPrompt, gjson.GetBytes(wire, "system.1.text").String())
		require.Equal(t, firstIdentity, gjson.GetBytes(wire, "metadata.user_id").String())
	}
	require.Contains(t, gjson.GetBytes(upstream.requestBodies[0], "messages").Raw, "bad-signature")
	require.NotContains(t, gjson.GetBytes(upstream.requestBodies[1], "messages").Raw, "bad-signature")
}

func TestAnthropicCompatEnvelopeNormalizesNonObjectMetadata(t *testing.T) {
	for _, metadata := range []string{`[1]`, `[]`, `"legacy"`, `42`, `true`, `null`, `{"other":"keep"}`} {
		t.Run(metadata, func(t *testing.T) {
			body := []byte(`{"metadata":` + metadata + `,"messages":[{"role":"user","content":"hello"}]}`)
			wire, err := compatInjectBody(body, &claudeCodeCompatProfile, "derived")
			require.NoError(t, err)
			require.True(t, gjson.GetBytes(wire, "metadata").IsObject())
			require.Equal(t, "derived", gjson.GetBytes(wire, "metadata.user_id").String())
			if strings.HasPrefix(metadata, "{") {
				require.Equal(t, "keep", gjson.GetBytes(wire, "metadata.other").String())
			}
		})
	}
}
