//go:build unit

package handler

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type modelUnavailableUpstream struct {
	service.HTTPUpstream
	status   int
	accounts []int64
}

func TestOpenAIModelUnavailableExhaustionAfterHeartbeat(t *testing.T) {
	withHandlerResilience(t, nil)
	c, recorder := turnCapContext(t, EndpointResponses)
	service.InstallDeliveryTracking(c, time.Now())
	c.Header("Content-Type", "text/event-stream")
	n, err := c.Writer.WriteString(": keepalive\n\n")
	require.NoError(t, err)
	require.True(t, service.NoteDeliveryHeartbeat(c, n))
	c.Writer.Flush()
	resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Seconds: 7})
	failure := &service.UpstreamFailoverError{
		StatusCode: 404, Reason: "openai_model_unavailable",
		ResponseBody: []byte(`{"error":{"type":"invalid_request_error","message":"model is unavailable"}}`),
	}
	h := &OpenAIGatewayHandler{}
	h.handleFailoverExhausted(c, failure, true)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: response.failed"))
	require.NotContains(t, recorder.Body.String(), "retry-after")
	require.Empty(t, recorder.Header().Get("Retry-After"))
	require.False(t, c.GetBool(ginKeyResilienceTurnCounted))
	before := recorder.Body.String()
	h.handleFailoverExhausted(c, failure, true)
	require.Equal(t, before, recorder.Body.String())
}

func (u *modelUnavailableUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.accounts = append(u.accounts, accountID)
	return &http.Response{
		StatusCode: u.status,
		Header:     http.Header{"Content-Type": {"application/json"}, "Retry-After": {"9"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","code":"model_unavailable","param":"model","message":"model is unavailable; workspace_id=private-workspace"}}`)),
	}, nil
}

func TestOpenAIModelUnavailableExhaustionPreservesLastError(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name   string
		status int
		write  func(*gin.Context, *service.UpstreamFailoverError, bool)
	}{
		{"native", 400, (&OpenAIGatewayHandler{}).handleFailoverExhausted},
		{"responses", 404, (&GatewayHandler{}).handleResponsesFailoverExhausted},
		{"chat", 400, (&GatewayHandler{}).handleCCFailoverExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder := turnCapContext(t, EndpointResponses)
			upstream := &modelUnavailableUpstream{status: tc.status}
			repo := &openAIImagesFailoverAccountRepo{}
			gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple}, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
			var last *service.UpstreamFailoverError
			for _, id := range []int64{1, 2} {
				account := &service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}
				_, err := gateway.Forward(c.Request.Context(), c, account, []byte(`{"model":"requested-model","stream":false,"input":"hello"}`))
				require.ErrorAs(t, err, &last)
				require.True(t, last.IsOpenAIModelUnavailable())
				require.Empty(t, recorder.Body.String())
			}
			require.Equal(t, []int64{1, 2}, upstream.accounts)
			c.Header("Retry-After", "7")
			resilience.SetRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Seconds: 7})
			tc.write(c, last, false)
			require.Equal(t, tc.status, recorder.Code)
			require.JSONEq(t, `{"error":{"type":"invalid_request_error","code":"model_unavailable","param":"model","message":"model is unavailable; workspace_id=[redacted]"}}`, recorder.Body.String())
			require.Empty(t, recorder.Header().Get("Retry-After"))
			require.False(t, c.GetBool(ginKeyResilienceTurnCounted))
			require.False(t, resilience.StateFrom(c.Request.Context()).TurnFailed())
			before := recorder.Body.String()
			tc.write(c, last, true)
			require.Equal(t, before, recorder.Body.String(), "a finalized response must not receive another terminal frame")
		})
	}
}

func TestOpenAIModelUnavailableAllCandidatesReturnOriginal400(t *testing.T) {
	withHandlerResilience(t, nil)
	upstream := &modelUnavailableUpstream{status: http.StatusBadRequest}
	h := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, recorder := newOpenAIResponsesFailoverTestContext(t, nil)
	h.Responses(c)
	require.Equal(t, []int64{1, 2}, upstream.accounts)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.JSONEq(t, `{"error":{"type":"invalid_request_error","code":"model_unavailable","param":"model","message":"model is unavailable; workspace_id=[redacted]"}}`, recorder.Body.String())
	require.Empty(t, recorder.Header().Get("Retry-After"))
	require.False(t, c.GetBool(ginKeyResilienceTurnCounted))
}
