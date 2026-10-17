//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newKeepaliveTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Header("X-Client-Request-ID", "req-1")
	return c, w
}

func waitCommitted(t *testing.T, c *gin.Context) {
	t.Helper()
	require.Eventually(t, func() bool { return PreOutputKeepaliveCommitted(c) }, 2*time.Second, 5*time.Millisecond)
}

func TestPreOutputKeepaliveAnthropicPingsAndFailoverSize(t *testing.T) {
	c, w := newKeepaliveTestContext(t)
	before := OpenAICompactKeepaliveAdjustedWrittenSize(c)
	stop := StartPreOutputKeepalive(c, PreOutputProtocolAnthropic, 20*time.Millisecond, 10*time.Millisecond)
	defer stop()
	waitCommitted(t, c)
	time.Sleep(40 * time.Millisecond)

	// Heartbeat bytes never count as output: failover stays possible.
	require.Equal(t, before, OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	require.Equal(t, "no", w.Header().Get("X-Accel-Buffering"))
	require.Equal(t, "req-1", w.Header().Get("X-Client-Request-ID"))
	require.True(t, strings.HasPrefix(w.Body.String(), "event: ping\ndata: {\"type\":\"ping\"}\n\n"), w.Body.String())

	// A second arm is a no-op.
	StartPreOutputKeepalive(c, PreOutputProtocolAnthropic, 0, time.Millisecond)()
}

func TestPreOutputKeepaliveUncommittedKeepsStatusCode(t *testing.T) {
	// Spec: fast failures still return their original status code.
	c, w := newKeepaliveTestContext(t)
	stop := StartPreOutputKeepalive(c, PreOutputProtocolResponses, time.Hour, time.Hour)
	defer stop()
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "message": "busy"}})
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, "busy", gjson.GetBytes(w.Body.Bytes(), "error.message").String())
}

func TestPreOutputKeepaliveRewritesCommittedErrors(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		status   int
		body     gin.H
		check    func(t *testing.T, body string)
	}{
		{PreOutputProtocolAnthropic, 529, gin.H{"type": "error", "error": gin.H{"type": "overloaded_error", "message": "Overloaded"}}, func(t *testing.T, body string) {
			require.Contains(t, body, "event: error\ndata: {")
			frame := body[strings.Index(body, "event: error"):]
			payload := strings.TrimPrefix(strings.Split(frame, "\n")[1], "data: ")
			require.Equal(t, "overloaded_error", gjson.Get(payload, "error.type").String())
			require.Equal(t, "Overloaded", gjson.Get(payload, "error.message").String())
		}},
		{PreOutputProtocolResponses, 429, gin.H{"error": gin.H{"type": "rate_limit_error", "message": "slow down"}}, func(t *testing.T, body string) {
			require.Contains(t, body, "event: response.failed\ndata: {")
			frame := body[strings.Index(body, "event: response.failed"):]
			payload := strings.TrimPrefix(strings.Split(frame, "\n")[1], "data: ")
			require.Equal(t, "rate_limit_exceeded", gjson.Get(payload, "response.error.code").String())
			require.Equal(t, "slow down", gjson.Get(payload, "response.error.message").String())
			require.Equal(t, "failed", gjson.Get(payload, "response.status").String())
		}},
		{PreOutputProtocolChat, 502, gin.H{"error": gin.H{"type": "upstream_error", "code": "bad_gateway", "message": "gone"}}, func(t *testing.T, body string) {
			require.True(t, strings.HasSuffix(body, "\n\ndata: [DONE]\n\n"), body)
			frame := body[strings.Index(body, "data: {"):]
			payload := strings.TrimPrefix(strings.Split(frame, "\n")[0], "data: ")
			require.Equal(t, int64(502), gjson.Get(payload, "error.status").Int())
			require.Equal(t, "bad_gateway", gjson.Get(payload, "error.code").String())
		}},
	} {
		c, w := newKeepaliveTestContext(t)
		stop := StartPreOutputKeepalive(c, tc.protocol, 0, time.Hour)
		waitCommitted(t, c)
		c.JSON(tc.status, tc.body)
		stop()
		require.Equal(t, http.StatusOK, w.Code, tc.protocol)
		tc.check(t, w.Body.String())
	}
}

// The client of a committed Responses stream has seen heartbeats only, so
// the terminal frame opens the response first: strict SDKs (openai-python,
// openai-node) reject a response.failed that is not preceded by
// response.created (RC client C-openai-py-slow-first-cdn).
func TestPreOutputKeepaliveResponsesTerminalOpensTheResponse(t *testing.T) {
	c, w := newKeepaliveTestContext(t)
	stop := StartPreOutputKeepalive(c, PreOutputProtocolResponses, 0, time.Hour)
	waitCommitted(t, c)
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "message": "busy"}})
	stop()

	var events []string
	for _, block := range strings.Split(w.Body.String(), "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				events = append(events, data)
			}
		}
	}
	require.Len(t, events, 2, w.Body.String())
	created, failed := events[0], events[1]
	require.Equal(t, "response.created", gjson.Get(created, "type").String())
	require.Equal(t, "in_progress", gjson.Get(created, "response.status").String())
	require.Equal(t, int64(0), gjson.Get(created, "sequence_number").Int())
	require.Equal(t, "response.failed", gjson.Get(failed, "type").String())
	require.Equal(t, int64(1), gjson.Get(failed, "sequence_number").Int())
	require.Equal(t, gjson.Get(created, "response.id").String(), gjson.Get(failed, "response.id").String())
	require.Equal(t, "busy", gjson.Get(failed, "response.error.message").String())
	require.Contains(t, w.Body.String(), "event: response.created\ndata: ")
}

func TestPreOutputKeepaliveStopsUncommittedForOAuth(t *testing.T) {
	c, w := newKeepaliveTestContext(t)
	stop := StartPreOutputKeepalive(c, PreOutputProtocolResponses, 30*time.Millisecond, 10*time.Millisecond)
	defer stop()
	StopPreOutputKeepaliveIfUncommitted(c)
	time.Sleep(80 * time.Millisecond)
	require.False(t, PreOutputKeepaliveCommitted(c))
	require.False(t, w.Body.Len() > 0)
}

func TestPreOutputKeepaliveSemanticOutputStopsBeats(t *testing.T) {
	c, w := newKeepaliveTestContext(t)
	stop := StartPreOutputKeepalive(c, PreOutputProtocolResponses, 0, 10*time.Millisecond)
	defer stop()
	waitCommitted(t, c)
	_, _ = c.Writer.Write([]byte("event: response.created\ndata: {}\n\n"))
	size := len(w.Body.String())
	time.Sleep(40 * time.Millisecond)
	stop()
	require.Equal(t, size, len(w.Body.String()), "no heartbeat after the first semantic byte")
	require.Greater(t, OpenAICompactKeepaliveAdjustedWrittenSize(c), 0)
}
