//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// W1.2 for the OpenAI Responses passthrough: the first-output deadline and
// the stall check of the native path, its bounded first-output stage, and
// the 0.4.4 review findings on heartbeats and departed clients.

const (
	ptCreated   = "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_pt\",\"status\":\"in_progress\"}}\n\n"
	ptProgress  = "event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"sequence_number\":1,\"response\":{\"id\":\"resp_pt\",\"status\":\"in_progress\"}}\n\n"
	ptDelta     = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":2,\"delta\":\"hello\"}\n\n"
	ptCompleted = "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"id\":\"resp_pt\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":5}}}\n\n"
	ptOverload  = "event: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_pt\",\"status\":\"failed\",\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"busy\"}}}\n\n"
	ptComment   = ": relay keepalive\n\n"
)

type ptOutcome struct {
	result *openaiStreamingResultPassthrough
	err    error
}

func ptRun(svc *OpenAIGatewayService, c *gin.Context, resp *http.Response) chan ptOutcome {
	done := make(chan ptOutcome, 1)
	go func() {
		result, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, rawChatCompletionsTestAccount(), time.Now(), "gpt-5.4", "gpt-5.4")
		done <- ptOutcome{result, err}
	}()
	return done
}

func ptAwait(t *testing.T, done chan ptOutcome, within time.Duration) ptOutcome {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(within):
		t.Fatalf("passthrough stream did not return within %s", within)
		return ptOutcome{}
	}
}

func ptContext(w *gwWriter) *gin.Context { return gwContext("/v1/responses", w) }

func TestOpenAIPassthroughFirstOutputDeadlineSwitchesSilentAttempt(t *testing.T) {
	withResilienceSettings(t, nil)
	w := newGWWriter()
	c := ptContext(w)
	up := newGWUpstream(nil)
	start := time.Now()
	done := ptRun(compatStreamService(nil, 1, 0, 0), c, up.response(nil))
	up.send(t, ptCreated, ptProgress)
	got := ptAwait(t, done, 4*time.Second)
	requireFirstOutputFailover(t, got.err)
	require.Less(t, time.Since(start), 3*time.Second)
	requireOnlyHeartbeats(t, c, w.Body())
	require.True(t, up.closed.Load(), "the silent attempt is closed")
}

func TestOpenAIPassthroughHeaderWaitIsBoundedByTheDeadline(t *testing.T) {
	withResilienceSettings(t, nil)
	svc := compatStreamService(&silentUpstream{}, 1, 0, 0)
	w := newGWWriter()
	c := ptContext(w)
	body := []byte(`{"model":"gpt-5.4","input":"hi","stream":true}`)
	done := make(chan error, 1)
	go func() {
		_, err := svc.forwardOpenAIPassthrough(context.Background(), c, rawChatCompletionsTestAccount(), body, body, "gpt-5.4", false, nil, true, time.Now())
		done <- err
	}()
	select {
	case err := <-done:
		requireFirstOutputFailover(t, err)
	case <-time.After(4 * time.Second):
		t.Fatal("the header wait was not bounded by the first-output deadline")
	}
	require.Empty(t, w.Body())
}

// Review P1-C on this path: before output the deadline owns the attempt,
// so the shorter stall check does not end it first.
func TestOpenAIPassthroughDeadlineOwnsStallBeforeOutput(t *testing.T) {
	withResilienceSettings(t, nil)
	w := newGWWriter()
	c := ptContext(w)
	up := newGWUpstream(nil)
	start := time.Now()
	done := ptRun(compatStreamService(nil, 3, 1, 0), c, up.response(nil))
	up.send(t, ptCreated)
	got := ptAwait(t, done, 6*time.Second)
	requireFirstOutputFailover(t, got.err)
	require.GreaterOrEqual(t, time.Since(start), 2500*time.Millisecond)
}

func TestOpenAIPassthroughStallBeforeOutputWithoutDeadlineFailsOver(t *testing.T) {
	withResilienceSettings(t, nil)
	w := newGWWriter()
	c := ptContext(w)
	up := newGWUpstream(nil)
	done := ptRun(compatStreamService(nil, 0, 1, 0), c, up.response(nil))
	up.send(t, ptCreated)
	got := ptAwait(t, done, 4*time.Second)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, got.err, &failover)
	requireOnlyHeartbeats(t, c, w.Body())
}

func TestOpenAIPassthroughStallAfterOutputEndsTheStream(t *testing.T) {
	withResilienceSettings(t, nil)
	w := newGWWriter()
	c := ptContext(w)
	up := newGWUpstream(nil)
	done := ptRun(compatStreamService(nil, 0, 1, 0), c, up.response(nil))
	up.send(t, ptCreated, ptDelta)
	got := ptAwait(t, done, 4*time.Second)
	require.Error(t, got.err)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(got.err, &failover), "output reached the client: %v", got.err)
	require.Contains(t, w.Body(), "hello")
}

// R1-P1-2: heartbeats while the opening is staged are comments counted as
// heartbeat bytes; the attempt may still switch.
func TestOpenAIPassthroughStagingHeartbeatIsNotOutput(t *testing.T) {
	withResilienceSettings(t, nil)
	w := newGWWriter()
	c := ptContext(w)
	up := newGWUpstream(nil)
	done := ptRun(compatStreamService(nil, 10, 0, 1), c, up.response(nil))
	up.send(t, ptCreated)
	require.Eventually(t, func() bool { return strings.HasPrefix(w.Body(), ":") }, 3*time.Second, 20*time.Millisecond)
	up.send(t, ptOverload)
	got := ptAwait(t, done, 3*time.Second)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, got.err, &failover)
	requireOnlyHeartbeats(t, c, w.Body())
	require.NotContains(t, w.Body(), "resp_pt")
}

// R2-P1③: upstream comment heartbeats are passed through at once, not
// staged with the opening.
func TestOpenAIPassthroughUpstreamCommentPassesThroughDuringStaging(t *testing.T) {
	withResilienceSettings(t, nil)
	w := newGWWriter()
	c := ptContext(w)
	up := newGWUpstream(nil)
	done := ptRun(compatStreamService(nil, 10, 0, 0), c, up.response(nil))
	up.send(t, ptCreated, ptComment)
	require.Eventually(t, func() bool { return strings.Contains(w.Body(), ": relay keepalive") }, 2*time.Second, 10*time.Millisecond)
	requireOnlyHeartbeats(t, c, w.Body())
	up.send(t, ptDelta, ptCompleted)
	_ = up.pw.Close()
	got := ptAwait(t, done, 3*time.Second)
	require.NoError(t, got.err)
	body := w.Body()
	require.Less(t, strings.Index(body, ": relay keepalive"), strings.Index(body, "resp_pt"))
}

// PM7 and R1-P1-1: a heartbeat that cannot be written means the client
// left; nothing afterwards switches accounts, not even the deadline.
func TestOpenAIPassthroughHeartbeatWriteFailureIsClientGone(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name, after string
	}{{"failed_event", ptOverload}, {"deadline", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newGWWriter()
			w.fail = func(data string) bool { return strings.HasPrefix(data, ":") }
			c := ptContext(w)
			up := newGWUpstream(nil)
			done := ptRun(compatStreamService(nil, 2, 0, 1), c, up.response(nil))
			up.send(t, ptCreated)
			require.Eventually(t, func() bool {
				w.mu.Lock()
				defer w.mu.Unlock()
				return w.failed
			}, 3*time.Second, 20*time.Millisecond)
			if tc.after != "" {
				up.send(t, tc.after)
			}
			got := ptAwait(t, done, 4*time.Second)
			require.Error(t, got.err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(got.err, &failover), "client gone: no switch, %v", got.err)
		})
	}
}

// P0 and R1-P1-1: a client that cancels before the first output ends the
// attempt and its upstream without a switch.
func TestOpenAIPassthroughClientCancelBeforeOutputStopsUpstream(t *testing.T) {
	withResilienceSettings(t, nil)
	w := newGWWriter()
	c := ptContext(w)
	ctx, cancel := context.WithCancel(context.Background())
	c.Request = c.Request.WithContext(ctx)
	up := newGWUpstream(nil)
	done := ptRun(compatStreamService(nil, 10, 0, 0), c, up.response(nil))
	up.send(t, ptCreated)
	cancel()
	got := ptAwait(t, done, 3*time.Second)
	require.Error(t, got.err)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(got.err, &failover))
	require.True(t, up.closed.Load())
	require.Empty(t, w.Body())
}

// P0: after output a departed client does not stop the upstream.
func TestOpenAIPassthroughClientGoneAfterOutputDrainsUsage(t *testing.T) {
	withResilienceSettings(t, nil)
	w := newGWWriter()
	c := ptContext(w)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	up := newGWUpstream(nil)
	done := ptRun(compatStreamService(nil, 10, 0, 0), c, up.response(nil))
	up.send(t, ptCreated, ptDelta)
	require.Eventually(t, func() bool { return strings.Contains(w.Body(), "hello") }, 2*time.Second, 10*time.Millisecond)
	w.mu.Lock()
	w.failed = true
	w.mu.Unlock()
	cancel()
	// The upstream is still read after the cancel: send fails on a closed body.
	up.send(t, ptCompleted)
	_ = up.pw.Close()
	got := ptAwait(t, done, 3*time.Second)
	require.NoError(t, got.err)
	require.Equal(t, 11, got.result.usage.InputTokens)
	require.Equal(t, 5, got.result.usage.OutputTokens)
}

// Switched off, the passthrough keeps 0.4.4: no first-output deadline.
func TestOpenAIPassthroughDeadlineSwitchOff(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Stream.StrictTerminal = false })
	w := newGWWriter()
	c := ptContext(w)
	up := newGWUpstream(nil)
	done := ptRun(compatStreamService(nil, 1, 0, 0), c, up.response(nil))
	up.send(t, ptCreated)
	select {
	case got := <-done:
		t.Fatalf("returned before the upstream ended: %v", got.err)
	case <-time.After(1500 * time.Millisecond):
	}
	up.send(t, ptDelta, ptCompleted)
	_ = up.pw.Close()
	got := ptAwait(t, done, 3*time.Second)
	require.NoError(t, got.err)
}

// A comment inside an event still being received is passed through as a
// heartbeat, but the blank line that ends the event stays with it.
func TestOpenAIPassthroughCommentInsideEventKeepsItsFraming(t *testing.T) {
	withResilienceSettings(t, nil)
	w := newGWWriter()
	c := ptContext(w)
	upstream := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_pt\",\"status\":\"in_progress\"}}\n: inside\n\n" +
		ptDelta + ptCompleted
	result, err := compatStreamService(nil, 0, 0, 0).handleStreamingResponsePassthrough(c.Request.Context(), gwStatic(upstream), c, rawChatCompletionsTestAccount(), time.Now(), "gpt-5.4", "gpt-5.4")
	require.NoError(t, err)
	require.Equal(t, 5, result.usage.OutputTokens)
	body := w.Body()
	require.True(t, strings.HasPrefix(body, ": inside\n\n"), body)
	require.Contains(t, body, "\"id\":\"resp_pt\",\"status\":\"in_progress\"}}\n\nevent: response.output_text.delta\n", "the created event keeps its blank line")
}

// Review DELTA-P1-2: a response.failed that matches an error passthrough
// rule after heartbeats committed 200 must end the Responses stream with
// created then failed carrying the rule's message, not a bare JSON body.
func TestOpenAIPassthroughRuleAfterHeartbeatEndsWithResponsesTerminal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		strict bool
	}{{"upstream_comment", true}, {"keepalive_goroutine_switch_off", false}} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Stream.StrictTerminal = tc.strict })
			w := newGWWriter()
			c := ptContext(w)
			bindPassthroughRule(c, "openai", []string{"context_length_exceeded"}, 400)
			up := newGWUpstream(nil)
			keepalive := 0
			if !tc.strict {
				keepalive = 1
			}
			done := ptRun(compatStreamService(nil, 0, 0, keepalive), c, up.response(nil))
			if tc.strict {
				up.send(t, ": keep-alive\n\n")
			}
			require.Eventually(t, func() bool { return strings.HasPrefix(w.Body(), ":") }, 3*time.Second, 10*time.Millisecond)
			up.send(t, buildContextLengthFailedSSE())
			_ = up.pw.Close()
			got := ptAwait(t, done, 3*time.Second)
			require.Error(t, got.err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(got.err, &failover))

			body := w.Body()
			require.Equal(t, http.StatusOK, w.rec.Code)
			require.NotContains(t, body, "\n{", "no bare JSON after the heartbeat")
			var events []string
			for _, block := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
				if !strings.HasPrefix(block, ":") {
					events = append(events, block)
				}
			}
			require.Len(t, events, 2, body)
			require.True(t, strings.HasPrefix(events[0], "event: response.created\ndata: "), body)
			require.True(t, strings.HasPrefix(events[1], "event: response.failed\ndata: "), body)
			created := gjson.Parse(strings.TrimPrefix(events[0], "event: response.created\ndata: "))
			failed := gjson.Parse(strings.TrimPrefix(events[1], "event: response.failed\ndata: "))
			require.Equal(t, int64(0), created.Get("sequence_number").Int())
			require.Equal(t, int64(1), failed.Get("sequence_number").Int())
			require.Equal(t, created.Get("response.id").String(), failed.Get("response.id").String())
			require.Contains(t, failed.Get("response.error.message").String(), "context window", "the rule's message")
			require.False(t, failed.Get("response.error.headers").Exists(), "a 400 rule carries no retry hint")
			require.True(t, IsResponseCommitted(c))
		})
	}
}

// Counter-example: without a heartbeat the rule still answers with its
// status code and JSON body.
func TestOpenAIPassthroughRuleWithoutHeartbeatKeepsJSON(t *testing.T) {
	withResilienceSettings(t, nil)
	w := newGWWriter()
	c := ptContext(w)
	bindPassthroughRule(c, "openai", []string{"context_length_exceeded"}, 400)
	_, err := compatStreamService(nil, 0, 0, 0).handleStreamingResponsePassthrough(c.Request.Context(), gwStatic(buildContextLengthFailedSSE()), c, rawChatCompletionsTestAccount(), time.Now(), "gpt-5.4", "gpt-5.4")
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, w.rec.Code)
	require.Contains(t, gjson.Get(w.Body(), "error.message").String(), "context window")
	require.NotContains(t, w.Body(), "event:")
}
