//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestOpenAIWrappedMalformedSchemaDoesNotLogValues(t *testing.T) {
	for _, tc := range []struct {
		event, payload, dataType, keys, statusType, codeType string
	}{
		{"error", `{"type":"proxy.upstream_error","status":"private-status","code":["private-code"],"message":"private-message","encrypted_content":"private-encrypted"}`, "proxy.upstream_error", "code,encrypted_content,message,status,type", "string", "array"},
		{"response.failed", `{"type":"response.failed","response":{"error":{"type":"upstream_error","status_code":502,"code":{"value":"private-code"},"message":"private-message"}}}`, "response.failed", "response,type", "number", "object"},
		{"error", `{"type":"proxy.upstream_error","status":null,"code":false}`, "proxy.upstream_error", "code,status,type", "null", "boolean"},
		{"error", `{"type":"proxy.upstream_error"}`, "proxy.upstream_error", "type", "missing", "missing"},
		{"error", `{"type":"proxy.upstream_error","message":"private-message"`, "", "", "invalid", "invalid"},
	} {
		core, logs := observer.New(zap.InfoLevel)
		ctx := logger.IntoContext(context.Background(), zap.New(core))
		logOpenAIWrappedErrorMalformed(ctx, &Account{ID: 1}, "native_sse", tc.event, []byte(tc.payload))
		entries := logs.FilterMessage("resilience.wrapped_error_malformed").All()
		require.Len(t, entries, 1)
		fields := entries[0].ContextMap()
		require.Equal(t, tc.event, fields["event_type"])
		require.Equal(t, tc.dataType, fields["data_type"])
		require.Equal(t, tc.keys, fields["data_keys"])
		require.Equal(t, tc.statusType, fields["status_json_type"])
		require.Equal(t, tc.codeType, fields["code_json_type"])
		encoded, err := json.Marshal(fields)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "private-")
		require.NotContains(t, string(encoded), "502")
	}
}

func TestOpenAIWrappedMalformedFrameIsProtocolFailure(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct{ name, event, payload string }{
		{"E2 conflicting auth status", "response.failed", `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"400","status_code":401,"message":"rejected"}}}`},
		{"E1 string auth status", "error", `{"type":"proxy.upstream_error","status":"401","message":"rejected"}`},
		{"E1 rejected state message", "error", `{"type":"proxy.upstream_error","status":"400","message":"conversation_invalid"}`},
	} {
		for _, path := range []string{"native stream", "passthrough stream", "native aggregate", "passthrough aggregate"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				c, recorder := newNonStreamingFailoverContext(t)
				core, logs := observer.New(zap.InfoLevel)
				c.Request = c.Request.WithContext(logger.IntoContext(c.Request.Context(), zap.New(core)))
				repo := &openAIAuthPolicyAccountRepo{}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), rateLimitService: &RateLimitService{accountRepo: repo}}
				account := &Account{ID: 9031, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
				body := "event: " + tc.event + "\ndata: " + tc.payload + "\n\n"
				resp := newNonStreamingSSEResponse()
				resp.Body = io.NopCloser(strings.NewReader(body))
				var err error
				switch path {
				case "native stream":
					_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				case "passthrough stream":
					_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				case "native aggregate":
					_, err = svc.handleSSEToJSON(resp, c, account, []byte(body), "model", "model")
				case "passthrough aggregate":
					_, err = svc.handlePassthroughSSEToJSON(resp, c, account, []byte(body), "model", "model")
				}
				var failure *UpstreamFailoverError
				require.ErrorAs(t, err, &failure, "must bypass repair signals and passthrough rules")
				require.Equal(t, http.StatusBadGateway, failure.StatusCode)
				require.Zero(t, failure.WrappedStatus)
				require.False(t, failure.IsCredentialFailure())
				require.Contains(t, string(failure.ResponseBody), openAIMalformedWrappedErrorMessage)
				require.NotContains(t, string(failure.ResponseBody), "rejected")
				require.NotContains(t, string(failure.ResponseBody), "conversation_invalid")
				require.Zero(t, repo.setErrorCalls, "invalid wrapper cannot disable credentials")
				require.Zero(t, repo.tempCalls, "invalid wrapper cannot trigger authentication cooldown")
				require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
				require.Empty(t, recorder.Body.String())
				entries := logs.FilterMessage("resilience.wrapped_error_malformed").All()
				require.Len(t, entries, 1)
				require.Equal(t, tc.event, entries[0].ContextMap()["event_type"])
				require.NotEmpty(t, entries[0].ContextMap()["data_keys"])
			})
		}
	}
}
