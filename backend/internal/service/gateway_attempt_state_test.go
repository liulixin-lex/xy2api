//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func ordinaryAttemptBody(t *testing.T, entry string) []byte {
	t.Helper()
	body := map[string]any{"model": "claude-sonnet-4-5", "max_tokens": 64}
	messages := []any{map[string]any{"role": "user", "content": "Return a literal string"}}
	if entry == "responses" {
		body["input"] = messages
	} else {
		body["messages"] = messages
	}
	var tools []any
	for _, name := range []string{"read_file", "edit_file", "list_files", "run_cmd", "search_text", "fetch_doc"} {
		schema := map[string]any{"type": "object"}
		switch entry {
		case "chat":
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": name, "parameters": schema}})
		case "responses":
			tools = append(tools, map[string]any{"type": "function", "name": name, "parameters": schema})
		default:
			tools = append(tools, map[string]any{"name": name, "input_schema": schema})
		}
	}
	body["tools"] = tools
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return raw
}

func TestOrdinaryFailoverClearsOAuthAttemptState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, entry := range []string{"messages", "chat", "responses", "count_tokens"} {
		t.Run(entry, func(t *testing.T) {
			svc, _, ordinary := cacheFixture(t)
			ordinary.Extra = map[string]any{AnthropicCompatProfileExtraKey: AnthropicCompatProfileOff}
			oauth := *ordinary
			oauth.ID++
			oauth.Type = AccountTypeOAuth
			oauth.Credentials = map[string]any{"access_token": "synthetic-oauth-token"}
			const beta = "context-management-2025-06-27"
			svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
				SettingKeyBetaPolicySettings: `{"rules":[{"beta_token":"` + beta + `","action":"filter","scope":"oauth"}]}`,
			}}, svc.cfg)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/"+entry, nil)
			c.Request.Header.Set("anthropic-beta", beta)
			first := ttlWireUpstream(false)
			first.resp.StatusCode = 503
			first.resp.Body = io.NopCloser(strings.NewReader(`{"error":{"type":"overloaded_error","message":"busy"}}`))
			svc.httpUpstream = first
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(ordinaryAttemptBody(t, "messages")), PlatformAnthropic)
			require.NoError(t, err)
			switch entry {
			case "chat":
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, &oauth, ordinaryAttemptBody(t, entry), nil)
			case "responses":
				_, err = svc.ForwardAsResponses(context.Background(), c, &oauth, ordinaryAttemptBody(t, entry), nil)
			default:
				// count_tokens shares this attempt boundary and must also clear any
				// response rewrite and cached policy from a preceding native attempt.
				_, err = svc.Forward(context.Background(), c, &oauth, parsed)
			}
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.False(t, c.Writer.Written())
			rw := toolNameRewriteFromContext(c)
			require.NotNil(t, rw)
			alias := rw.Forward["read_file"]
			require.NotEmpty(t, alias)
			require.Contains(t, svc.getBetaPolicyFilterSet(context.Background(), c, &oauth, parsed.Model), beta)

			stream := entry == "chat" || entry == "responses"
			second := ttlWireUpstream(stream)
			response := ttlWireResponse
			if stream {
				response = ttlWireSSE()
			} else if entry == "count_tokens" {
				response = `{"input_tokens":42}`
			}
			second.resp.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(response, "OK", alias)))
			svc.httpUpstream = second
			body := ordinaryAttemptBody(t, entry)
			parsed, err = ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)
			switch entry {
			case "messages":
				_, err = svc.Forward(context.Background(), c, ordinary, parsed)
			case "chat":
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, ordinary, body, nil)
			case "responses":
				_, err = svc.ForwardAsResponses(context.Background(), c, ordinary, body, nil)
			case "count_tokens":
				err = svc.ForwardCountTokens(context.Background(), c, ordinary, parsed)
			}
			require.NoError(t, err)
			require.Equal(t, "read_file", gjson.GetBytes(second.lastBody, "tools.0.name").String())
			require.Nil(t, toolNameRewriteFromContext(c))
			require.Contains(t, getHeaderRaw(second.lastReq.Header, "anthropic-beta"), beta, "OAuth-only policy must not survive failover to an API Key")
			if entry != "count_tokens" {
				require.Contains(t, rec.Body.String(), alias, "ordinary response must retain the literal upstream tool alias")
			} else {
				require.JSONEq(t, `{"input_tokens":42}`, rec.Body.String())
			}
		})
	}
}
