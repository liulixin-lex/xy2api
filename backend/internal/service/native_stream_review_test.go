package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/responseturn"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestReviewNativeStreamIncompleteTerminalCannotSucceed(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "ordinary"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			ctx := nativeRelayContext(context.Background())
			out := newNativeStreamTestRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			frame := nativeRelaySSE("response.completed", "\"sequence_number\":0,\"response\":{\"id\":\"resp_incomplete\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")
			resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.TrimSuffix(frame, "\n")))}
			svc := nativeRelayService()
			var err error
			if passthrough {
				_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
			} else {
				_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
			}
			require.Error(t, err, "EOF without the dispatching blank line is not a complete terminal event")
			require.NotContains(t, out.Body.String(), "resp_incomplete", "incomplete upstream identity must not be exposed")
		})
	}
}

func TestReviewNativeStreamEmptyCompletedAfterCreatedFailsSameResponse(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			ctx := nativeRelayContext(context.Background())
			out := newNativeStreamTestRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			body := nativeRelaySSE("response.created", `"sequence_number":0,"response":{"id":"resp_empty","created_at":123,"model":"fixture"}`) +
				nativeRelaySSE("response.completed", `"sequence_number":1,"response":{"id":"resp_empty","status":"completed","output":[]}`)
			resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
			svc := nativeRelayService()
			var err error
			if passthrough {
				_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
			} else {
				_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
			}
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover), "delivered response identity forbids retry")
			output := out.Body.String()
			require.Contains(t, output, "event: response.created\n")
			require.NotContains(t, output, "event: response.completed\n")
			require.Equal(t, 1, strings.Count(output, "event: response.failed\n"))
			require.Contains(t, output, `"code":"openai_silent_refusal"`)
			require.Contains(t, output, `"sequence_number":1`)
			require.Contains(t, output, `"id":"resp_empty"`)
		})
	}
}

type reviewFlushErrorWriter struct {
	header   http.Header
	writes   int
	flushes  int
	deadline time.Time
}

func (w *reviewFlushErrorWriter) Header() http.Header         { return w.header }
func (w *reviewFlushErrorWriter) WriteHeader(int)             {}
func (w *reviewFlushErrorWriter) Write(p []byte) (int, error) { w.writes++; return len(p), nil }
func (w *reviewFlushErrorWriter) Flush()                      {}
func (w *reviewFlushErrorWriter) FlushError() error           { w.flushes++; return io.ErrClosedPipe }
func (w *reviewFlushErrorWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestReviewNativeStreamFlushFailureCannotSucceed(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "ordinary"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			ctx := nativeRelayContext(context.Background())
			out := &reviewFlushErrorWriter{header: make(http.Header)}
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			body := nativeRelaySSE("response.created", "\"response\":{\"id\":\"resp_flush\"}") + nativeRelaySSE("response.completed", "\"response\":{\"id\":\"resp_flush\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")
			resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
			svc := nativeRelayService()
			var err error
			if passthrough {
				_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
			} else {
				_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
			}
			require.Error(t, err, "failed network Flush must stop delivery")
			require.True(t, errors.Is(err, context.Canceled), "downstream failure must not penalize provider: %v", err)
			require.Equal(t, 1, out.writes)
			require.Equal(t, 1, out.flushes)
		})
	}
}

func TestReviewNativeStreamSchedulingWriterExposesDeadlineControl(t *testing.T) {
	_, r, _ := nativeCommitFixture(t)
	out := &reviewFlushErrorWriter{header: make(http.Header)}
	c, _ := gin.CreateTestContext(out)
	w := &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
	deadline := time.Now().Add(time.Second)
	require.NoError(t, http.NewResponseController(w).SetWriteDeadline(deadline))
	require.Equal(t, deadline, out.deadline)
}

func TestReviewNativeStreamScannerRejectsIncompleteEOF(t *testing.T) {
	scanner := newNativeSSEEventScanner(bufio.NewScanner(strings.NewReader("data: {\"type\":\"response.created\"}\n")))
	require.False(t, scanner.Scan())
	require.ErrorIs(t, scanner.Err(), io.ErrUnexpectedEOF)
}

func TestReviewNativeStreamDownstreamDeadlineAndCancellation(t *testing.T) {
	for _, cancelExplicitly := range []bool{false, true} {
		name := "write_timeout"
		if cancelExplicitly {
			name = "control_cancel"
		}
		t.Run(name, func(t *testing.T) {
			completed := make(chan error, 1)
			started := make(chan struct{})
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := nativeRelayContext(parent)
				timeout := 50 * time.Millisecond
				if cancelExplicitly {
					timeout = time.Minute
				}
				close(started)
				_, err := writeNativeStreamFrame(ctx, w, []byte(strings.Repeat("x", 16<<20)), timeout)
				completed <- err
			}))
			defer gateway.Close()
			connection, err := net.Dial("tcp", strings.TrimPrefix(gateway.URL, "http://"))
			require.NoError(t, err)
			defer func() { _ = connection.Close() }()
			_, err = io.WriteString(connection, "GET / HTTP/1.1\r\nHost: fixture\r\n\r\n")
			require.NoError(t, err)
			<-started
			if cancelExplicitly {
				time.Sleep(50 * time.Millisecond)
				cancel()
			}
			select {
			case err := <-completed:
				require.ErrorIs(t, err, context.Canceled)
				if !cancelExplicitly {
					require.Contains(t, err.Error(), "slow_consumer")
				}
			case <-time.After(time.Second):
				_ = connection.Close()
				t.Fatal("downstream socket write outlived its deadline/cancellation")
			}
		})
	}
}

func TestReviewNativeStreamExplicitCancellationCauseWins(t *testing.T) {
	for _, reason := range []responseturn.Reason{responseturn.ReasonUserStop, responseturn.ReasonAdminCancel, responseturn.ReasonLeaseLost, responseturn.Reason("permission_revoked"), responseturn.Reason("authorization_unavailable")} {
		t.Run(string(reason), func(t *testing.T) {
			ctx, r, d := nativeCommitFixture(t)
			parent, cancel := context.WithCancelCause(ctx)
			r.clientContext = parent
			d.ctx = parent
			cancel(&responseturn.Cancellation{Reason: reason})
			err := nativeStreamDownstreamError(parent, io.ErrClosedPipe)
			require.Contains(t, err.Error(), string(reason))
			require.Equal(t, ControlledCancelReason(reason), ControlledStreamSnapshot(parent).CancelReason)
			require.True(t, d.classifyFailureDomains("write_failure", err).Effect == "none")
		})
	}
}

func TestReviewNativeStreamMultiLineReassemblyIsBounded(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "ordinary"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			ctx := nativeRelayContext(context.Background())
			out := newNativeStreamTestRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			// Every line is legal and small. A one-megabyte unclosed event must not
			// circumvent the configured 128-KiB protocol reassembly resource budget.
			body := strings.Repeat(":"+strings.Repeat("x", 1022)+"\n", 1024) + "\n" + nativeRelaySSE("response.completed", "\"response\":{\"id\":\"resp_flood\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")
			svc := nativeRelayService()
			svc.cfg.Gateway.MaxLineSize = 128 << 10
			resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
			var err error
			if passthrough {
				_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
			} else {
				_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
			}
			require.Error(t, err)
			require.NotContains(t, out.Body.String(), "resp_flood")
		})
	}
}

func TestReviewNativeStreamKnownNonTextContentStopsStartupTimer(t *testing.T) {
	for _, frame := range []string{
		"{\"type\":\"response.custom_tool_call_input.delta\",\"delta\":\"print(1)\",\"item_id\":\"tool_1\"}",
		"{\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"ZmFrZQ==\",\"item_id\":\"image_1\"}",
	} {
		t.Run(gjson.Get(frame, "type").String(), func(t *testing.T) {
			ctx, _, d := nativeCommitFixture(t)
			d.ObserveFrame([]byte(frame))
			require.True(t, ControlledStreamSnapshot(ctx).SemanticSeen, "valid non-text content cannot retain startup timeout")
			require.False(t, d.semantic.IsZero())
		})
	}
}

func TestReviewNativeStreamCreatedDisarmsStartupDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(nativeRelayContext(context.Background()))
	defer cancel()
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	out := &notifyingNativeStreamRecorder{nativeStreamTestRecorder: newNativeStreamTestRecorder(), writes: make(chan []byte, 8)}
	c, _ := gin.CreateTestContext(out)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	svc := nativeRelayService()
	svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
	type result struct {
		first *int
		err   error
	}
	completed := make(chan result, 1)
	go func() {
		stream, err := svc.handleStreamingResponse(ctx, &http.Response{Header: make(http.Header), Body: reader}, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
		completed <- result{first: stream.firstTokenMs, err: err}
	}()
	_, err := io.WriteString(writer, nativeRelaySSE("response.created", "\"sequence_number\":0,\"response\":{\"id\":\"resp_idle\"}"))
	require.NoError(t, err)
	_, err = io.WriteString(writer, nativeRelaySSE("response.reasoning_text.delta", "\"sequence_number\":1,\"delta\":\"thinking\""))
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		select {
		case <-out.writes:
		case <-time.After(time.Second):
			t.Fatal("preamble event was not flushed")
		}
	}
	time.Sleep(1200 * time.Millisecond)
	select {
	case result := <-completed:
		t.Fatalf("stream terminated during valid upstream reasoning: %v", result.err)
	default:
	}
	_, err = io.WriteString(writer, nativeRelaySSE("response.output_text.delta", "\"sequence_number\":2,\"delta\":\"answer\""))
	require.NoError(t, err)
	_, err = io.WriteString(writer, nativeRelaySSE("response.completed", "\"sequence_number\":3,\"response\":{\"id\":\"resp_idle\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	select {
	case result := <-completed:
		require.NoError(t, result.err)
		require.NotNil(t, result.first)
		require.GreaterOrEqual(t, *result.first, 1000)
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not finish after the answer")
	}
	require.NotContains(t, out.Body.String(), "content_timeout")
}

func TestReviewNativeStreamTerminalMustBeVerified(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, kind := range []string{"created_eof", "done_only", "incomplete", "cancelled", "duplicate_type"} {
			name := kind + "_ordinary"
			if passthrough {
				name = kind + "_passthrough"
			}
			t.Run(name, func(t *testing.T) {
				ctx := nativeRelayContext(context.Background())
				out := newNativeStreamTestRecorder()
				c, _ := gin.CreateTestContext(out)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				body := nativeRelaySSE("response.created", "\"response\":{\"id\":\"resp_terminal\"}")
				switch kind {
				case "done_only":
					body += "data: [DONE]\n\n"
				case "incomplete", "cancelled":
					body += nativeRelaySSE("response."+kind, "\"response\":{\"id\":\"resp_terminal\",\"status\":\""+kind+"\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")
				case "duplicate_type":
					body = "data: {\"type\":\"response.completed\",\"type\":\"response.failed\",\"response\":{\"id\":\"resp_ambiguous\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
				}
				svc := nativeRelayService()
				resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				} else {
					_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				}
				require.Error(t, err, "unverified/non-success terminal must not return success")
				require.Equal(t, kind == "incomplete" || kind == "cancelled", IsNativeResponsesTerminalError(err), "only delivered, verified terminals can suppress generic handler errors")
				if kind == "duplicate_type" {
					require.NotContains(t, out.Body.String(), "resp_ambiguous")
				}
			})
		}
	}
}

func TestReviewNativeStreamErrorKeepsFollowingTerminalUsage(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "ordinary"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			ctx := nativeRelayContext(context.Background())
			out := newNativeStreamTestRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			body := nativeRelaySSE("response.created", "\"sequence_number\":0,\"response\":{\"id\":\"resp_failed_usage\"}") + nativeRelaySSE("error", "\"sequence_number\":1,\"code\":\"server_error\",\"message\":\"fixture failure\"") + nativeRelaySSE("response.failed", "\"sequence_number\":2,\"response\":{\"id\":\"resp_failed_usage\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"fixture failure\"},\"usage\":{\"input_tokens\":21,\"output_tokens\":3}}")
			svc := nativeRelayService()
			resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
			var err error
			var usage *OpenAIUsage
			if passthrough {
				var result *openaiStreamingResultPassthrough
				result, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				usage = result.usage
			} else {
				var result *openaiStreamingResult
				result, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				usage = result.usage
			}
			require.Error(t, err)
			require.Contains(t, out.Body.String(), "event: error")
			require.Contains(t, out.Body.String(), "event: response.failed")
			require.EqualValues(t, 21, usage.InputTokens)
			require.EqualValues(t, 3, usage.OutputTokens)
		})
	}
}

func TestReviewNativeStreamObserverDoesNotInventEOFTerminal(t *testing.T) {
	_, _, d := nativeCommitFixture(t)
	d.once.Do(func() {}) // Inspect parser terminal proof independently of datastore settlement.
	d.parser.onFrame = d.ObserveFrame
	b := &controlledResponseBody{ReadCloser: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n")), dispatch: d, sse: true, success: true}
	_, err := io.Copy(io.Discard, b)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.False(t, d.terminal, "EOF must not fabricate an SSE event delimiter for health credit")
}

func TestReviewNativeStreamDoneDoesNotProveResponsesSuccess(t *testing.T) {
	for _, protocol := range []string{"responses", "ws", "chat"} {
		t.Run(protocol, func(t *testing.T) {
			_, r, d := nativeCommitFixture(t)
			r.Protocol = protocol
			d.ObserveFrame([]byte("[DONE]"))
			require.Equal(t, protocol == "chat", d.terminal)
			require.Equal(t, protocol != "chat", d.firstEvent.IsZero())
		})
	}
}

func TestReviewNativeStreamMissingEventTypeDoesNotStopStartupDeadline(t *testing.T) {
	for _, frame := range []string{`{}`, `{"response":{"id":"resp_no_type"}}`, `{"type":null}`, `{"type":7}`} {
		t.Run(frame, func(t *testing.T) {
			_, _, d := nativeCommitFixture(t)
			d.ObserveFrame([]byte(frame))
			require.True(t, d.firstEvent.IsZero())
			require.True(t, d.semantic.IsZero())
			select {
			case <-d.semanticReady:
				t.Fatal("invalid event stopped startup deadline")
			default:
			}
		})
	}
}

type reviewNativeReadError struct{ err error }

func (r reviewNativeReadError) Read([]byte) (int, error) { return 0, r.err }

func TestReviewNativeStreamPostcommitReadFailureEndsSameResponse(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, failure := range []string{"eof", "done", "malformed", "too_long", "read_error"} {
			t.Run(fmt.Sprintf("passthrough_%v/%s", passthrough, failure), func(t *testing.T) {
				ctx := nativeRelayContext(context.Background())
				out := newNativeStreamTestRecorder()
				c, _ := gin.CreateTestContext(out)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				body := nativeRelaySSE("response.created", `"sequence_number":4,"response":{"id":"resp_read_error","created_at":123,"model":"fixture"}`)
				switch failure {
				case "done":
					body += "data: [DONE]\n\n"
				case "malformed":
					body += "data: {bad-json}\n\n"
				case "too_long":
					body += "data: " + strings.Repeat("x", 128*1024) + "\n\n"
				}
				var reader io.Reader = strings.NewReader(body)
				if failure == "read_error" {
					reader = io.MultiReader(reader, reviewNativeReadError{err: errors.New("fixture upstream failure")})
				}
				resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(reader)}
				svc := nativeRelayService()
				svc.cfg.Gateway.MaxLineSize = 64 * 1024
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				} else {
					_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				}
				require.Error(t, err)
				var retry *UpstreamFailoverError
				require.NotErrorAs(t, err, &retry)
				require.Equal(t, 1, strings.Count(out.Body.String(), "event: response.created\n"))
				require.Equal(t, 1, strings.Count(out.Body.String(), "event: response.failed\n"))
				require.Contains(t, out.Body.String(), `"sequence_number":5`)
				require.Contains(t, out.Body.String(), `"id":"resp_read_error"`)
				require.NotContains(t, out.Body.String(), "[DONE]")
				require.NotContains(t, out.Body.String(), "event: response.completed\n")
			})
		}
	}
}

type nativeStreamTestRecorder struct{ *httptest.ResponseRecorder }

func (*nativeStreamTestRecorder) NativeStreamMemoryWriter() bool { return true }
func newNativeStreamTestRecorder() *nativeStreamTestRecorder {
	return &nativeStreamTestRecorder{httptest.NewRecorder()}
}

type reviewOpaqueWriter struct {
	header http.Header
	writes int
}

func (w *reviewOpaqueWriter) Header() http.Header         { return w.header }
func (w *reviewOpaqueWriter) WriteHeader(int)             {}
func (w *reviewOpaqueWriter) Write(p []byte) (int, error) { w.writes++; return len(p), nil }
func (w *reviewOpaqueWriter) Flush()                      {}
func TestReviewNativeStreamOpaqueWriterFailsBeforeWrite(t *testing.T) {
	writer := &reviewOpaqueWriter{header: make(http.Header)}
	n, err := WriteNativeStreamFrame(nativeRelayContext(context.Background()), writer, []byte("event"))
	require.Error(t, err)
	require.Zero(t, n)
	require.Zero(t, writer.writes)
}

func TestReviewNativeStreamRetainedExecutionClosesOnce(t *testing.T) {
	ctx := NewControlledRequestContext(context.Background(), "responses")
	r := controlledRequest(ctx)
	closed := 0
	r.finish = func() { closed++ }
	first, err := RetainControlledExecution(ctx)
	require.NoError(t, err)
	second, err := RetainControlledExecution(ctx)
	require.NoError(t, err)
	r.Close()
	r.Close()
	require.Zero(t, closed)
	first()
	first()
	require.Zero(t, closed)
	second()
	second()
	require.Equal(t, 1, closed)
	r.Close()
	require.Equal(t, 1, closed)
	_, err = RetainControlledExecution(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestReviewNativeStreamBackgroundAcceptanceKeepsOriginalDispatch(t *testing.T) {
	ctx, r, d := nativeCommitFixture(t)
	d.ctx = WithNativeBackgroundExecution(d.ctx)
	body := []byte("{\"id\":\"resp_background\",\"background\":true,\"status\":\"queued\",\"output\":[]}")
	b := &controlledResponseBody{ReadCloser: io.NopCloser(strings.NewReader(string(body))), dispatch: d, success: true}
	resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: b}
	_, err := io.ReadAll(b)
	require.NoError(t, err)
	require.True(t, d.transportTerminal)
	require.NoError(t, validateControlledNonstreamResponse(resp, body, "responses"))
	require.True(t, b.backgroundAccepted)
	require.False(t, d.transportTerminal)
	require.False(t, b.nonstreamValidated)
	release, err := RetainControlledExecution(ctx)
	require.NoError(t, err)
	closed := 0
	r.finish = func() { closed++ }
	var adapterErr error
	finishControlledNonstreamResponse(resp, &adapterErr)
	require.NoError(t, b.Close())
	r.Close()
	require.NoError(t, d.ctx.Err())
	require.Zero(t, closed)
	require.Equal(t, 1, r.Ledger.Snapshot().Attempts)
	require.Equal(t, "resp_background", d.backgroundResponseID)
	release()
	require.Equal(t, 1, closed)
}

func TestReviewNativeStreamBackgroundAcceptanceIsClosed(t *testing.T) {
	for _, body := range []string{
		"{\"id\":\"resp_background\",\"background\":false,\"status\":\"queued\"}",
		"{\"id\":\"resp_background\",\"background\":true,\"status\":\"completed\"}",
		"{\"id\":\"resp_background\",\"background\":true,\"status\":\"queued\",\"status\":\"completed\"}",
		"{\"id\":\"\",\"background\":true,\"status\":\"queued\"}",
	} {
		_, _, d := nativeCommitFixture(t)
		d.ctx = WithNativeBackgroundExecution(d.ctx)
		b := &controlledResponseBody{dispatch: d, success: true}
		require.False(t, acceptControlledNativeBackground(b, []byte(body), "responses"))
	}
	_, _, d := nativeCommitFixture(t)
	b := &controlledResponseBody{dispatch: d, success: true}
	require.False(t, acceptControlledNativeBackground(b, []byte("{\"id\":\"resp_background\",\"background\":true,\"status\":\"queued\"}"), "responses"))
}

func TestReviewNativeStreamBareErrorDrainIsBounded(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "passthrough"}[passthrough], func(t *testing.T) {
			ctx, cancel := context.WithCancel(nativeRelayContext(context.Background()))
			defer cancel()
			reader, writer := io.Pipe()
			defer func() { _ = writer.Close() }()
			out := newNativeStreamTestRecorder()
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			svc := nativeRelayService()
			completed := make(chan error, 1)
			go func() {
				resp := &http.Response{Header: make(http.Header), Body: reader}
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				} else {
					_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				}
				completed <- err
			}()
			_, err := io.WriteString(writer, nativeRelaySSE("response.created", "\"response\":{\"id\":\"resp_bare_error\"}")+nativeRelaySSE("error", "\"code\":\"server_error\",\"message\":\"fixture\""))
			require.NoError(t, err)
			select {
			case err := <-completed:
				require.Error(t, err)
				require.Contains(t, out.Body.String(), "event: error")
				require.NotContains(t, out.Body.String(), "event: response.failed")
			case <-time.After(nativeStreamFailureDrainTimeout + time.Second):
				cancel()
				<-completed
				t.Fatal("bare error collection exceeded bounded grace")
			}
		})
	}
}

func TestReviewNativeStreamForwardRetainsTerminalUsage(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		mode := "ordinary"
		if passthrough {
			mode = "passthrough"
		}
		for _, status := range []string{"failed", "incomplete", "cancelled", "bare_then_failed"} {
			t.Run(mode+"/"+status, func(t *testing.T) {
				ctx := nativeRelayContext(context.Background())
				out := newNativeStreamTestRecorder()
				c, _ := gin.CreateTestContext(out)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
				frames := nativeRelaySSE("response.created", "\"sequence_number\":0,\"response\":{\"id\":\"resp_forward_usage\"}")
				terminalStatus := status
				if status == "bare_then_failed" {
					terminalStatus = "failed"
					frames += nativeRelaySSE("error", "\"sequence_number\":1,\"code\":\"server_error\",\"message\":\"fixture\"")
				}
				frames += nativeRelaySSE("response."+terminalStatus, "\"sequence_number\":2,\"response\":{\"id\":\"resp_forward_usage\",\"status\":\""+terminalStatus+"\",\"usage\":{\"input_tokens\":4,\"output_tokens\":1},\"output\":[]}")
				transport := &httpUpstreamSequenceRecorder{responses: []*http.Response{{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(frames))}}}
				svc := nativeRelayService()
				svc.httpUpstream = transport
				account := nativeRelayAccount()
				account.Credentials = map[string]any{"api_key": "fixture-only"}
				account.Extra = map[string]any{"openai_passthrough": passthrough, "openai_responses_supported": true}
				result, err := svc.Forward(ctx, c, account, []byte("{\"model\":\"gpt-5.1\",\"stream\":true,\"input\":\"fixture\"}"))
				require.Error(t, err)
				require.True(t, IsNativeResponsesTerminalError(err))
				require.NotNil(t, result)
				require.Equal(t, 4, result.Usage.InputTokens)
				require.Equal(t, 1, result.Usage.OutputTokens)
				require.Equal(t, "resp_forward_usage", result.ResponseID)
				require.Equal(t, "response."+terminalStatus, result.UpstreamTerminalEvent)
				require.Equal(t, 1, transport.callCount)
				require.False(t, result.OpenAIWSMode)
			})
		}
	}
}
