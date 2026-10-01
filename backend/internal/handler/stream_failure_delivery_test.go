package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type failureFlushWriter struct{ gin.ResponseWriter }

func (w *failureFlushWriter) SetWriteDeadline(time.Time) error { return nil }
func (w *failureFlushWriter) FlushError() error                { return errors.New("fixture flush failure") }

func TestOpenAILocalFailureReportsActualFlush(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request = c.Request.WithContext(service.WithNativeStreamPolicy(c.Request.Context(), service.NativeStreamPolicy{Delivery: true}))
	c.Writer = &failureFlushWriter{c.Writer}
	// Write succeeds, but the socket flush fails. Do not report a delivered
	// fallback or append a second protocol error to the partially sent frame.
	require.False(t, (&OpenAIGatewayHandler{}).handleStreamingAwareError(c, 502, "upstream_error", "fixture", true))
	require.False(t, c.GetBool(localStreamFailureWrittenKey))
	require.Len(t, c.Errors, 1)
	require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed\n"))
}

func TestControlledSchedulingStopAfterHeartbeatEndsOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/responses", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, path, nil)
			c.Header("Content-Type", "text/event-stream")
			_, err := c.Writer.WriteString(": keepalive\n\n")
			require.NoError(t, err)
			c.Writer.Flush()
			require.True(t, handleControlledSchedulingStop(c, scheduling.ErrCapacity))
			require.Contains(t, rec.Body.String(), "scheduling_capacity_exhausted")
			body := rec.Body.String()
			require.True(t, handleControlledSchedulingStop(c, scheduling.ErrDeadline))
			require.Equal(t, body, rec.Body.String())
			require.True(t, c.IsAborted())
		})
	}
}
