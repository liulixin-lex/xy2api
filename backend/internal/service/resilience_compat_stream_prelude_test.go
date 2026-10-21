//go:build unit

package service

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type preludeCancelBody struct {
	started, closed chan struct{}
	once            sync.Once
}

func (b *preludeCancelBody) Read([]byte) (int, error) {
	close(b.started)
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *preludeCancelBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestCompatPreludeCancelReadFailureIsNeutral(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, messages := range []bool{false, true} {
		for _, interval := range []int{0, 30} {
			t.Run(fmtBool(messages)+"/"+fmtBool(interval > 0), func(t *testing.T) {
				c, rec := compatStreamContext("/v1/chat/completions", "")
				state := resilience.NewRequestState()
				ctx, cancel := context.WithCancel(resilience.WithRequestState(c.Request.Context(), state))
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				body := &preludeCancelBody{started: make(chan struct{}), closed: make(chan struct{})}
				go func() { <-body.started; cancel() }()
				resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}
				svc := compatStreamService(nil, 0, interval, 0)
				account := rawChatCompletionsTestAccount()
				var result *OpenAIForwardResult
				var err error
				if messages {
					result, err = svc.handleAnthropicStreamingResponse(resp, c, account, "model", "model", "model", time.Now())
				} else {
					result, err = svc.handleChatStreamingResponse(resp, c, account, "model", "model", "model", time.Now(), 0)
				}
				require.ErrorIs(t, err, context.Canceled)
				require.Nil(t, result, "canceled prelude must not be billed")
				require.Empty(t, rec.Body.String())
				require.False(t, state.AccountFailed(account.ID))
				require.False(t, state.TurnFailed())
				_, marked := c.Get(OpsUpstreamErrorsKey)
				require.False(t, marked)
			})
		}
	}
}

func TestCompatPreludeCommentHeartbeatStillAllowsFailover(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, messages := range []bool{false, true} {
		t.Run(fmtBool(messages), func(t *testing.T) {
			svc := compatStreamService(&silentUpstream{headers: true, before: keepaliveStallCreated}, 2, 30, 1)
			c, rec := compatStreamContext("/v1/chat/completions", compatChatStreamBody)
			var err error
			if messages {
				_, err = svc.ForwardAsAnthropic(c.Request.Context(), c, rawChatCompletionsTestAccount(), []byte(compatMessagesStreamBody), "", "")
			} else {
				_, err = svc.ForwardAsChatCompletions(c.Request.Context(), c, rawChatCompletionsTestAccount(), []byte(compatChatStreamBody), "", "")
			}
			requireFirstOutputFailover(t, err)
			require.Contains(t, rec.Body.String(), ":\n\n")
			require.NotContains(t, rec.Body.String(), "data:")
			require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
			body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_b\"}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"
			_, err = runPreludeCompat(t, c, messages, strings.NewReader(body), 0, 0)
			require.NoError(t, err)
			require.Contains(t, rec.Body.String(), "resp_b")
			if messages {
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_start"))
			} else {
				require.Equal(t, 1, strings.Count(rec.Body.String(), `"role":"assistant"`))
			}
		})
	}
}

func runPreludeCompat(t *testing.T, c *gin.Context, messages bool, body io.Reader, requestBytes int, interval int) (*OpenAIForwardResult, error) {
	t.Helper()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(body)}
	svc := compatStreamService(nil, 0, interval, 0)
	if messages {
		return svc.handleAnthropicStreamingResponse(resp, c, rawChatCompletionsTestAccount(), "model", "model", "model", time.Now())
	}
	return svc.handleChatStreamingResponse(resp, c, rawChatCompletionsTestAccount(), "model", "model", "model", time.Now(), requestBytes)
}

func TestCompatPreludeFailureBeforeContent(t *testing.T) {
	withResilienceSettings(t, nil)
	created := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_a\"}}\n\n"
	failed := "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_a\",\"usage\":{\"input_tokens\":999},\"error\":{\"message\":\"The service is busy\"}}}\n\n"
	for _, messages := range []bool{false, true} {
		for _, interval := range []int{0, 30} {
			for _, ending := range []string{"failed", "eof", "read_error"} {
				t.Run(fmtBool(messages)+"/"+fmtBool(interval > 0)+"/"+ending, func(t *testing.T) {
					c, rec := compatStreamContext("/v1/chat/completions", "")
					var body io.Reader = strings.NewReader(created)
					if ending == "failed" {
						body = strings.NewReader(created + failed)
					}
					if ending == "read_error" {
						body = io.MultiReader(body, preludeBrokenReader{})
					}
					result, err := runPreludeCompat(t, c, messages, body, 0, interval)
					var failover *UpstreamFailoverError
					require.ErrorAs(t, err, &failover)
					require.Nil(t, result, "failed attempt usage must be discarded")
					require.Empty(t, rec.Body.String())
				})
			}
		}
	}
}

type preludeBrokenReader struct{}

func (preludeBrokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestCompatPreludeOpeningAndIdentity(t *testing.T) {
	withResilienceSettings(t, nil)
	created := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_a\"}}\n\n"
	lateCreated := strings.ReplaceAll(created, "resp_a", "resp_late")
	text := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"
	tool := "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"name\":\"test\",\"call_id\":\"call_a\",\"arguments\":\"\"}}\n\n"
	reasoning := "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"summary\":[]}}\n\n" +
		"data: {\"type\":\"response.reasoning_summary_text.delta\",\"output_index\":0,\"delta\":\"thought\"}\n\n"
	completed := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":1}}}\n\n"
	incomplete := strings.ReplaceAll(strings.ReplaceAll(completed, "response.completed", "response.incomplete"), `"completed"`, `"incomplete"`)
	for _, messages := range []bool{false, true} {
		for _, tc := range []struct{ name, body, want string }{
			{"text", created + text + completed, "hello"},
			{"delta_first", text + lateCreated + text + completed, "hello"},
			{"tool_first", tool + completed, "call_a"},
			{"reasoning", created + reasoning + completed, "thought"},
			{"usage_only", created + completed, ""},
			{"incomplete", created + incomplete, ""},
		} {
			t.Run(fmtBool(messages)+"/"+tc.name, func(t *testing.T) {
				c, rec := compatStreamContext("/v1/chat/completions", "")
				_, err := runPreludeCompat(t, c, messages, strings.NewReader(tc.body), 0, 0)
				require.NoError(t, err)
				require.Contains(t, rec.Body.String(), tc.want)
				var events []gjson.Result
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if strings.HasPrefix(line, "data: {") {
						events = append(events, gjson.Parse(strings.TrimPrefix(line, "data: ")))
					}
				}
				require.NotEmpty(t, events)
				if messages {
					require.Equal(t, "message_start", events[0].Get("type").String())
					require.NotEmpty(t, events[0].Get("message.id").String())
					require.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_start"))
				} else {
					require.Equal(t, "assistant", events[0].Get("choices.0.delta.role").String())
					id := events[0].Get("id").String()
					for _, e := range events {
						require.Equal(t, id, e.Get("id").String())
					}
				}
			})
		}
	}
}

func TestCompatPreludeSwitchOff(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Stream.PreludeStaging = false })
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_a\"}}\n\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_is_overloaded\"}}}\n\n"
	for _, messages := range []bool{false, true} {
		c, rec := compatStreamContext("/v1/chat/completions", "")
		_, err := runPreludeCompat(t, c, messages, strings.NewReader(body), 0, 0)
		var failover *UpstreamFailoverError
		require.Error(t, err)
		require.False(t, errors.As(err, &failover))
		require.Contains(t, rec.Body.String(), "resp_a")
	}
}

func TestCompatPreludeGuardWaitsForContent(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, messages := range []bool{false, true} {
		t.Run(fmtBool(messages), func(t *testing.T) {
			before := keepaliveStallCreated + "data: {\"type\":\"response.queued\"}\n\n"
			svc := compatStreamService(&silentUpstream{headers: true, before: before}, 1, 30, 0)
			c, rec := compatStreamContext("/v1/chat/completions", compatChatStreamBody)
			var err error
			if messages {
				_, err = svc.ForwardAsAnthropic(c.Request.Context(), c, rawChatCompletionsTestAccount(), []byte(compatMessagesStreamBody), "", "")
			} else {
				_, err = svc.ForwardAsChatCompletions(c.Request.Context(), c, rawChatCompletionsTestAccount(), []byte(compatChatStreamBody), "", "")
			}
			requireFirstOutputFailover(t, err)
			require.Empty(t, rec.Body.String(), "staging stays uncommitted when service heartbeats are disabled")
		})
	}
}

func TestCompatPreludeHeartbeatFailoverAndTerminal(t *testing.T) {
	withResilienceSettings(t, nil)
	created := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_a\"}}\n\n"
	for _, messages := range []bool{false, true} {
		c, rec := compatStreamContext("/v1/chat/completions", "")
		c.Header("Content-Type", "text/event-stream")
		n, err := c.Writer.Write([]byte(": ping\n\n"))
		require.NoError(t, err)
		recordOpenAIStreamKeepaliveBytes(c, n)
		c.Writer.Flush()
		result, err := runPreludeCompat(t, c, messages, strings.NewReader(created), 0, 0)
		var failover *UpstreamFailoverError
		require.ErrorAs(t, err, &failover)
		require.Nil(t, result)
		require.NotContains(t, rec.Body.String(), "resp_a")
		completed := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_b\",\"status\":\"completed\",\"usage\":{\"input_tokens\":2}}}\n\n"
		_, err = runPreludeCompat(t, c, messages, strings.NewReader(strings.ReplaceAll(created, "resp_a", "resp_b")+completed), 0, 0)
		require.NoError(t, err)
		require.Contains(t, rec.Body.String(), "resp_b")
	}
}

func TestCompatPreludeCanceledClientClosesUpstream(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, messages := range []bool{false, true} {
		c, rec := compatStreamContext("/v1/chat/completions", "")
		ctx, cancel := context.WithCancel(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		pr, pw := io.Pipe()
		defer func() { _ = pw.Close() }()
		wrote := make(chan struct{})
		go func() { _, _ = pw.Write([]byte(keepaliveStallCreated)); close(wrote); <-ctx.Done() }()
		go func() { <-wrote; cancel() }()
		resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: pr}
		svc := compatStreamService(nil, 0, 0, 0)
		var err error
		if messages {
			_, err = svc.handleAnthropicStreamingResponse(resp, c, rawChatCompletionsTestAccount(), "model", "model", "model", time.Now())
		} else {
			_, err = svc.handleChatStreamingResponse(resp, c, rawChatCompletionsTestAccount(), "model", "model", "model", time.Now(), 0)
		}
		cancel()
		require.Error(t, err)
		var failover *UpstreamFailoverError
		require.False(t, errors.As(err, &failover))
		require.Empty(t, rec.Body.String())
		_, writeErr := pw.Write([]byte("late"))
		require.ErrorIs(t, writeErr, io.ErrClosedPipe)
	}
}

type cancelAfterContentWriter struct {
	gin.ResponseWriter
	onFlush func()
}

func (w *cancelAfterContentWriter) Flush() {
	w.ResponseWriter.Flush()
	w.onFlush()
}

func TestCompatPreludeDrainsUsageAfterClientCancellation(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, messages := range []bool{false, true} {
		for _, interval := range []int{0, 30} {
			t.Run(fmtBool(messages)+"/"+fmtBool(interval > 0), func(t *testing.T) {
				c, rec := compatStreamContext("/v1/chat/completions", "")
				requestState := resilience.NewRequestState()
				ctx, cancel := context.WithCancel(resilience.WithRequestState(c.Request.Context(), requestState))
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				c.Writer = &cancelAfterContentWriter{ResponseWriter: c.Writer, onFlush: func() {
					if strings.Contains(rec.Body.String(), "hello") {
						cancel()
					}
				}}
				pr, pw := io.Pipe()
				defer func() { _ = pr.Close() }()
				defer func() { _ = pw.Close() }()
				wroteTerminal := make(chan error, 1)
				go func() {
					_, err := io.WriteString(pw, keepaliveStallCreated+"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
					if err == nil {
						<-ctx.Done()
						_, err = io.WriteString(pw, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":13,\"output_tokens\":21}}}\n\n")
					}
					_ = pw.Close()
					wroteTerminal <- err
				}()
				resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: pr}
				svc := compatStreamService(nil, 0, interval, 0)
				account := rawChatCompletionsTestAccount()
				var result *OpenAIForwardResult
				var err error
				if messages {
					result, err = svc.handleAnthropicStreamingResponse(resp, c, account, "model", "model", "model", time.Now())
				} else {
					result, err = svc.handleChatStreamingResponse(resp, c, account, "model", "model", "model", time.Now(), 0)
				}
				require.NoError(t, err)
				require.NoError(t, <-wroteTerminal, "must drain through completed after client cancellation")
				require.Equal(t, 13, result.Usage.InputTokens)
				require.Equal(t, 21, result.Usage.OutputTokens)
				require.False(t, requestState.AccountFailed(account.ID))
				require.False(t, requestState.TurnFailed())
				_, marked := c.Get(OpsStreamErrorKey)
				require.False(t, marked)
			})
		}
	}
}

func TestCompatPreludeTerminalSemanticsWithLargeRequest(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, messages := range []bool{false, true} {
		for _, ending := range []string{
			`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`,
			`{"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
			`{"type":"response.failed","response":{"error":{"code":"content_policy_violation","message":"blocked"}}}`,
		} {
			c, rec := compatStreamContext("/v1/chat/completions", "")
			_, err := runPreludeCompat(t, c, messages, strings.NewReader(keepaliveStallCreated+"data: "+ending+"\n\n"), 128<<10, 0)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			require.NotEmpty(t, rec.Body.String())
		}
	}
}
