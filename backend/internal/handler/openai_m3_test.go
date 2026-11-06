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
	"github.com/tidwall/gjson"
)

type m3MessageEchoUpstream struct {
	service.HTTPUpstream
	calls     int
	aggregate bool
	bodies    [][]byte
}

func (u *m3MessageEchoUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.bodies = append(u.bodies, body)
	if u.aggregate {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader("event: response.failed\ndata: " +
				`{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"400","status_code":400,"message":"relay rejected cipher-must-not-leak"}},"upstream_body":{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_safe; ciphertext=cipher-must-not-leak"}}}` + "\n\n")),
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item amsg_safe; ciphertext=cipher-must-not-leak"}}`)),
	}, nil
}

func TestOpenAIM3AggregateTerminalNeverEchoesCiphertext(t *testing.T) {
	withHandlerResilience(t, nil)
	for name, passthrough := range map[string]bool{"native": false, "passthrough": true} {
		t.Run(name, func(t *testing.T) {
			ctx := budgetCtx(t, resilience.KindText, 7)
			c, recorder := newOpenAIResponsesFailoverTestContext(t, ctx)
			service.InstallDeliveryTracking(c, time.Now())
			upstream := &m3MessageEchoUpstream{aggregate: true}
			cfg := &config.Config{RunMode: config.RunModeSimple, Gateway: config.GatewayConfig{LogUpstreamErrorBody: true}}
			gateway := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
			account := &service.Account{
				ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "test-key"}, Extra: map[string]any{"openai_passthrough": passthrough},
			}
			body := []byte(`{"model":"gpt-5.1","stream":false,"input":[{"type":"agent_message","id":"amsg_safe","content":[{"type":"encrypted_content","encrypted_content":"cipher-must-not-leak"}]}]}`)
			_, err := gateway.Forward(ctx, c, account, body)
			var failure *service.UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			require.Equal(t, service.OpenAIStateRepairReason, failure.Reason)
			require.Equal(t, 2, upstream.calls)
			require.Len(t, upstream.bodies, 2)
			require.Equal(t, "amsg_safe", gjson.GetBytes(upstream.bodies[1], "input.0.id").String())
			require.JSONEq(t, `{"type":"input_text","text":"[encrypted content omitted]"}`, gjson.GetBytes(upstream.bodies[1], "input.0.content.0").Raw)
			require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.content.0.encrypted_content").Exists())
			require.NotContains(t, string(upstream.bodies[1]), "cipher-must-not-leak")
			require.Equal(t, "The upstream rejected the derived encrypted-state request", failure.ClientMessage)
			require.Empty(t, failure.ResponseBody)
			(&OpenAIGatewayHandler{gatewayService: gateway}).handleFailoverExhausted(c, failure, false)
			require.Equal(t, http.StatusBadGateway, recorder.Code)
			require.Contains(t, recorder.Body.String(), failure.ClientMessage)
			require.NotContains(t, recorder.Body.String(), "cipher-must-not-leak")
			require.Equal(t, failure.ClientMessage, c.GetString(service.OpsUpstreamErrorMessageKey))
			require.NotContains(t, c.GetString(service.OpsUpstreamErrorDetailKey), "cipher-must-not-leak")
			if raw, exists := c.Get(service.OpsUpstreamErrorsKey); exists {
				events, ok := raw.([]*service.OpsUpstreamErrorEvent)
				require.True(t, ok)
				for _, event := range events {
					require.NotContains(t, event.Message, "cipher-must-not-leak")
					require.NotContains(t, event.Detail, "cipher-must-not-leak")
					require.NotContains(t, event.UpstreamResponseBody, "cipher-must-not-leak")
				}
			}
		})
	}
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
