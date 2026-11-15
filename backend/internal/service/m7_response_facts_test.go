//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

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
