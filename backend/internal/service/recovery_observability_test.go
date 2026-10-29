//go:build unit

package service

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
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
	for _, payload := range []string{`{"data":[]}`, `{"output":[]}`, `{"candidates":[{"finishReason":"STOP"}]}`} {
		c, st := newSSEDeliveryContext(t, "application/json")
		_, err := c.Writer.WriteString(payload)
		require.NoError(t, err)
		require.Nil(t, st.EndToEndFirstOutputMs())
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
