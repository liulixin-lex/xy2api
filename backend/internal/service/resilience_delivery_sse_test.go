//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Review R03: on every stream that is not the staged Responses path, the
// opening events and pings written before the first content are no
// delivery, so a client that leaves then is not charged.

func newSSEDeliveryContext(t *testing.T, contentType string) (*gin.Context, *resilience.RequestState) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	st := resilience.NewRequestState()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(resilience.WithRequestState(t.Context(), st))
	InstallDeliveryTracking(c, time.Now())
	c.Header("Content-Type", contentType)
	c.Status(http.StatusOK)
	return c, st
}

func TestSSEOpeningEventsAreNoDelivery(t *testing.T) {
	withResilienceSettings(t, nil)
	for name, tc := range map[string]struct {
		opening []string
		content string
	}{
		"messages conversion and Anthropic": {
			opening: []string{
				"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":12}}}\n\n",
				"event: ping\ndata: {\"type\": \"ping\"}\n\n",
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n",
			},
			content: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n",
		},
		"chat conversion": {
			opening: []string{
				"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n",
				":\n\n",
				": ping\n\n",
			},
			content: "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n",
		},
		"grok and passthrough responses": {
			opening: []string{
				"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n",
				"event: response.in_progress\ndata: {\"type\":\"response.in_progress\"}\n\n",
				"event: response.metadata\ndata: {\"type\":\"response.metadata\",\"metadata\":{\"openai_chatgpt_moderation_metadata\":{}}}\n\n",
				"event: codex.response.metadata\ndata: {\"type\":\"codex.response.metadata\"}\n\n",
				": ping\n\n",
			},
			content: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}\n\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, st := newSSEDeliveryContext(t, "text/event-stream")
			for _, block := range tc.opening {
				_, _ = c.Writer.WriteString(block)
			}
			require.Zero(t, st.DeliveredBytes(), "opening events and pings only")
			_, _ = c.Writer.WriteString(tc.content)
			require.Positive(t, st.DeliveredBytes(), "content was delivered")
		})
	}
}

func TestSSEDeliveryCountsUnknownAndErrorBlocksAsContent(t *testing.T) {
	for name, block := range map[string]string{
		"anthropic error":  "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n\n",
		"responses failed": "event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n",
		"done":             "data: [DONE]\n\n",
		"gemini chunk":     "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hi\"}]}}]}\n\n",
		"tool use start":   "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"name\":\"bash\"}}\n\n",
		"chat finish":      "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"chat usage":       "data: {\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"total_tokens\":3}}\n\n",
		"chat tool call":   "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0}]}}]}\n\n",
		"unknown event":    "event: future.event\ndata: {\"type\":\"future.event\"}\n\n",
		"unknown codex":    "event: codex.unknown\ndata: {\"type\":\"codex.unknown\"}\n\n",
		"image partial":    "event: image_generation.partial_image\ndata: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"AAAA\"}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			c, st := newSSEDeliveryContext(t, "text/event-stream; charset=utf-8")
			_, _ = c.Writer.WriteString(block)
			require.Positive(t, st.DeliveredBytes())
		})
	}
}

func TestSSECodexRateLimitsPreludeIsNotDelivery(t *testing.T) {
	withResilienceSettings(t, nil)
	c, st := newSSEDeliveryContext(t, "text/event-stream")
	_, _ = c.Writer.WriteString("event: codex.rate_limits\ndata: {\"type\":\"codex.rate_limits\",\"rate_limits\":{}}\n\n")
	require.Zero(t, st.DeliveredBytes(), "codex.rate_limits is an opening event")
}

func TestSSEDeliveryHandlesSplitWritesAndCRLF(t *testing.T) {
	c, st := newSSEDeliveryContext(t, "text/event-stream")
	start := "event: message_start\r\ndata: {\"type\":\"message_start\"}\r\n\r\n"
	_, _ = c.Writer.WriteString(start[:10])
	_, _ = c.Writer.WriteString(start[10:])
	delta := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"x\"}}"
	_, _ = c.Writer.WriteString(delta)
	require.Zero(t, st.DeliveredBytes(), "an incomplete block is not judged yet")
	_, _ = c.Writer.WriteString("\n\n")
	require.Positive(t, st.DeliveredBytes())

	// A block that never ends is content once it grows past the limit.
	c2, st2 := newSSEDeliveryContext(t, "text/event-stream")
	_, _ = c2.Writer.WriteString("data: " + strings.Repeat("x", ssePendingLimit+1))
	require.Positive(t, st2.DeliveredBytes())
}

func TestNonSSEResponsesKeepTheByteCount(t *testing.T) {
	c, st := newSSEDeliveryContext(t, "application/json")
	_, _ = c.Writer.WriteString(`{"type":"message_start"}`)
	require.Equal(t, int64(len(`{"type":"message_start"}`)), st.DeliveredBytes())
}

func TestMessagesConversionClientLeavingAfterOpeningEventsIsNotCharged(t *testing.T) {
	// The review's reproduction: the upstream only sent response.created;
	// the legacy Messages conversion wrote message_start, then the client left.
	// Keep this delivery regression with staging disabled; the staged cancel
	// path has separate no-output/no-charge coverage.
	withResilienceSettings(t, func(s *resilience.Settings) { s.Stream.PreludeStaging = false })
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	client, leave := context.WithCancel(context.Background())
	defer leave()
	st := resilience.NewRequestState()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(resilience.WithRequestState(client, st))
	InstallDeliveryTracking(c, time.Now())
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.1\"}}\n\n"))
	}()
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	w := deliveryWriterFrom(c)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.handleAnthropicStreamingResponse(&http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr},
			c, &Account{ID: 7301, Platform: PlatformOpenAI}, "claude-sonnet-4-5", "gpt-5.1", "gpt-5.1", time.Now())
	}()
	require.Eventually(t, func() bool { return w.written.Load() > 0 }, 2*time.Second, 5*time.Millisecond)

	leave()
	disconnected, delivered, _ := st.ResolveDisconnect()
	require.True(t, disconnected)
	require.False(t, delivered, "message_start alone is no delivery")
	usageLog := &UsageLog{Model: "gpt-5.1", InputTokens: 100, TotalCost: 0.01, ActualCost: 0.01}
	require.True(t, resilienceUndeliveredUsage(c.Request.Context(), usageLog, &CostBreakdown{}, 1), "no charge")
	_ = pw.Close()
	<-done
	require.Contains(t, rec.Body.String(), "message_start")
}
