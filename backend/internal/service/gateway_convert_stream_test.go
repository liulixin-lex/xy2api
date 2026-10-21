//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// W1.1/W1.2 for the Gateway conversions of an Anthropic upstream
// (Responses and Chat Completions inbound): the opening is deferred to the
// first content or terminal, failures before it switch accounts, and a
// stream that breaks after output ends with the inbound protocol's terminal
// frame instead of a synthesized success. Each case below pins one of the
// 0.4.4 review findings (P0, R1-P1-1, R1-P1-2, R2-P1①②③, PM7, RA-1).

const (
	gwMsgStart    = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_A\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-x\",\"content\":[],\"usage\":{\"input_tokens\":100}}}\n\n"
	gwPing        = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
	gwTextStart   = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"
	gwTextDelta   = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"
	gwTextDelta2  = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n\n"
	gwBlockStop   = "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
	gwToolStart   = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"lookup\",\"input\":{}}}\n\n"
	gwMsgStop     = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	gwOverloaded  = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	gwInvalid     = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"prompt is too long\"}}\n\n"
	gwComment     = ": upstream keepalive\n\n"
	gwStreamModel = "claude-x"
)

func gwMsgDelta(stopReason string) string {
	return "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"" + stopReason + "\"},\"usage\":{\"output_tokens\":7}}\n\n"
}

type gwConvertPath struct {
	name string
	run  func(svc *GatewayService, c *gin.Context, account *Account, resp *http.Response) (*ForwardResult, error)
}

func gwConvertPaths() []gwConvertPath {
	return []gwConvertPath{
		{"responses", func(svc *GatewayService, c *gin.Context, account *Account, resp *http.Response) (*ForwardResult, error) {
			return svc.handleResponsesStreamingResponse(resp, c, account, "gpt-5", gwStreamModel, nil, time.Now(), apicompat.ResponsesClientToolMapping{})
		}},
		{"chat", func(svc *GatewayService, c *gin.Context, account *Account, resp *http.Response) (*ForwardResult, error) {
			return svc.handleCCStreamingFromAnthropic(resp, c, account, "gpt-5", gwStreamModel, nil, time.Now())
		}},
	}
}

func gwTestAccount() *Account {
	return &Account{ID: 41, Name: "anthropic-a", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
}

func gwConvertService(keepalive, interval int) *GatewayService {
	return &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		StreamKeepaliveInterval: keepalive, StreamDataIntervalTimeout: interval, MaxLineSize: defaultMaxLineSize,
	}}}
}

// gwWriter is a thread-safe recorder; fail decides which writes fail.
type gwWriter struct {
	mu      sync.Mutex
	rec     *httptest.ResponseRecorder
	fail    func(data string) bool
	failed  bool
	flushed chan string
}

func newGWWriter() *gwWriter {
	return &gwWriter{rec: httptest.NewRecorder(), flushed: make(chan string, 64)}
}

func (w *gwWriter) Header() http.Header { return w.rec.Header() }

func (w *gwWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rec.WriteHeader(code)
}

func (w *gwWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed || (w.fail != nil && w.fail(string(p))) {
		w.failed = true
		return 0, errors.New("client went away")
	}
	return w.rec.Write(p)
}

func (w *gwWriter) Flush() {
	w.mu.Lock()
	body := w.rec.Body.String()
	w.mu.Unlock()
	select {
	case w.flushed <- body:
	default:
	}
}

func (w *gwWriter) Body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rec.Body.String()
}

func gwContext(path string, w *gwWriter) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c
}

// gwUpstream is an Anthropic stream fed by the test. A context, when given,
// ends the stream with its error like a real transport does.
type gwUpstream struct {
	pr     *io.PipeReader
	pw     *io.PipeWriter
	closed atomic.Bool
}

func newGWUpstream(ctx context.Context) *gwUpstream {
	pr, pw := io.Pipe()
	u := &gwUpstream{pr: pr, pw: pw}
	if ctx != nil {
		context.AfterFunc(ctx, func() { _ = pw.CloseWithError(ctx.Err()) })
	}
	return u
}

func (u *gwUpstream) Read(p []byte) (int, error) { return u.pr.Read(p) }

func (u *gwUpstream) Close() error {
	u.closed.Store(true)
	return u.pr.Close()
}

// send fails instead of blocking when the handler stopped reading.
func (u *gwUpstream) send(t *testing.T, frames ...string) {
	t.Helper()
	t.Cleanup(func() { _ = u.pw.Close() })
	for _, frame := range frames {
		written := make(chan error, 1)
		go func() {
			_, err := io.WriteString(u.pw, frame)
			written <- err
		}()
		select {
		case err := <-written:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatalf("the stream handler stopped reading the upstream before %q", frame)
		}
	}
}

func (u *gwUpstream) response(ctx context.Context) *http.Response {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Request-Id": []string{"req_up"}}, Body: u, Request: req}
}

type gwOutcome struct {
	result *ForwardResult
	err    error
}

func gwRun(path gwConvertPath, svc *GatewayService, c *gin.Context, resp *http.Response) chan gwOutcome {
	done := make(chan gwOutcome, 1)
	go func() {
		result, err := path.run(svc, c, gwTestAccount(), resp)
		done <- gwOutcome{result, err}
	}()
	return done
}

func gwAwait(t *testing.T, done chan gwOutcome, within time.Duration) gwOutcome {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(within):
		t.Fatalf("stream handler did not return within %s", within)
		return gwOutcome{}
	}
}

func gwStatic(stream string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream)),
		Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil)}
}

func gwPathURL(name string) string {
	if name == "chat" {
		return "/v1/chat/completions"
	}
	return "/v1/responses"
}

// requireOnlyHeartbeats checks that the client saw comments at most.
func requireOnlyHeartbeats(t *testing.T, c *gin.Context, body string) {
	t.Helper()
	for _, block := range strings.Split(body, "\n\n") {
		if block == "" {
			continue
		}
		require.True(t, strings.HasPrefix(block, ":"), "only heartbeats before content, got %q in %q", block, body)
	}
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c), "heartbeats are no output")
	require.NotContains(t, body, "msg_A")
}

func requireGWFailover(t *testing.T, err error) *UpstreamFailoverError {
	t.Helper()
	var failover *UpstreamFailoverError
	require.True(t, errors.As(err, &failover), "expected a failover, got %v", err)
	return failover
}

// requireProtocolTerminal checks that a broken stream ended with the inbound
// protocol's failure frame and nothing that claims success.
func requireProtocolTerminal(t *testing.T, path string, body string) gjson.Result {
	t.Helper()
	if path == "chat" {
		require.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"), body)
		frames := strings.Split(strings.TrimSuffix(body, "data: [DONE]\n\n"), "\n\n")
		last := frames[len(frames)-2]
		require.True(t, strings.HasPrefix(last, "data: {\"error\""), "chat ends with data:{error} before [DONE]: %q", body)
		require.NotContains(t, body, `"finish_reason":"stop"`)
		return gjson.Parse(strings.TrimPrefix(last, "data: ")).Get("error")
	}
	require.NotContains(t, body, "response.completed")
	require.Contains(t, body, "event: response.failed\n")
	blocks := strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n")
	last := blocks[len(blocks)-1]
	require.True(t, strings.HasPrefix(last, "event: response.failed\ndata: "), body)
	failed := gjson.Parse(strings.TrimPrefix(last, "event: response.failed\ndata: "))
	require.Equal(t, "failed", failed.Get("response.status").String())
	return failed.Get("response.error")
}

func TestGatewayConvertRetryableErrorBeforeContentFailsOver(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		t.Run(path.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			result, err := path.run(gwConvertService(0, 0), c, gwTestAccount(), gwStatic(gwMsgStart+gwPing+gwTextStart+gwOverloaded))
			failover := requireGWFailover(t, err)
			require.Equal(t, 529, failover.StatusCode)
			require.Nil(t, result, "a failed attempt carries no usage into the next one")
			requireOnlyHeartbeats(t, c, w.Body())
		})
	}
}

func TestGatewayConvertEndBeforeContentFailsOver(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		for _, tc := range []struct {
			name  string
			cause error
		}{{"eof", nil}, {"read_error", io.ErrUnexpectedEOF}} {
			t.Run(path.name+"/"+tc.name, func(t *testing.T) {
				w := newGWWriter()
				c := gwContext(gwPathURL(path.name), w)
				up := newGWUpstream(nil)
				done := gwRun(path, gwConvertService(0, 0), c, up.response(nil))
				up.send(t, gwMsgStart, gwTextStart)
				_ = up.pw.CloseWithError(tc.cause)
				got := gwAwait(t, done, 3*time.Second)
				requireGWFailover(t, got.err)
				require.Nil(t, got.result)
				requireOnlyHeartbeats(t, c, w.Body())
			})
		}
	}
}

func TestGatewayConvertBreakAfterContentEndsWithProtocolTerminal(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		for _, tc := range []struct {
			name, tail string
			cause      error
		}{
			{"eof_without_terminal", gwTextDelta2, nil},
			{"read_error", "", io.ErrUnexpectedEOF},
			{"retryable_error_event", gwOverloaded, nil},
		} {
			t.Run(path.name+"/"+tc.name, func(t *testing.T) {
				w := newGWWriter()
				c := gwContext(gwPathURL(path.name), w)
				up := newGWUpstream(nil)
				done := gwRun(path, gwConvertService(0, 0), c, up.response(nil))
				up.send(t, gwMsgStart, gwTextStart, gwTextDelta)
				if tc.tail != "" {
					up.send(t, tc.tail)
				}
				_ = up.pw.CloseWithError(tc.cause)
				got := gwAwait(t, done, 3*time.Second)
				require.Error(t, got.err)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(got.err, &failover), "output reached the client: %v", got.err)
				body := w.Body()
				require.Contains(t, body, "hello")
				requireProtocolTerminal(t, path.name, body)
				require.True(t, IsResponseCommitted(c), "the handler must not append a second terminal")
			})
		}
	}
}

func TestGatewayConvertNonRetryableErrorBeforeContentIsNotReplayed(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		t.Run(path.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			_, err := path.run(gwConvertService(0, 0), c, gwTestAccount(), gwStatic(gwMsgStart+gwInvalid))
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover), "an invalid request is not replayed: %v", err)
			body := w.Body()
			errObj := requireProtocolTerminal(t, path.name, body)
			require.Contains(t, errObj.Get("message").String(), "prompt is too long")
			if path.name == "responses" {
				require.True(t, strings.HasPrefix(body, "event: response.created\n"), "strict SDKs need created first: %q", body)
			}
		})
	}
}

func TestGatewayConvertLegitimateEndingsAreNotReplayed(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		for _, tc := range []struct{ name, stream, status string }{
			{"usage_only_empty_answer", gwMsgStart + gwMsgDelta("end_turn") + gwMsgStop, "completed"},
			{"incomplete_max_tokens", gwMsgStart + gwTextStart + gwTextDelta + gwBlockStop + gwMsgDelta("max_tokens") + gwMsgStop, "incomplete"},
			{"content_refusal", gwMsgStart + gwMsgDelta("refusal") + gwMsgStop, "completed"},
			{"stop_reason_then_eof", gwMsgStart + gwTextStart + gwTextDelta + gwBlockStop + gwMsgDelta("end_turn"), "completed"},
		} {
			t.Run(path.name+"/"+tc.name, func(t *testing.T) {
				w := newGWWriter()
				c := gwContext(gwPathURL(path.name), w)
				result, err := path.run(gwConvertService(0, 0), c, gwTestAccount(), gwStatic(tc.stream))
				require.NoError(t, err)
				require.Equal(t, 100, result.Usage.InputTokens)
				require.Equal(t, 7, result.Usage.OutputTokens)
				body := w.Body()
				if path.name == "chat" {
					require.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"))
					require.NotContains(t, body, `"error"`)
					return
				}
				require.True(t, strings.HasPrefix(body, "event: response.created\n"), body)
				terminal := "response.completed"
				if tc.status == "incomplete" {
					terminal = "response.incomplete"
				}
				require.Contains(t, body, "event: "+terminal+"\n")
				require.NotContains(t, body, "response.failed")
				require.Equal(t, tc.status, gjson.Get(body[strings.LastIndex(body, "data: ")+6:], "response.status").String())
			})
		}
	}
}

func TestGatewayConvertToolCallFirstReleasesImmediately(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		t.Run(path.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			up := newGWUpstream(nil)
			done := gwRun(path, gwConvertService(0, 0), c, up.response(nil))
			t.Cleanup(func() { _ = up.pw.Close() })
			up.send(t, gwMsgStart, gwToolStart)
			want := "function_call"
			if path.name == "chat" {
				want = "tool_calls"
			}
			require.Eventually(t, func() bool { return strings.Contains(w.Body(), want) }, 2*time.Second, 10*time.Millisecond,
				"a tool call identity is output and must not wait for more upstream events")
			up.send(t, gwBlockStop, gwMsgDelta("tool_use"), gwMsgStop)
			_ = up.pw.Close()
			got := gwAwait(t, done, 3*time.Second)
			require.NoError(t, got.err)
		})
	}
}

// P0 and R1-P1-1: a client that leaves before the first output is a
// cancellation: the upstream is closed, nothing is switched or billed.
func TestGatewayConvertClientCancelBeforeOutputStopsUpstream(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		t.Run(path.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			ctx, cancel := context.WithCancel(context.Background())
			c.Request = c.Request.WithContext(ctx)
			up := newGWUpstream(nil)
			done := gwRun(path, gwConvertService(0, 0), c, up.response(nil))
			up.send(t, gwMsgStart, gwTextStart)
			cancel()
			got := gwAwait(t, done, 3*time.Second)
			require.ErrorIs(t, got.err, context.Canceled)
			require.Nil(t, got.result, "nothing to bill")
			require.True(t, up.closed.Load(), "the upstream attempt is closed")
			require.Empty(t, w.Body())
		})
	}
}

// P0: after output a departed client does not stop the upstream; the usage
// is read to the end and nothing counts against the account.
func TestGatewayConvertClientGoneAfterOutputDrainsUsage(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		t.Run(path.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Request = c.Request.WithContext(ctx)
			up := newGWUpstream(nil)
			done := gwRun(path, gwConvertService(0, 0), c, up.response(nil))
			up.send(t, gwMsgStart, gwTextStart, gwTextDelta)
			require.Eventually(t, func() bool { return strings.Contains(w.Body(), "hello") }, 2*time.Second, 10*time.Millisecond)
			w.mu.Lock()
			w.failed = true
			w.mu.Unlock()
			cancel()
			up.send(t, gwTextDelta2, gwBlockStop, gwMsgDelta("end_turn"), gwMsgStop)
			_ = up.pw.Close()
			got := gwAwait(t, done, 3*time.Second)
			require.NoError(t, got.err)
			require.NotNil(t, got.result)
			require.True(t, got.result.ClientDisconnect)
			require.Equal(t, 100, got.result.Usage.InputTokens)
			require.Equal(t, 7, got.result.Usage.OutputTokens, "usage read to the end")
			require.False(t, up.closed.Load(), "the upstream was not cut")
			_, hasOps := c.Get(OpsUpstreamErrorsKey)
			require.False(t, hasOps, "no account failure is reported")
		})
	}
}

// R1-P1-2: heartbeats during staging are comments, counted as heartbeat
// bytes; the attempt may still switch afterwards.
func TestGatewayConvertStagingHeartbeatIsNotOutput(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		t.Run(path.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			up := newGWUpstream(nil)
			done := gwRun(path, gwConvertService(1, 0), c, up.response(nil))
			up.send(t, gwMsgStart)
			require.Eventually(t, func() bool { return strings.Contains(w.Body(), ":\n\n") }, 3*time.Second, 20*time.Millisecond)
			require.Equal(t, http.StatusOK, w.rec.Code)
			up.send(t, gwOverloaded)
			got := gwAwait(t, done, 3*time.Second)
			requireGWFailover(t, got.err)
			requireOnlyHeartbeats(t, c, w.Body())
		})
	}
}

// R2-P1①: the handler finishes a heartbeat-only stream with the inbound
// protocol's terminal frame; it relies on the delivery tracking seeing the
// staged comments as heartbeats.
func TestGatewayConvertStagingHeartbeatIsTrackedAsHeartbeat(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		t.Run(path.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			c.Request = c.Request.WithContext(resilience.WithRequestState(context.Background(), resilience.NewRequestState()))
			InstallDeliveryTracking(c, time.Now())
			_, err := path.run(gwConvertService(0, 0), c, gwTestAccount(), gwStatic(gwMsgStart+gwPing+gwComment+gwOverloaded))
			requireGWFailover(t, err)
			only, tracked := DeliveryOnlyHeartbeats(c)
			require.True(t, tracked)
			require.True(t, only, "staged comments are heartbeats: %q", w.Body())
		})
	}
}

// R2-P1③: liveness the upstream sends while the opening is staged reaches
// the client at once as a comment, not after the staging ends.
func TestGatewayConvertUpstreamHeartbeatPassesThroughDuringStaging(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		for _, tc := range []struct{ name, frame string }{{"ping", gwPing}, {"comment", gwComment}} {
			t.Run(path.name+"/"+tc.name, func(t *testing.T) {
				w := newGWWriter()
				c := gwContext(gwPathURL(path.name), w)
				up := newGWUpstream(nil)
				done := gwRun(path, gwConvertService(0, 0), c, up.response(nil))
				t.Cleanup(func() { _ = up.pw.Close() })
				up.send(t, gwMsgStart, tc.frame)
				require.Eventually(t, func() bool { return strings.HasPrefix(w.Body(), ":") }, 2*time.Second, 10*time.Millisecond)
				requireOnlyHeartbeats(t, c, w.Body())
				up.send(t, gwTextStart, gwTextDelta, gwBlockStop, gwMsgDelta("end_turn"), gwMsgStop)
				_ = up.pw.Close()
				got := gwAwait(t, done, 3*time.Second)
				require.NoError(t, got.err)
				require.Contains(t, w.Body(), "hello")
			})
		}
	}
}

// PM7 and R1-P1-1: a heartbeat that cannot be written means the client
// left. Nothing is switched afterwards, not even on the first-event
// deadline, and an answer that still arrives is read for its usage.
func TestGatewayConvertHeartbeatWriteFailureIsClientGone(t *testing.T) {
	withResilienceSettings(t, nil)
	failHeartbeats := func(data string) bool { return strings.HasPrefix(data, ":") }
	for _, path := range gwConvertPaths() {
		t.Run(path.name+"/error_after_failure", func(t *testing.T) {
			w := newGWWriter()
			w.fail = failHeartbeats
			c := gwContext(gwPathURL(path.name), w)
			got := gwAwaitStatic(t, path, c, gwMsgStart+gwPing+gwOverloaded)
			require.Error(t, got.err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(got.err, &failover), "client gone: no switch, %v", got.err)
			require.True(t, IsResponseCommitted(c))
		})
		t.Run(path.name+"/deadline_after_failure", func(t *testing.T) {
			w := newGWWriter()
			w.fail = failHeartbeats
			c := gwContext(gwPathURL(path.name), w)
			guardCtx, guard := newFirstEventGuard(context.Background(), 600*time.Millisecond)
			defer guard.Release()
			up := newGWUpstream(guardCtx)
			done := gwRun(path, gwConvertService(0, 0), c, up.response(guardCtx))
			up.send(t, gwMsgStart, gwPing)
			got := gwAwait(t, done, 3*time.Second)
			require.Error(t, got.err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(got.err, &failover), "the deadline does not switch a departed client: %v", got.err)
		})
		t.Run(path.name+"/answer_after_failure_is_drained", func(t *testing.T) {
			w := newGWWriter()
			w.fail = failHeartbeats
			c := gwContext(gwPathURL(path.name), w)
			guardCtx, guard := newFirstEventGuard(context.Background(), 600*time.Millisecond)
			defer guard.Release()
			up := newGWUpstream(guardCtx)
			done := gwRun(path, gwConvertService(0, 0), c, up.response(guardCtx))
			up.send(t, gwMsgStart, gwPing, gwTextStart, gwTextDelta)
			// Past the deadline: content stopped the guard although nothing
			// could be written.
			time.Sleep(900 * time.Millisecond)
			up.send(t, gwBlockStop, gwMsgDelta("end_turn"), gwMsgStop)
			_ = up.pw.Close()
			got := gwAwait(t, done, 3*time.Second)
			require.NoError(t, got.err)
			require.True(t, got.result.ClientDisconnect)
			require.Equal(t, 7, got.result.Usage.OutputTokens)
		})
	}
}

func gwAwaitStatic(t *testing.T, path gwConvertPath, c *gin.Context, stream string) gwOutcome {
	t.Helper()
	return gwAwait(t, gwRun(path, gwConvertService(0, 0), c, gwStatic(stream)), 3*time.Second)
}

// The first-event guard of the attempt covers the staged opening: an
// upstream that opens and then stays silent is switched at the deadline.
func TestGatewayConvertFirstEventGuardCoversStagedOpening(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		t.Run(path.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			guardCtx, guard := newFirstEventGuard(context.Background(), 500*time.Millisecond)
			defer guard.Release()
			up := newGWUpstream(guardCtx)
			done := gwRun(path, gwConvertService(0, 0), c, up.response(guardCtx))
			up.send(t, gwMsgStart, gwPing, gwTextStart)
			got := gwAwait(t, done, 3*time.Second)
			failover := requireGWFailover(t, got.err)
			require.Contains(t, string(failover.ResponseBody), "first_output_timeout")
			requireOnlyHeartbeats(t, c, w.Body())
		})
	}
}

func TestGatewayConvertForwardArmsFirstEventGuard(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Budget.AnthropicFirstEventTimeoutSeconds = 1 })
	for _, endpoint := range []struct {
		name, path, body string
		call             func(*GatewayService, context.Context, *gin.Context, *Account, []byte) (*ForwardResult, error)
	}{
		{"responses", "/v1/responses", `{"model":"claude-x","input":"hi","stream":true}`, func(s *GatewayService, ctx context.Context, c *gin.Context, a *Account, b []byte) (*ForwardResult, error) {
			return s.ForwardAsResponses(ctx, c, a, b, nil)
		}},
		{"chat", "/v1/chat/completions", `{"model":"claude-x","messages":[{"role":"user","content":"hi"}],"stream":true}`, func(s *GatewayService, ctx context.Context, c *gin.Context, a *Account, b []byte) (*ForwardResult, error) {
			return s.ForwardAsChatCompletions(ctx, c, a, b, nil)
		}},
	} {
		for _, upstream := range []*silentUpstream{{}, {headers: true, before: gwMsgStart}} {
			name := endpoint.name + "/no_headers"
			if upstream.headers {
				name = endpoint.name + "/opening_then_silence"
			}
			t.Run(name, func(t *testing.T) {
				svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream, tlsFPProfileService: &TLSFingerprintProfileService{}}
				w := newGWWriter()
				c := gwContext(endpoint.path, w)
				st := resilience.NewRequestState()
				ctx := resilience.WithRequestState(context.Background(), st)
				st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
				c.Request = c.Request.WithContext(ctx)
				account := &Account{ID: 42, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "k"}}
				done := make(chan gwOutcome, 1)
				go func() {
					result, err := endpoint.call(svc, ctx, c, account, []byte(endpoint.body))
					done <- gwOutcome{result, err}
				}()
				got := gwAwait(t, done, 5*time.Second)
				failover := requireGWFailover(t, got.err)
				require.Contains(t, string(failover.ResponseBody), "first_output_timeout")
				require.NotContains(t, w.Body(), "msg_A")
			})
		}
	}
}

func TestGatewayConvertStallAfterOutputEndsWithProtocolTerminal(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		t.Run(path.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			up := newGWUpstream(nil)
			t.Cleanup(func() { _ = up.pw.Close() })
			done := gwRun(path, gwConvertService(0, 1), c, up.response(nil))
			up.send(t, gwMsgStart, gwTextStart, gwTextDelta)
			got := gwAwait(t, done, 4*time.Second)
			require.Error(t, got.err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(got.err, &failover))
			requireProtocolTerminal(t, path.name, w.Body())
		})
	}
}

func TestGatewayConvertStallBeforeOutputFailsOver(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		t.Run(path.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			up := newGWUpstream(nil)
			t.Cleanup(func() { _ = up.pw.Close() })
			done := gwRun(path, gwConvertService(0, 1), c, up.response(nil))
			up.send(t, gwMsgStart)
			got := gwAwait(t, done, 4*time.Second)
			requireGWFailover(t, got.err)
			requireOnlyHeartbeats(t, c, w.Body())
		})
	}
}

// The usage of a failed attempt never reaches the result of the attempt
// that succeeds, and only the successful attempt's identity is sent.
func TestGatewayConvertFailedAttemptLeavesNoTrace(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range gwConvertPaths() {
		t.Run(path.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			svc := gwConvertService(0, 0)
			first, err := path.run(svc, c, gwTestAccount(), gwStatic(strings.ReplaceAll(gwMsgStart, `"input_tokens":100`, `"input_tokens":999`)+gwOverloaded))
			requireGWFailover(t, err)
			require.Nil(t, first)
			second := strings.ReplaceAll(gwMsgStart, "msg_A", "msg_B")
			result, err := path.run(svc, c, gwTestAccount(), gwStatic(second+gwTextStart+gwTextDelta+gwBlockStop+gwMsgDelta("end_turn")+gwMsgStop))
			require.NoError(t, err)
			require.Equal(t, 100, result.Usage.InputTokens)
			body := w.Body()
			require.NotContains(t, body, "999")
			if path.name == "responses" {
				require.Contains(t, body, "msg_B")
				require.Equal(t, 1, strings.Count(body, "event: response.created\n"))
			} else {
				require.Equal(t, 1, strings.Count(body, `"role":"assistant"`))
			}
		})
	}
}

// R2-P1② and RA-1: a synthesized response.failed carries a retry hint only
// when a retry can help.
func TestGatewayConvertSynthesizedFailedFrameRetryAdvice(t *testing.T) {
	withResilienceSettings(t, nil)
	path := gwConvertPaths()[0]
	for _, tc := range []struct {
		name, event string
		hint        bool
	}{
		{"transient", gwOverloaded, true},
		{"invalid_request", gwInvalid, false},
		{"content_rejection", "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"Output blocked by content filtering policy\"}}\n\n", false},
		{"balance", "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"Your credit balance is too low to access the Anthropic API.\"}}\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext("/v1/responses", w)
			ctx := resilience.WithRetryAdvice(context.Background(), resilience.RetryAdvice{Seconds: 3, Source: "recovery"})
			c.Request = c.Request.WithContext(ctx)
			_, err := path.run(gwConvertService(0, 0), c, gwTestAccount(), gwStatic(gwMsgStart+gwTextStart+gwTextDelta+tc.event))
			require.Error(t, err)
			errObj := requireProtocolTerminal(t, path.name, w.Body())
			if tc.hint {
				require.Equal(t, "3", errObj.Get("headers.retry-after").String(), errObj.Raw)
				return
			}
			require.False(t, errObj.Get("headers").Exists(), errObj.Raw)
			require.NotContains(t, errObj.Get("message").String(), "try again in")
		})
	}
}

// Switched off, both conversions keep the 0.4.4 behavior: the opening goes
// out at once and an upstream end without message_stop still completes.
func TestGatewayConvertSwitchesOffKeepBaseline(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) {
		s.Stream.PreludeStaging = false
		s.Stream.StrictTerminal = false
	})
	for _, path := range gwConvertPaths() {
		t.Run(path.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			result, err := path.run(gwConvertService(0, 0), c, gwTestAccount(), gwStatic(gwMsgStart+gwOverloaded))
			require.NoError(t, err)
			require.Equal(t, 100, result.Usage.InputTokens)
			body := w.Body()
			if path.name == "chat" {
				require.Contains(t, body, `"role":"assistant"`)
				require.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"))
				return
			}
			require.True(t, strings.HasPrefix(body, "event: response.created\n"))
			require.Contains(t, body, "event: response.completed\n")
		})
	}
}

// Staging alone defers the opening and switches failures before it; the
// end of the stream after output keeps the baseline.
func TestGatewayConvertStagingWithoutStrictTerminal(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Stream.StrictTerminal = false })
	for _, path := range gwConvertPaths() {
		t.Run(path.name+"/before_content", func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			_, err := path.run(gwConvertService(0, 0), c, gwTestAccount(), gwStatic(gwMsgStart+gwTextStart))
			requireGWFailover(t, err)
			requireOnlyHeartbeats(t, c, w.Body())
		})
		// Review DELTA-P2-1: a non-retryable error before content is dropped
		// as in 0.4.4, and the end that follows must not replay it.
		t.Run(path.name+"/non_retryable_error_is_not_replayed", func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			result, err := path.run(gwConvertService(0, 0), c, gwTestAccount(), gwStatic(gwMsgStart+gwInvalid))
			require.NoError(t, err)
			require.Equal(t, 100, result.Usage.InputTokens)
			if path.name == "responses" {
				require.True(t, strings.HasPrefix(w.Body(), "event: response.created\n"))
				require.Contains(t, w.Body(), "event: response.completed\n")
			} else {
				require.True(t, strings.HasSuffix(w.Body(), "data: [DONE]\n\n"))
			}
		})
		t.Run(path.name+"/after_content", func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			_, err := path.run(gwConvertService(0, 0), c, gwTestAccount(), gwStatic(gwMsgStart+gwTextStart+gwTextDelta))
			require.NoError(t, err)
			if path.name == "responses" {
				require.Contains(t, w.Body(), "event: response.completed\n")
			} else {
				require.True(t, strings.HasSuffix(w.Body(), "data: [DONE]\n\n"))
			}
		})
	}
}

// Strict terminal alone writes the opening at once (no staging) and still
// switches an attempt that failed before anything was written.
func TestGatewayConvertStrictTerminalWithoutStaging(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Stream.PreludeStaging = false })
	for _, path := range gwConvertPaths() {
		t.Run(path.name+"/error_first", func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			_, err := path.run(gwConvertService(0, 0), c, gwTestAccount(), gwStatic(gwOverloaded))
			requireGWFailover(t, err)
			require.Empty(t, w.Body())
		})
		t.Run(path.name+"/eof_after_opening", func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			_, err := path.run(gwConvertService(0, 0), c, gwTestAccount(), gwStatic(gwMsgStart))
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			requireProtocolTerminal(t, path.name, w.Body())
		})
	}
}

// Closing the upstream body, as the caller does when the attempt ends,
// releases the read pump even when the handler returned mid-stream.
func TestGatewayConvertReadPumpEndsWithTheBody(t *testing.T) {
	withResilienceSettings(t, nil)
	pumpAlive := func() bool {
		buf := make([]byte, 1<<20)
		return strings.Contains(string(buf[:runtime.Stack(buf, true)]), "(*gatewayConvertStream).run.func")
	}
	for _, path := range gwConvertPaths() {
		t.Run(path.name, func(t *testing.T) {
			w := newGWWriter()
			c := gwContext(gwPathURL(path.name), w)
			up := newGWUpstream(nil)
			done := gwRun(path, gwConvertService(1, 1), c, up.response(nil))
			up.send(t, gwMsgStart, gwOverloaded)
			requireGWFailover(t, gwAwait(t, done, 3*time.Second).err)
			// The pump may be blocked reading more; the caller's close ends it.
			go func() { _, _ = io.WriteString(up.pw, gwPing) }()
			require.NoError(t, up.Close())
			require.Eventually(t, func() bool { return !pumpAlive() }, 3*time.Second, 20*time.Millisecond, "read pump leaked")
		})
	}
}
