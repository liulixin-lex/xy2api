//go:build unit

package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type nativeAttemptTimeoutReadCloser struct {
	io.ReadCloser
	dispatch *controlledDispatch
}

func (b *nativeAttemptTimeoutReadCloser) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.dispatch.parser.Feed(p[:n], nil)
	}
	return n, b.dispatch.responseReadError(err)
}

func TestNativeStreamAttemptDeadlineDeliversFailureOrSafeFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		for _, created := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough_%v_created_%v", passthrough, created), func(t *testing.T) {
				ctx, r, d := nativeCommitFixture(t)
				r.policyLoaded = true
				r.Profile = scheduling.LatencyProfile{AttemptTimeoutMS: 80, TotalBudgetMS: 1000}
				r.Ledger = scheduling.NewAttemptLedger(r.Policy.Retry, r.Profile, time.Now(), time.Time{})
				require.NoError(t, r.Ledger.BeginAttempt(1, 0, time.Now(), true))
				d.parser.onFrame = d.ObserveFrame
				pr, pw := io.Pipe()
				reader := &nativeAttemptTimeoutReadCloser{ReadCloser: pr, dispatch: d}
				defer reader.Close()
				d.mu.Lock()
				d.startFirstOutputTimerLocked()
				d.mu.Unlock()
				writerDone := make(chan struct{})
				go func() {
					defer close(writerDone)
					if created {
						_, _ = io.WriteString(pw, nativeRelaySSE("response.created", `"sequence_number":7,"response":{"id":"resp_timeout","created_at":12345,"model":"fixture"}`))
					}
					<-d.ctx.Done()
					_ = pw.CloseWithError(context.Canceled)
				}()
				rec := newNativeStreamTestRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				c.Writer = &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}
				svc := nativeRelayService()
				var err error
				var first *int
				if passthrough {
					result, e := svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
					err, first = e, result.firstTokenMs
				} else {
					result, e := svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
					err, first = e, result.firstTokenMs
				}
				<-writerDone
				require.Error(t, err)
				require.NoError(t, ctx.Err(), "the downstream request remains live after attempt cancellation")
				require.Nil(t, first, "created and a local failure are not semantic output")
				if created {
					require.ErrorIs(t, err, context.DeadlineExceeded)
					require.True(t, ControlledStreamFailureWritten(ctx))
					require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed\n"))
					chunks := strings.Split(rec.Body.String(), "\n\n")
					data := nativeSSEEventData([]byte(chunks[1]))
					require.Equal(t, "resp_timeout", gjson.GetBytes(data, "response.id").String())
					require.EqualValues(t, 12345, gjson.GetBytes(data, "response.created_at").Int())
					require.Equal(t, "fixture", gjson.GetBytes(data, "response.model").String())
					require.EqualValues(t, 8, gjson.GetBytes(data, "sequence_number").Int())
					require.Equal(t, "content_timeout", gjson.GetBytes(data, "response.error.code").String())
					before := rec.Body.String()
					require.NoError(t, writeNativeResponsesFailure(ctx, c, "resp_timeout", 9, "upstream_error", "duplicate"))
					require.Equal(t, before, rec.Body.String(), "failure delivery must be exactly once")
				} else {
					var failover *UpstreamFailoverError
					require.ErrorAs(t, err, &failover)
					require.Equal(t, http.StatusGatewayTimeout, failover.StatusCode)
					require.True(t, failover.SafeToFailoverAfterWrite)
					require.Empty(t, rec.Body.String())
					require.False(t, ControlledStreamSnapshot(ctx).AttemptCommitted)
					require.False(t, ControlledStreamFailureWritten(ctx))
				}
				t.Logf("OBSERVED created=%v passthrough=%v parent_live=true bytes=%d failure_delivered=%v", created, passthrough, rec.Body.Len(), ControlledStreamFailureWritten(ctx))
			})
		}
	}
}

func TestNativeRawChatAttemptCancellationNeverSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, preamble := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v_preamble_%v", failure, preamble), func(t *testing.T) {
				ctx := nativeRelayContext(context.Background())
				rec := newNativeStreamTestRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
				payload := ""
				if preamble {
					payload = "data: {\"id\":\"chatcmpl_timeout\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"
				}
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &openAIChatStreamReadErrorCloser{payload: []byte(payload), err: failure}}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				result, err := svc.streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture", nil, nil, time.Now(), 1)
				require.Error(t, err)
				require.NoError(t, ctx.Err())
				if result != nil {
					require.Nil(t, result.FirstTokenMs)
				}
				require.NotContains(t, rec.Body.String(), "[DONE]")
			})
		}
	}
}

func TestNativeRawChatReadErrorAfterTerminalMetadataNeverSucceeds(t *testing.T) {
	for _, payload := range []string{
		`{"choices":[],"usage":{}}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	} {
		for _, failure := range []error{context.Canceled, context.DeadlineExceeded, io.ErrUnexpectedEOF} {
			t.Run(payload+"/"+failure.Error(), func(t *testing.T) {
				rec := newNativeStreamTestRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(nativeRelayContext(context.Background()))
				resp := &http.Response{Header: make(http.Header), Body: &openAIChatStreamReadErrorCloser{payload: []byte("data: " + payload + "\n\n"), err: failure}}
				result, err := (&OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}).streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture", nil, nil, time.Now(), 1)
				require.Error(t, err)
				require.ErrorIs(t, err, failure)
				require.Nil(t, result.FirstTokenMs)
				require.False(t, IsNativeChatTerminalError(err))
			})
		}
	}
}

func TestNativeRawChatUsageAloneCannotCompleteResponse(t *testing.T) {
	for _, preamble := range []bool{false, true} {
		t.Run(fmt.Sprint(preamble), func(t *testing.T) {
			rec := newNativeStreamTestRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(nativeRelayContext(context.Background()))
			body := ""
			if preamble {
				body = "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
			}
			body += "data: {\"choices\":[],\"usage\":{}}\n\n"
			resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
			result, err := (&OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}).streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture", nil, nil, time.Now(), 1)
			require.ErrorIs(t, err, ErrOpenAIUpstreamStreamTruncated)
			require.False(t, IsNativeChatTerminalError(err))
			if !preamble {
				require.Nil(t, result.FirstTokenMs)
			}
		})
	}
}

func TestNativeRawChatValidEmptyTerminalRemainsValid(t *testing.T) {
	for _, body := range []string{
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n",
		"data: [DONE]\n\n",
	} {
		t.Run(body, func(t *testing.T) {
			rec := newNativeStreamTestRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(nativeRelayContext(context.Background()))
			resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
			result, err := (&OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}).streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture", nil, nil, time.Now(), 1)
			require.NoError(t, err)
			require.Nil(t, result.FirstTokenMs)
			require.Equal(t, body, rec.Body.String(), "do not append an error after a valid provider terminal")
		})
	}
}

func TestNativeRawChatDeliveredDoneDoesNotWaitForReadError(t *testing.T) {
	rec := newNativeStreamTestRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(nativeRelayContext(context.Background()))
	body := "data: [DONE]\n\n"
	resp := &http.Response{Header: make(http.Header), Body: &openAIChatStreamReadErrorCloser{payload: []byte(body), err: context.DeadlineExceeded}}
	_, err := (&OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}).streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture", nil, nil, time.Now(), 1)
	require.NoError(t, err)
	require.Equal(t, body, rec.Body.String())
}

func TestNativeChatParentCancellationDoesNotEmitLocalFailure(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(fmt.Sprint(raw), func(t *testing.T) {
			parent, cancel := context.WithCancel(nativeRelayContext(context.Background()))
			rec := newNativeStreamTestRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(parent)
			cancel()
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: &openAIChatStreamReadErrorCloser{err: context.Canceled}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			var err error
			if raw {
				_, err = svc.streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture", nil, nil, time.Now(), 1)
			} else {
				_, err = svc.handleChatStreamingResponse(resp, c, nativeRelayAccount(), "fixture", "fixture", "fixture", time.Now(), 1)
			}
			require.True(t, err == nil || errors.Is(err, context.Canceled))
			require.Empty(t, rec.Body.String())
			require.False(t, ControlledStreamFailureWritten(parent))
		})
	}
}

func TestNativeResponsesLateEventDeadlineKeepsRequestLive(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, committed := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough_%v_committed_%v", passthrough, committed), func(t *testing.T) {
				ctx, r, d := nativeCommitFixture(t)
				r.policyLoaded = true
				rec := newNativeStreamTestRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				c.Writer = &schedulingResponseWriter{ResponseWriter: c.Writer, request: r}
				if committed {
					frame := nativeRelaySSE("response.created", `"sequence_number":7,"response":{"id":"resp_delivered","created_at":12345}`)
					payload := nativeSSEEventData([]byte(frame))
					require.NoError(t, CommitControlledOutput(ctx, payload))
					_, err := WriteNativeStreamFrame(ctx, c.Writer, []byte(frame))
					require.NoError(t, err)
					recordNativeResponsesDeliveredIdentity(c, payload)
				}
				d.mu.Lock()
				d.timeout = true
				d.cancellationReason = ControlledContentTimeout
				d.cancel()
				d.mu.Unlock()
				late := nativeRelaySSE("response.created", `"sequence_number":99,"response":{"id":"resp_late"}`)
				resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(late))}
				var err error
				if passthrough {
					_, err = nativeRelayService().handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				} else {
					_, err = nativeRelayService().handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				}
				require.Error(t, err)
				require.NoError(t, ctx.Err())
				require.Equal(t, ControlledContentTimeout, ControlledStreamSnapshot(ctx).CancelReason)
				require.NotContains(t, rec.Body.String(), "resp_late")
				if committed {
					require.ErrorIs(t, err, context.DeadlineExceeded)
					require.NoError(t, writeNativeResponsesFailure(ctx, c, "resp_late", 100, "content_timeout", "timeout"))
					require.Contains(t, rec.Body.String(), `"sequence_number":8`)
					require.Contains(t, rec.Body.String(), `"id":"resp_delivered"`)
				} else {
					var failover *UpstreamFailoverError
					require.ErrorAs(t, err, &failover)
					require.Equal(t, http.StatusGatewayTimeout, failover.StatusCode)
					require.Empty(t, rec.Body.String())
				}
			})
		}
	}
}

func TestNativeRawChatUpstreamErrorMarksOnlyDeliveredTerminal(t *testing.T) {
	for _, flushFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(flushFailure), func(t *testing.T) {
			ctx := nativeRelayContext(context.Background())
			var out http.ResponseWriter = newNativeStreamTestRecorder()
			if flushFailure {
				out = &reviewFlushErrorWriter{header: make(http.Header)}
			}
			c, _ := gin.CreateTestContext(out)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
			body := `data: {"error":{"code":"upstream_error","message":"provider failed"}}` + "\n\n"
			resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
			_, err := (&OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}).streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture", nil, nil, time.Now(), 1)
			require.Error(t, err)
			require.Equal(t, !flushFailure, IsNativeChatTerminalError(err))
			if flushFailure {
				require.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}

type nativeConvertedChatFailureWriter struct {
	*httptest.ResponseRecorder
	failWrite       bool
	failFlush       bool
	errorFrame      bool
	errorWrites     int
	errorFlushes    int
	deadlineUpdates int
}

func (w *nativeConvertedChatFailureWriter) Write(p []byte) (int, error) {
	w.errorFrame = strings.Contains(string(p), `"error":`)
	if w.errorFrame {
		w.errorWrites++
		if w.failWrite {
			return 0, io.ErrClosedPipe
		}
	}
	return w.ResponseRecorder.Write(p)
}

func (w *nativeConvertedChatFailureWriter) FlushError() error {
	if w.errorFrame {
		w.errorFlushes++
		if w.failFlush {
			return io.ErrClosedPipe
		}
	}
	w.ResponseRecorder.Flush()
	return nil
}

func (w *nativeConvertedChatFailureWriter) SetWriteDeadline(time.Time) error {
	w.deadlineUpdates++
	return nil
}

func TestNativeConvertedChatUpstreamErrorMarksOnlyDeliveredTerminal(t *testing.T) {
	for _, preamble := range []bool{false, true} {
		for _, failure := range []string{"none", "write", "flush"} {
			t.Run(fmt.Sprintf("preamble_%v_%s", preamble, failure), func(t *testing.T) {
				writer := &nativeConvertedChatFailureWriter{
					ResponseRecorder: httptest.NewRecorder(),
					failWrite:        failure == "write",
					failFlush:        failure == "flush",
				}
				c, _ := gin.CreateTestContext(writer)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(nativeRelayContext(context.Background()))
				body := ""
				if preamble {
					body = nativeRelaySSE("response.output_text.delta", `"delta":"partial"`)
				}
				body += nativeRelaySSE("response.failed", `"response":{"id":"resp_failed","status":"failed","error":{"code":"invalid_request_error","message":"provider rejected request"}}`)
				resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
				result, err := nativeRelayService().handleChatStreamingResponse(resp, c, nativeRelayAccount(), "fixture", "fixture", "fixture", time.Now(), 1)
				require.Error(t, err)
				require.Equal(t, failure == "none", IsNativeChatTerminalError(err))
				require.Equal(t, 1, writer.errorWrites)
				require.GreaterOrEqual(t, writer.deadlineUpdates, 2, "the error frame must use the bounded writer")
				require.NotContains(t, writer.Body.String(), "[DONE]")
				if failure != "none" {
					require.ErrorIs(t, err, context.Canceled)
					require.NotContains(t, err.Error(), "upstream response failed:")
				} else {
					require.Equal(t, 1, strings.Count(writer.Body.String(), `"error":`))
				}
				if failure == "write" {
					require.Zero(t, writer.errorFlushes)
				} else {
					require.Equal(t, 1, writer.errorFlushes)
				}
				if !preamble {
					require.Nil(t, result.FirstTokenMs, "failure is not semantic output")
				}
			})
		}
	}
}

func TestNativeStreamHeartbeatPreservesSchedulerDeadline(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	scanner := newNativeSSEIdleScanner(context.Background(), pr, bufio.NewScanner(pr), time.Millisecond, func() error {
		return context.DeadlineExceeded
	})
	defer scanner.Close()
	require.False(t, scanner.Scan())
	require.ErrorIs(t, scanner.Err(), context.DeadlineExceeded)
	require.NotErrorIs(t, scanner.Err(), context.Canceled)
}
