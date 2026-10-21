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

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type rawChatCommentFlushWriter struct {
	gin.ResponseWriter
	flushed chan struct{}
}

func (w *rawChatCommentFlushWriter) Flush() {
	w.ResponseWriter.Flush()
	select {
	case w.flushed <- struct{}{}:
	default:
	}
}

func TestRawChatStrictCommentHeartbeatAllowsFailover(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, requestBytes := range []int{0, 128 << 10} {
		name := "normal_request"
		if requestBytes > 0 {
			name = "large_request"
		}
		t.Run(name, func(t *testing.T) {
			c, rec := compatStreamContext("/v1/chat/completions", "")
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			writer := &rawChatCommentFlushWriter{ResponseWriter: c.Writer, flushed: make(chan struct{}, 1)}
			c.Writer = writer
			InstallDeliveryTracking(c, time.Now())
			pr, pw := io.Pipe()
			defer func() { _ = pr.Close() }()
			defer func() { _ = pw.Close() }()
			allowFailure := make(chan struct{})
			go func() {
				_, _ = io.WriteString(pw, ": heartbeat\n\nevent: error\nid: attempt_a\nretry: 1000\n: still waiting\n")
				<-allowFailure
				_, _ = io.WriteString(pw, "data: {\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"busy\"}}\n\ndata: [DONE]\n\n")
				_ = pw.Close()
			}()
			svc := compatStreamService(nil, 0, 0, 0)
			account := rawChatCompletionsTestAccount()
			type attemptResult struct {
				result *OpenAIForwardResult
				err    error
			}
			done := make(chan attemptResult, 1)
			go func() {
				result, err := svc.streamRawChatCompletions(c, &http.Response{StatusCode: 200, Header: http.Header{}, Body: pr}, account, "model", "model", "model", nil, nil, time.Now(), requestBytes)
				done <- attemptResult{result: result, err: err}
			}()
			select {
			case <-writer.flushed:
			case <-time.After(time.Second):
				t.Error("the comment heartbeat must flush while the upstream is still waiting")
			}
			close(allowFailure)
			got := <-done
			var failover *UpstreamFailoverError
			require.ErrorAs(t, got.err, &failover)
			require.Nil(t, got.result)
			require.Equal(t, ": heartbeat\n\n: still waiting\n", rec.Body.String())
			require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c), "comments must not close the replay window")
			require.Zero(t, state.DeliveredBytes())

			accountB := *account
			accountB.ID++
			bodyB := "data: {\"id\":\"chat_b\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n"
			result, err := svc.streamRawChatCompletions(c, &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(bodyB))}, &accountB, "model", "model", "model", nil, nil, time.Now(), requestBytes)
			require.NoError(t, err)
			require.Equal(t, 2, result.Usage.InputTokens)
			require.Equal(t, 3, result.Usage.OutputTokens)
			require.Equal(t, ": heartbeat\n\n: still waiting\n"+bodyB, rec.Body.String(), "only B's data may reach the client")
		})
	}
}

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

func TestRawChatStrictErrorFieldNeedsFailureEvidence(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		failure bool
	}{
		{"empty_string", `{"error":"","choices":[{"delta":{"content":"a"}}]}`, false},
		{"false", `{"error":false,"choices":[{"delta":{"content":"a"}}]}`, false},
		{"empty_object", `{"error":{},"choices":[{"delta":{"content":"a"}}]}`, false},
		{"blank_fields", `{"error":{"message":" ","code":null},"choices":[{"delta":{"content":"a"}}]}`, false},
		{"null", `{"error":null,"choices":[{"delta":{"content":"a"}}]}`, false},
		{"blank_string", `{"error":"  ","choices":[{"delta":{"content":"a"}}]}`, false},
		{"zero", `{"error":0,"choices":[{"delta":{"content":"a"}}]}`, false},
		{"all_fields_empty", `{"error":{"message":"","code":"","type":""},"choices":[{"delta":{"content":"a"}}]}`, false},
		{"string", `{"error":"rate limited"}`, true},
		{"type_only", `{"error":{"type":"server_error"}}`, true},
		{"message", `{"error":{"message":"busy"}}`, true},
		{"code", `{"error":{"code":"server_is_overloaded"}}`, true},
		{"numeric_code", `{"error":{"code":500}}`, true},
		{"type_error", `{"type":"error","error":"busy"}`, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			state := openAIRawStreamTerminalState{strict: true}
			state.ObserveDataLine(tt.payload)
			require.Equal(t, tt.failure, state.failurePayload != "")
		})
	}
}

func TestRawChatStrictPlaceholderErrorFieldStreamsNormally(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, placeholder := range []string{`""`, `false`, `{}`} {
		t.Run(placeholder, func(t *testing.T) {
			c, rec := compatStreamContext("/v1/chat/completions", "")
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			body := `data: {"id":"chat_a","object":"chat.completion.chunk","error":` + placeholder +
				`,"choices":[{"delta":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3}}` +
				"\n\ndata: [DONE]\n\n"
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
			result, err := compatStreamService(nil, 0, 0, 0).streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "model", "model", "model", nil, nil, time.Now(), 0)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 3, result.Usage.OutputTokens)
			require.Equal(t, body, rec.Body.String())
			require.False(t, state.TurnFailed())
			_, marked := c.Get(OpsStreamErrorKey)
			require.False(t, marked)
		})
	}
}
