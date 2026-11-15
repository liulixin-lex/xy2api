//go:build unit

package service

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The mismatched tool history reaches the upstream unchanged. Only its actual
// HTTP 400 rejection is projected; no local tool-history validator is added.
func TestToolHistory400Preserved(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"native", "passthrough", "chat-responses"} {
		for _, param := range []string{"input[1].call_id", ""} {
			t.Run(route+"/param="+param, func(t *testing.T) {
				const message = "No tool call found for function call output with call_id call_wrong at input[1]"
				fields := map[string]string{"type": "invalid_request_error", "code": "invalid_request_error", "message": message}
				if param != "" {
					fields["param"] = param
				}
				errorJSON, marshalErr := json.Marshal(map[string]any{"error": fields})
				require.NoError(t, marshalErr)
				errorBody := string(errorJSON)
				upstream := &httpUpstreamRecorder{resp: newOpenAIUpstreamErrorResponse(http.StatusBadRequest, errorBody)}
				repo := &openAIModelAvailabilityRepo{}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: repo,
					rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
				account := openAIFailoverCachedBodyTestAccount(941, "fixture", nil)
				account.Type = AccountTypeAPIKey
				account.Credentials["api_key"] = "fixture-key"
				account.Credentials["base_url"] = "https://fixture.invalid/v1"
				account.Extra = map[string]any{"openai_passthrough": route == "passthrough", "openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff}
				req := newWebSearchReplayRequest(t)
				var err error
				if route == "chat-responses" {
					_, err = svc.ForwardAsChatCompletions(req.ctx, req.c, account,
						[]byte(`{"model":"gpt-5.6-sol","stream":false,"messages":[{"role":"assistant","tool_calls":[{"id":"call_right","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_wrong","content":"ok"}]}`), "", "")
				} else {
					_, err = svc.Forward(req.ctx, req.c, account,
						[]byte(`{"model":"gpt-5.6-sol","stream":false,"input":[{"type":"function_call","call_id":"call_right","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_wrong","output":"ok"}]}`))
				}
				var failure *UpstreamFailoverError
				require.Error(t, err)
				require.NotErrorAs(t, err, &failure)
				require.Len(t, upstream.bodies, 1)
				require.Contains(t, string(upstream.bodies[0]), "call_wrong")
				require.Equal(t, http.StatusBadRequest, req.rec.Code)
				require.Equal(t, message, gjson.GetBytes(req.rec.Body.Bytes(), "error.message").String())
				require.Equal(t, "invalid_request_error", gjson.GetBytes(req.rec.Body.Bytes(), "error.type").String())
				require.Equal(t, "invalid_request_error", gjson.GetBytes(req.rec.Body.Bytes(), "error.code").String())
				require.Equal(t, param, gjson.GetBytes(req.rec.Body.Bytes(), "error.param").String())
				require.Zero(t, repo.setErrorCalls)
				require.Zero(t, repo.tempCalls)
				require.Empty(t, repo.modelRateLimitCalls)
				require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
				require.Empty(t, req.session.failures)
			})
		}
	}
}
