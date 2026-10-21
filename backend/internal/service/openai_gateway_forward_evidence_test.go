//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAIHTTPFailureKeepsStructuredClassificationEvidence(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, kind := range []string{"native", "passthrough", "chat"} {
		t.Run(kind, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4","instructions":"test","input":"hello","stream":false,"messages":[{"role":"user","content":"hello"}]}`)
			c, _ := compatStreamContext("/v1/responses", string(body))
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 503, Header: http.Header{},
				Body: io.NopCloser(strings.NewReader(`{"error":{"type":"overloaded_error","message":"busy"}}`))}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			_, err := forwardCapabilityAttempt(svc, c, kind, rawChatCompletionsTestAccount(), body)
			require.Error(t, err)
			raw, exists := c.Get(OpsUpstreamErrorsKey)
			require.True(t, exists)
			events := raw.([]*OpsUpstreamErrorEvent)
			require.NotEmpty(t, events)
			require.Equal(t, string(resilience.CategoryCapacityShed), events[0].ResilienceCategory)
		})
	}
}

func TestOpenAIFirstOutputTimeoutRecordsActualSeconds(t *testing.T) {
	withResilienceSettings(t, nil)
	c, _ := compatStreamContext("/v1/responses", "")
	svc := compatStreamService(nil, 100, 0, 0)
	_ = svc.newOpenAIFirstOutputTimeoutError(context.Background(), c, rawChatCompletionsTestAccount(), nil, "", time.Now(), "model", "", 1500*time.Millisecond, "semantic_output", nil)
	raw, _ := c.Get(OpsUpstreamErrorsKey)
	event := raw.([]*OpsUpstreamErrorEvent)[0]
	require.Equal(t, "first_output_timeout", event.Reason)
	require.Contains(t, event.Detail, "timeout_ms=1500")
	require.Contains(t, event.Detail, "timeout_seconds=1.5")
	require.Equal(t, string(resilience.CategoryTransient), event.ResilienceCategory)
}
