//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestEndToEndFirstOutputIncludesQueueAndFailedAttempt(t *testing.T) {
	c, st := newSSEDeliveryContext(t, "text/event-stream")
	// Three seconds queued, a 20-second failed attempt, then five seconds TTFT.
	st.InitDelivery(c.Request.Context(), time.Now().Add(-28*time.Second), deliveryWriterFrom(c).deliveredBytes)
	_, err := c.Writer.WriteString("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
	require.NoError(t, err)
	require.InDelta(t, 28000, *st.EndToEndFirstOutputMs(), 100)
	first := *st.EndToEndFirstOutputMs()
	_, err = c.Writer.WriteString("data: {\"type\":\"response.output_text.delta\",\"delta\":\"again\"}\n\n")
	require.NoError(t, err)
	require.Equal(t, first, *st.EndToEndFirstOutputMs())
	ttft := 5000
	result := &ForwardResult{Model: "gpt-5", FirstTokenMs: &ttft}
	log := (&GatewayService{}).buildRecordUsageLog(c.Request.Context(), &recordUsageCoreInput{}, result,
		&APIKey{}, &User{}, &Account{}, nil, "gpt-5", 1, 1, 1, 0, false, nil)
	require.Equal(t, 5000, *log.FirstTokenMs)
	require.Equal(t, first, *log.EndToEndFirstOutputMs)
}

func TestEndToEndFirstOutputExcludesProtocolOnlyDelivery(t *testing.T) {
	for name, payload := range map[string]string{
		"heartbeat":       ": ping\n\n",
		"failed":          "data: {\"type\":\"response.failed\",\"response\":{\"output\":[]}}\n\n",
		"done":            "data: [DONE]\n\n",
		"empty success":   "data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n",
		"chat finish":     "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"messages finish": "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		"opening":         "data: {\"type\":\"response.created\"}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			c, st := newSSEDeliveryContext(t, "text/event-stream")
			_, err := c.Writer.WriteString(payload)
			require.NoError(t, err)
			require.Nil(t, st.EndToEndFirstOutputMs())
			if name == "failed" || name == "done" {
				require.Positive(t, st.DeliveredBytes(), "billing criterion is unchanged")
			}
		})
	}
	for _, payload := range []string{`{"data":[]}`, `{"output":[]}`, `{"candidates":[{"finishReason":"STOP"}]}`,
		`{"choices":[{"message":{"role":"assistant","content":[]},"finish_reason":"stop"}]}`,
		`{"choices":[{"message":{"content":{}}}]}`,
		`{"error":{"message":"failed"},"choices":[{"message":{"content":"hello"}}]}`} {
		c, st := newSSEDeliveryContext(t, "application/json")
		_, err := c.Writer.WriteString(payload)
		require.NoError(t, err)
		require.Nil(t, st.EndToEndFirstOutputMs())
	}
}

func TestEndToEndFirstOutputAcceptsPlaceholderErrors(t *testing.T) {
	for _, tc := range []struct{ contentType, payload string }{
		{"application/json", `{"status":"completed","error":null,"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}`},
		{"text/event-stream", "data: {\"error\":null,\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"},
	} {
		c, st := newSSEDeliveryContext(t, tc.contentType)
		_, err := c.Writer.WriteString(tc.payload)
		require.NoError(t, err)
		require.NotNil(t, st.EndToEndFirstOutputMs())
	}
}

func TestEndToEndFirstOutputAcrossProtocols(t *testing.T) {
	for _, payload := range []string{
		`{"choices":[{"delta":{"content":"hi"}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`,
		`{"type":"content_block_delta","delta":{"text":"hi"}}`,
		`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}}`,
	} {
		c, st := newSSEDeliveryContext(t, "text/event-stream")
		_, err := c.Writer.WriteString("data: " + payload + "\n\n")
		require.NoError(t, err)
		require.NotNil(t, st.EndToEndFirstOutputMs(), payload)
	}
}

func TestFirstOutputTimeoutPersistsOnlyFailedAttemptProfile(t *testing.T) {
	c, st := newSSEDeliveryContext(t, "text/event-stream")
	start := time.Now().Add(-20 * time.Second)
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: 1, Model: "gpt-5.5", Warm: false, BodyBytes: 256 << 10, AttemptStart: start})
	svc := &OpenAIGatewayService{}
	svc.newOpenAIFirstOutputTimeoutError(c.Request.Context(), c, &Account{ID: 1, Platform: PlatformOpenAI}, nil, "", start, "gpt-5.5", "high", 20*time.Second, "semantic_output", nil)
	raw, _ := c.Get(OpsUpstreamErrorsKey)
	events := raw.([]*OpsUpstreamErrorEvent)
	require.Len(t, events, 1)
	require.NotNil(t, events[0].Warm)
	require.False(t, *events[0].Warm)
	require.Equal(t, 256<<10, events[0].BodyBytes)
	require.Equal(t, "high", events[0].Effort)
	require.InDelta(t, 20000, events[0].AttemptElapsedMs, 100)
	require.Equal(t, int64(20000), events[0].TimeoutMs)
	entry := &OpsInsertErrorLogInput{UpstreamErrors: events}
	require.NoError(t, sanitizeOpsUpstreamErrors(entry))
	stored, err := ParseOpsUpstreamErrors(*entry.UpstreamErrorsJSON)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.NotNil(t, stored[0].Warm)
	require.False(t, *stored[0].Warm)
	require.Equal(t, events[0].BodyBytes, stored[0].BodyBytes)
}
