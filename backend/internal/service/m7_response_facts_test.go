//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestM7NilResultResponseFacts(t *testing.T) {
	c, _ := compatStreamContext("/v1/messages", "")
	state := resilience.NewRequestState()
	state.EnableGatewayRequest(time.Now())
	state.NoteGatewaySend(11)
	c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
	svc := &OpenAIGatewayService{}
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.done\",\"response\":{\"status\":\"future\"}}\n\n"))}
	result, err := svc.handleAnthropicBufferedStreamingResponse(resp, c, &Account{ID: 11}, "fixture", "fixture", "fixture", time.Now())
	require.Error(t, err)
	require.Nil(t, result)
	meta, ok := state.ClaimGatewayRequest(false)
	require.True(t, ok)
	require.NotNil(t, meta.UsageObserved)
	require.False(t, *meta.UsageObserved)
	require.Equal(t, "missing", *meta.UsageSource)
	require.True(t, *meta.TerminalEventSeen)
	require.False(t, *meta.TerminalEventValid)
	require.Equal(t, "sse", *meta.ResponseBodyType)
}

func TestM7UnknownTerminalMapping(t *testing.T) {
	for _, source := range []string{"chat", "anthropic"} {
		c, rec := compatStreamContext("/v1/responses", "")
		state := resilience.NewRequestState()
		c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
		var err error
		if source == "chat" {
			wire := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"eos_token\"}]}\n\n"
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
			_, err = (&OpenAIGatewayService{}).streamChatCompletionsAsResponses(c, resp, &Account{ID: 1}, "fixture", nil, nil, false, nil, "fixture", "fixture", nil, nil, time.Now())
		} else {
			wire := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"role\":\"assistant\",\"content\":[]}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"some_future_reason\"}}\n\n"
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
			_, err = (&GatewayService{}).handleResponsesBufferedStreamingResponse(resp, c, "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
		}
		require.NoError(t, err)
		require.Equal(t, 200, rec.Code)
		field := "finish_reason"
		if source == "anthropic" {
			field = "stop_reason"
		}
		require.Equal(t, []apicompat.ConversionNotice{{Field: field, Action: "map"}}, resilience.SnapshotConversionNotices(c.Request.Context()))
	}
}
