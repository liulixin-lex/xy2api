//go:build unit

package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type m3MessageEchoUpstream struct {
	service.HTTPUpstream
	calls int
}

func (u *m3MessageEchoUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_safe; ciphertext=cipher-must-not-leak"}}`)),
	}, nil
}

func TestOpenAIM3TerminalNeverEchoesCiphertext(t *testing.T) {
	withHandlerResilience(t, nil)
	for _, committed := range []bool{false, true} {
		ctx := budgetCtx(t, resilience.KindText, 7)
		c, recorder := newOpenAIResponsesFailoverTestContext(t, ctx)
		service.InstallDeliveryTracking(c, time.Now())
		if committed {
			c.Header("Content-Type", "text/event-stream")
			n, err := c.Writer.WriteString("event: ping\ndata: {\"type\":\"ping\"}\n\n")
			require.NoError(t, err)
			require.True(t, service.NoteDeliveryHeartbeat(c, n))
			c.Writer.Flush()
		}
		upstream := &m3MessageEchoUpstream{}
		gateway := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple}, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
		account := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}
		body := []byte(`{"model":"gpt-5.1","stream":true,"input":[{"type":"agent_message","id":"amsg_safe","content":[{"type":"encrypted_content","encrypted_content":"cipher-must-not-leak"}]}]}`)
		_, err := gateway.Forward(ctx, c, account, body)
		var failure *service.UpstreamFailoverError
		require.ErrorAs(t, err, &failure)
		require.Equal(t, service.OpenAIStateRepairReason, failure.Reason)
		require.Equal(t, 2, upstream.calls)
		require.Equal(t, "Encrypted content could not be verified for item amsg_safe", failure.ClientMessage)
		require.Empty(t, failure.ResponseBody)
		(&OpenAIGatewayHandler{gatewayService: gateway}).handleFailoverExhausted(c, failure, committed)
		require.NotContains(t, recorder.Body.String(), "cipher-must-not-leak")
		require.Contains(t, recorder.Body.String(), "amsg_safe")
		recorded, ok := c.Get(service.OpsUpstreamErrorMessageKey)
		require.True(t, ok)
		require.Equal(t, failure.ClientMessage, recorded)
		require.NotContains(t, recorded, "cipher-must-not-leak")
		if committed {
			require.Contains(t, recorder.Body.String(), "event: response.failed")
			streamError, exists := service.GetOpsStreamError(c)
			require.True(t, exists)
			require.NotContains(t, streamError.Message, "cipher-must-not-leak")
		} else {
			require.Equal(t, http.StatusBadGateway, recorder.Code)
		}
	}
}

func TestOpenAIM3TerminalUsesRetryableResponse(t *testing.T) {
	withHandlerResilience(t, nil)
	gin.SetMode(gin.TestMode)
	for _, committed := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		ctx := resilience.WithRequestState(httptest.NewRequest(http.MethodPost, "/v1/responses", nil).Context(), resilience.NewRequestState())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
		if committed {
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			c.Writer.WriteHeader(http.StatusOK)
			_, err := c.Writer.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_m3\",\"status\":\"in_progress\"}}\n\n")
			require.NoError(t, err)
		}
		(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode: http.StatusBadRequest, Reason: service.OpenAIStateRepairReason,
			ClientMessage: "encrypted state rejected", M3Cause: "no_growth",
		}, false) // The terminal writer must also detect already committed headers itself.
		if !committed {
			require.Equal(t, http.StatusBadGateway, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"code":"server_error"`)
			continue
		}
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Contains(t, recorder.Body.String(), "event: response.failed")
		require.Contains(t, recorder.Body.String(), `"code":"server_error"`)
	}
}
