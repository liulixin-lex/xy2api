//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A Responses stream that the client has seen only heartbeats on, and that
// the gateway then ends itself (every candidate failed, the 240 s budget
// ran out), must open with response.created: openai-python raised
// "Expected to have received `response.created` before `response.failed`"
// instead of showing the gateway's message (RC client
// C-openai-py-slow-first-cdn).

type sseEvent struct {
	name string
	data string
}

func parseSSEEvents(body string) []sseEvent {
	var events []sseEvent
	for _, block := range strings.Split(body, "\n\n") {
		var ev sseEvent
		for _, line := range strings.Split(block, "\n") {
			if name, ok := strings.CutPrefix(line, "event: "); ok {
				ev.name = name
			} else if data, ok := strings.CutPrefix(line, "data: "); ok {
				ev.data = data
			}
		}
		if ev.name != "" || ev.data != "" {
			events = append(events, ev)
		}
	}
	return events
}

func trackedResponsesContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	service.InstallDeliveryTracking(c, time.Now())
	return c, rec
}

func requireCreatedThenFailed(t *testing.T, body string) gjson.Result {
	t.Helper()
	events := parseSSEEvents(body)
	require.Len(t, events, 2, body)
	created, failed := events[0], events[1]
	require.Equal(t, "response.created", created.name, body)
	require.Equal(t, "response.created", gjson.Get(created.data, "type").String())
	require.Equal(t, "in_progress", gjson.Get(created.data, "response.status").String())
	require.Equal(t, int64(0), gjson.Get(created.data, "sequence_number").Int())
	require.Equal(t, "response.failed", failed.name, body)
	require.Equal(t, int64(1), gjson.Get(failed.data, "sequence_number").Int())
	id := gjson.Get(created.data, "response.id").String()
	require.True(t, strings.HasPrefix(id, "resp_"), id)
	require.Equal(t, id, gjson.Get(failed.data, "response.id").String(), "one response")
	require.Equal(t, gjson.Get(created.data, "response.created_at").Int(), gjson.Get(failed.data, "response.created_at").Int())
	return gjson.Get(failed.data, "response")
}

func TestHeartbeatCommittedResponsesStreamOpensBeforeItFails(t *testing.T) {
	withHandlerResilience(t, nil)
	c, rec := trackedResponsesContext(t)
	c.Set(ginKeyPreOutputKeepaliveStop, service.StartPreOutputKeepalive(c, service.PreOutputProtocolResponses, 0, time.Hour))
	defer stopPreOutputKeepalive(c)
	require.Eventually(t, func() bool { return service.PreOutputKeepaliveCommitted(c) }, time.Second, 5*time.Millisecond)

	(&OpenAIGatewayHandler{}).handleStreamingAwareErrorWithCode(c, http.StatusServiceUnavailable, "upstream_error", "",
		"Upstream service temporarily unavailable", false, true)
	stopPreOutputKeepalive(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.HasPrefix(rec.Body.String(), ":\n\n"), "heartbeats came first")
	failed := requireCreatedThenFailed(t, rec.Body.String())
	require.Equal(t, "failed", failed.Get("status").String())
	require.Contains(t, failed.Get("error.message").String(), "Upstream service temporarily unavailable")
	require.NotEmpty(t, failed.Get("error.code").String())
}

func TestQueuePingsThenResponsesFailure(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		withHandlerResilience(t, func(s *resilience.Settings) { s.Enabled = enabled })
		c, rec := trackedResponsesContext(t)
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.WriteHeader(http.StatusOK)
		n, err := c.Writer.WriteString(":\n\n")
		require.NoError(t, err)
		recordGatewayStreamHeartbeat(c, n)

		require.True(t, writeResponsesFailedSSE(c, "api_error", "", "no account"))
		if enabled {
			requireCreatedThenFailed(t, rec.Body.String())
			continue
		}
		// Switched off: the 0.3.0 frame.
		events := parseSSEEvents(rec.Body.String())
		require.Len(t, events, 1, rec.Body.String())
		require.Equal(t, "response.failed", events[0].name)
		require.Equal(t, int64(0), gjson.Get(events[0].data, "sequence_number").Int())
	}
}

func TestResponsesFailureAfterUpstreamEventsAddsNoCreated(t *testing.T) {
	withHandlerResilience(t, nil)
	c, rec := trackedResponsesContext(t)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.WriteHeader(http.StatusOK)
	_, err := c.Writer.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_up\",\"status\":\"in_progress\"}}\n\n")
	require.NoError(t, err)

	require.True(t, writeResponsesFailedSSE(c, "api_error", "", "stream broke"))
	events := parseSSEEvents(rec.Body.String())
	require.Len(t, events, 2, rec.Body.String())
	require.Equal(t, "resp_up", gjson.Get(events[0].data, "response.id").String(), "the upstream's own response.created")
	require.Equal(t, "response.failed", events[1].name)
}
