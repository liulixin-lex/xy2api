//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOAuthMimicTemperatureByModelAndAPIVersion(t *testing.T) {
	for _, tc := range []struct {
		model           string
		wantTemperature bool
	}{
		{"claude-opus-4-5", true},
		{"claude-opus-4-6", true},
		{"claude-opus-4-5-20251101", true},
		{"claude-opus-4-6-20260205", true},
		{"claude-opus-4-5@20251101", true},
		{"claude-opus-4-6@20260205", true},
		{"anthropic/claude-opus-4.5", true},
		{"anthropic/claude-opus-4.6", true},
		{"anthropic/claude-opus-4-5-20251101", true},
		{"anthropic/claude-opus-4-6-20260205", true},
		{"bedrock/us.anthropic.claude-opus-4-5-20251101-v1:0", true},
		{"bedrock/us.anthropic.claude-opus-4-6-20260205-v1:0", true},
		{"claude-opus-4-7", false},
		{"claude-opus-4-8", false},
		{"claude-opus-5", false},
		{"claude-opus-5-5", false},
		{"claude-opus-4-20250514", true},
		{"claude-opus-4-7-20260416", false},
		{"us.anthropic.claude-opus-4-8", false},
		{"anthropic/claude-opus-5.5", false},
		{"claude-sonnet-5-5", true},
		{"claude-haiku-5-5", true},
		{"claude-fable-5", false},
		{"claude-fable-5-1", false},
		{"claude-mythos-5", false},
		{"claude-mythos-5-1", false},
		{"anthropic/claude-fable-5.1", false},
		{"bedrock/us.anthropic.claude-mythos-5-1-v1:0", false},
	} {
		for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
			for _, apiVersion := range []string{"", "2026-10-21", "2027-01-01"} {
				for _, path := range []string{"messages", "converted"} {
					t.Run(fmt.Sprintf("%s/%s/%s/%s", tc.model, accountType, apiVersion, path), func(t *testing.T) {
						c, _ := gin.CreateTestContext(httptest.NewRecorder())
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
						if apiVersion != "" {
							c.Request.Header.Set("anthropic-version", apiVersion)
						}
						svc := &GatewayService{cfg: &config.Config{}}
						account := &Account{Platform: PlatformAnthropic, Type: accountType}
						body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, tc.model))
						if path == "messages" {
							body, _ = normalizeClaudeOAuthRequestBody(body, tc.model, claudeOAuthNormalizeOptions{})
						} else {
							// Responses and Chat share this body mimicry entry point.
							body = svc.applyClaudeCodeOAuthMimicryToBody(context.Background(), c, account, body, nil, tc.model)
						}
						req, wireBody, err := svc.buildUpstreamRequest(context.Background(), c, account, body,
							"test-token", "oauth", tc.model, false, true)
						require.NoError(t, err)
						// Mimicry forces the legacy API version, regardless of the client's header.
						require.Equal(t, "2023-06-01", getHeaderRaw(req.Header, "anthropic-version"))
						temperature := gjson.GetBytes(wireBody, "temperature")
						require.Equal(t, tc.wantTemperature, temperature.Exists())
						if tc.wantTemperature {
							require.Equal(t, float64(1), temperature.Float())
						}
					})
				}
			}
		}
	}
}

func TestOAuthMimicPreservesClientSamplingParameters(t *testing.T) {
	for _, model := range []string{"claude-opus-4-6", "claude-opus-4-7", "claude-opus-5-5", "claude-fable-5-1", "claude-mythos-5-1"} {
		for _, field := range []string{`"temperature":0.7`, `"top_p":0.9`, `"top_k":20`} {
			t.Run(model+"/"+field, func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":%q,%s,"messages":[]}`, model, field))
				out, _ := normalizeClaudeOAuthRequestBody(body, model, claudeOAuthNormalizeOptions{})
				for _, key := range []string{"temperature", "top_p", "top_k"} {
					if value := gjson.GetBytes(body, key); value.Exists() {
						require.Equal(t, value.Raw, gjson.GetBytes(out, key).Raw)
					}
				}
				if gjson.GetBytes(body, "top_p").Exists() {
					require.False(t, gjson.GetBytes(out, "temperature").Exists())
				}
			})
		}
	}
}
