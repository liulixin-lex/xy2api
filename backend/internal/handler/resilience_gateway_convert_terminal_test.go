//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// R2-P1① for the Gateway conversions of an Anthropic upstream: when the
// staged attempt only wrote heartbeats and every account then failed, the
// exhausted exit ends the committed 200 with the inbound protocol's terminal
// frame, never a bare JSON body.
func TestGatewayConvertExhaustedAfterHeartbeatsWritesProtocolTerminal(t *testing.T) {
	withHandlerResilience(t, nil)
	lastErr := &service.UpstreamFailoverError{StatusCode: 529, ResponseBody: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)}
	heartbeatOnly := func(t *testing.T, path string) (*gin.Context, *httptest.ResponseRecorder) {
		t.Helper()
		c, rec := trackedResponsesContext(t)
		c.Request.URL.Path = path
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.WriteHeader(http.StatusOK)
		n, err := c.Writer.WriteString(":\n\n")
		require.NoError(t, err)
		require.True(t, service.NoteDeliveryHeartbeat(c, n))
		return c, rec
	}

	t.Run("responses", func(t *testing.T) {
		c, rec := heartbeatOnly(t, "/v1/responses")
		(&GatewayHandler{}).handleResponsesFailoverExhausted(c, lastErr, false)
		require.True(t, strings.HasPrefix(rec.Body.String(), ":\n\n"))
		failed := requireCreatedThenFailed(t, rec.Body.String())
		require.Equal(t, "failed", failed.Get("status").String())
	})
	t.Run("chat", func(t *testing.T) {
		c, rec := heartbeatOnly(t, "/v1/chat/completions")
		(&GatewayHandler{}).handleCCFailoverExhausted(c, lastErr, false)
		body := rec.Body.String()
		require.True(t, strings.HasPrefix(body, ":\n\n"))
		require.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"), body)
		frames := strings.Split(strings.TrimSuffix(strings.TrimPrefix(body, ":\n\n"), "\n\n"), "\n\n")
		require.Len(t, frames, 2, body)
		require.True(t, strings.HasPrefix(frames[0], "data: {"), body)
		require.True(t, gjson.Get(strings.TrimPrefix(frames[0], "data: "), "error.message").Exists(), body)
		require.NotContains(t, body, "\n{", "no bare JSON after the heartbeat")
	})
}
