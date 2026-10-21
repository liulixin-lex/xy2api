//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestRawChatStrictTerminalCancellationTakesPriority(t *testing.T) {
	withResilienceSettings(t, nil)
	c, _ := compatStreamContext("/v1/chat/completions", "")
	state := resilience.NewRequestState()
	ctx, cancel := context.WithCancel(resilience.WithRequestState(c.Request.Context(), state))
	c.Request = c.Request.WithContext(ctx)
	cancel()
	body := `data: {"error":{"code":"server_is_overloaded","message":"busy"}}` + "\n\ndata: [DONE]\n\n"
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
	_, err := compatStreamService(nil, 0, 0, 0).streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "model", "model", "model", nil, nil, time.Now(), 0)
	require.NoError(t, err)
	require.False(t, state.TurnFailed())
	_, marked := c.Get(OpsStreamErrorKey)
	require.False(t, marked)
}

func TestRawChatStrictErrorTerminal(t *testing.T) {
	withResilienceSettings(t, nil)
	failure := `data: {"error":{"type":"server_error","code":"server_is_overloaded","message":"busy"}}` + "\n\ndata: [DONE]\n\n"
	content := `data: {"object":"chat.completion.chunk","choices":[{"delta":{"content":"hello"}}]}` + "\n\n"
	for _, prefix := range []string{"", "event: error\n", content} {
		t.Run(prefix, func(t *testing.T) {
			c, rec := compatStreamContext("/v1/chat/completions", "")
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(prefix + failure))}
			svc := compatStreamService(nil, 0, 0, 0)
			result, err := svc.streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "model", "model", "model", nil, nil, time.Now(), 0)
			require.Error(t, err)
			var failover *UpstreamFailoverError
			if prefix != content {
				require.ErrorAs(t, err, &failover)
				require.Nil(t, result)
				require.Empty(t, rec.Body.String())
			} else {
				require.False(t, errors.As(err, &failover))
				require.NotNil(t, result)
				require.Equal(t, 1, strings.Count(rec.Body.String(), `"error"`))
				require.Equal(t, 1, strings.Count(rec.Body.String(), "[DONE]"))
				require.True(t, IsResponseCommitted(c), "handler must not append a duplicate terminal")
				require.True(t, state.TurnFailed())
				_, ok := c.Get(OpsStreamErrorKey)
				require.True(t, ok, "HTTP 200 stream must be recorded as failed")
			}
		})
	}
}

func TestRawChatStrictTerminalSwitchOffAndSuccess(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(fmtBool(strict), func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Stream.StrictTerminal = strict })
			for _, body := range []string{
				`data: {"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":0}}` + "\n\n",
				`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n",
			} {
				c, _ := compatStreamContext("/v1/chat/completions", "")
				resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
				_, err := compatStreamService(nil, 0, 0, 0).streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "model", "model", "model", nil, nil, time.Now(), 0)
				require.NoError(t, err)
			}
			if !strict {
				c, rec := compatStreamContext("/v1/chat/completions", "")
				body := `data: {"error":{"code":"server_is_overloaded"}}` + "\n\ndata: [DONE]\n\n"
				resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
				_, err := compatStreamService(nil, 0, 0, 0).streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "model", "model", "model", nil, nil, time.Now(), 0)
				require.NoError(t, err)
				require.Contains(t, rec.Body.String(), "[DONE]")
			}
		})
	}
}
