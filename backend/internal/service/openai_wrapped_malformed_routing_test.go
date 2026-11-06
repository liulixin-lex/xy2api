//go:build unit

package service

import (
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
				require.Len(t, logs.FilterMessage("resilience.wrapped_error_malformed").All(), 1)
			})
		}
	}
}
