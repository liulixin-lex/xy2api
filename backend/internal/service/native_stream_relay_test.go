package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func nativeRelayContext(parent context.Context) context.Context {
	return WithNativeStreamPolicy(parent, NativeStreamPolicy{Version: 1, Delivery: true})
}
func nativeRelayAccount() *Account {
	return &Account{ID: 881, Name: "local-fixture", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
}
func nativeRelayService() *OpenAIGatewayService {
	return &OpenAIGatewayService{cfg: &config.Config{}, toolCorrector: NewCodexToolCorrector()}
}
func nativeRelaySSE(event, extra string) string {
	return "event: " + event + "\ndata: {\"type\":\"" + event + "\"," + extra + "}\n\n"
}
func nativeRelayReadEvent(reader *bufio.Reader) (string, error) {
	var frame strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return frame.String(), err
		}
		_, _ = frame.WriteString(line)
		if line == "\n" {
			return frame.String(), nil
		}
	}
}

// Real local HTTP sockets observe arrival separately from upstream production.
// The optional 0.2 / 5 / 20 second playback uses exactly the release-gate timing.
func TestNativeStreamRelayImmediateCompleteEvents(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough_%v", passthrough), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			legacy := os.Getenv("XY2_RELAY_EXPECT_LEGACY") == "1"
			times := []time.Duration{20 * time.Millisecond, 100 * time.Millisecond, 180 * time.Millisecond}
			if os.Getenv("XY2_RELAY_ACCEPTANCE_TIMING") == "1" {
				times = []time.Duration{200 * time.Millisecond, 5 * time.Second, 20 * time.Second}
			}
			frames := []string{
				nativeRelaySSE("response.created", "\"sequence_number\":0,\"response\":{\"id\":\"resp_same\"}"),
				nativeRelaySSE("response.vendor_progress", "\"sequence_number\":1,\"opaque\":\"kept\""),
				nativeRelaySSE("response.output_text.delta", "\"sequence_number\":2,\"delta\":\"hello\""),
				nativeRelaySSE("response.function_call_arguments.delta", "\"sequence_number\":3,\"item_id\":\"tool_same\",\"delta\":\"{\""),
				nativeRelaySSE("response.completed", "\"sequence_number\":4,\"response\":{\"id\":\"resp_same\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}"),
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				begun := time.Now()
				schedule := []time.Duration{times[0], times[1], times[1], times[1], times[2]}
				for i, frame := range frames {
					delay := schedule[i] - time.Since(begun)
					if delay < 0 {
						delay = 0
					}
					timer := time.NewTimer(delay)
					select {
					case <-timer.C:
					case <-r.Context().Done():
						timer.Stop()
						return
					}
					_, _ = io.WriteString(w, frame)
					require.NoError(t, http.NewResponseController(w).Flush())
				}
			}))
			defer upstream.Close()
			result := make(chan error, 1)
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := nativeRelayContext(r.Context())
				if legacy {
					ctx = r.Context()
				}
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					result <- err
					return
				}
				defer func() { _ = response.Body.Close() }()
				c, _ := gin.CreateTestContext(w)
				c.Request = r.WithContext(ctx)
				svc := nativeRelayService()
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(ctx, response, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				} else {
					_, err = svc.handleStreamingResponse(ctx, response, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				}
				result <- err
			}))
			defer gateway.Close()
			started := time.Now()
			client := &http.Client{Timeout: times[2] + 2*time.Second}
			response, err := client.Get(gateway.URL)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			reader := bufio.NewReader(response.Body)
			got := make([]string, 0, len(frames))
			arrivals := make([]time.Duration, 0, len(frames))
			heartbeats := 0
			for len(got) < len(frames) {
				frame, err := nativeRelayReadEvent(reader)
				require.NoError(t, err)
				if strings.HasPrefix(strings.TrimSpace(frame), ":") && len(nativeSSEEventData([]byte(frame))) == 0 {
					heartbeats++ // Local liveness is not a protocol/content event.
					continue
				}
				got = append(got, frame)
				arrivals = append(arrivals, time.Since(started))
			}
			require.NoError(t, <-result)
			for i, frame := range frames {
				require.Contains(t, got[i], strings.Split(frame, "\n")[0])
				require.Contains(t, got[i], fmt.Sprintf("\"sequence_number\":%d", i))
			}
			require.Contains(t, got[3], "tool_same")
			beforeContent := arrivals[0] < times[1]-20*time.Millisecond
			t.Logf("created_before_content=%v created_arrival_ms=%d content_arrival_ms=%d terminal_arrival_ms=%d local_heartbeats=%d original_response=resp_same", beforeContent, arrivals[0].Milliseconds(), arrivals[2].Milliseconds(), arrivals[4].Milliseconds(), heartbeats)
			require.Equal(t, !legacy, beforeContent)
		})
	}
}

func TestNativeStreamRelayErrorBoundary(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, created := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough_%v_created_%v", passthrough, created), func(t *testing.T) {
				ctx := nativeRelayContext(context.Background())
				c, _ := gin.CreateTestContext(newNativeStreamTestRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				body := nativeRelaySSE("response.failed", "\"response\":{\"id\":\"resp_failed\",\"error\":{\"code\":\"server_error\",\"message\":\"overloaded\"}}")
				if created {
					body = nativeRelaySSE("response.created", "\"response\":{\"id\":\"resp_failed\"}") + body
				}
				resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
				svc := nativeRelayService()
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				} else {
					_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				}
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.Equal(t, !created, errors.As(err, &failover))
				require.Equal(t, created, IsResponseCommitted(c))
			})
		}
	}
}

type nativeRelayPartialWriter struct {
	header  http.Header
	written int
}

func (w *nativeRelayPartialWriter) Header() http.Header { return w.header }
func (w *nativeRelayPartialWriter) WriteHeader(int)     {}
func (w *nativeRelayPartialWriter) Write(p []byte) (int, error) {
	w.written++
	return min(3, len(p)), io.ErrClosedPipe
}
func (w *nativeRelayPartialWriter) Flush() {}
func TestNativeStreamRelayPartialWriteCommits(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			ctx := nativeRelayContext(context.Background())
			w := &nativeRelayPartialWriter{header: make(http.Header)}
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(nativeRelaySSE("response.created", "\"response\":{\"id\":\"resp_partial\"}")))}
			svc := nativeRelayService()
			var err error
			if passthrough {
				_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
			} else {
				_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
			}
			require.Error(t, err)
			require.ErrorIs(t, err, context.Canceled)
			require.True(t, IsResponseCommitted(c))
			require.Equal(t, 1, w.written)
		})
	}
}

type nativeRelayCloseBody struct {
	reader *io.PipeReader
	closed chan struct{}
	once   sync.Once
}

func (b *nativeRelayCloseBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *nativeRelayCloseBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return b.reader.Close()
}
func TestNativeStreamRelayCancellationClosesBody(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			ctx := nativeRelayContext(parent)
			reader, writer := io.Pipe()
			defer func() { _ = writer.Close() }()
			body := &nativeRelayCloseBody{reader: reader, closed: make(chan struct{})}
			c, _ := gin.CreateTestContext(newNativeStreamTestRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			done := make(chan error, 1)
			go func() {
				svc := nativeRelayService()
				resp := &http.Response{Header: make(http.Header), Body: body}
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				} else {
					_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				}
				done <- err
			}()
			cancel()
			select {
			case <-body.closed:
			case <-time.After(time.Second):
				t.Fatal("upstream body remained live after cancellation")
			}
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("relay did not terminate")
			}
		})
	}
}
func TestNativeStreamUpstreamContextPreservesCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, release := detachStreamUpstreamContext(nativeRelayContext(parent), true)
	defer release()
	cancel()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestNativeStreamRelayMultilineAndInvalidEvents(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, valid := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough_%v_valid_%v", passthrough, valid), func(t *testing.T) {
				ctx := nativeRelayContext(context.Background())
				rec := newNativeStreamTestRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				frame := "event: response.created\ndata: {\"type\":\"response.created\",\ndata: \"sequence_number\":0,\"response\":{\"id\":\"resp_multiline\"}}\n\n"
				if !valid {
					frame = "event: response.created\ndata: {\"type\":broken}\n\n"
				} else {
					frame += nativeRelaySSE("response.completed", "\"sequence_number\":1,\"response\":{\"id\":\"resp_multiline\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")
				}
				resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(frame))}
				svc := nativeRelayService()
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				} else {
					_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				}
				if valid {
					require.NoError(t, err)
					require.Contains(t, rec.Body.String(), "resp_multiline")
					require.True(t, IsResponseCommitted(c))
				} else {
					require.Error(t, err)
					require.Empty(t, rec.Body.String())
					require.False(t, IsResponseCommitted(c))
				}
			})
		}
	}
}

func TestNativeStreamRelayLocalErrorContinuesSequence(t *testing.T) {
	ctx := nativeRelayContext(context.Background())
	w := newNativeStreamTestRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	input := nativeRelaySSE("response.created", "\"sequence_number\":7,\"response\":{\"id\":\"resp_partial\"}") + "data: {invalid}\n\n"
	resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(strings.NewReader(input))}
	_, err := nativeRelayService().handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
	require.Error(t, err)
	require.Contains(t, w.Body.String(), "\"sequence_number\":7")
	require.Contains(t, w.Body.String(), "\"type\":\"response.failed\",\"sequence_number\":8")
	require.Equal(t, 1, strings.Count(w.Body.String(), "event: response.failed\n"))
	require.Contains(t, w.Body.String(), `"id":"resp_partial"`)
	require.NotContains(t, w.Body.String(), "{invalid}")
}

func (*nativeRelayPartialWriter) NativeStreamMemoryWriter() bool { return true }

func TestNativeStreamRelayReusedBufferPreservesFrames(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough_%v", passthrough), func(t *testing.T) {
			out := newNativeStreamTestRecorder()
			c, _ := gin.CreateTestContext(out)
			var flushed []string
			var identities []nativeResponsesDeliveredIdentity
			ctx := context.WithValue(nativeRelayContext(context.Background()), nativeStreamFlushObserverKey{}, func(_, _ time.Time) {
				require.True(t, out.Flushed, "the snapshot must follow a completed Flush")
				flushed = append(flushed, out.Body.String())
				identity, ok := nativeResponsesDeliveredIdentityFromContext(c)
				require.True(t, ok, "successful delivery must retain the response identity")
				identities = append(identities, identity)
			})
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			texts := []string{strings.Repeat("a", 40*1024), strings.Repeat("b", 40*1024)}
			frames := []string{
				nativeRelaySSE("response.created", `"sequence_number":0,"response":{"id":"resp_reused_buffer","created_at":42,"model":"fixture"}`),
				nativeRelaySSE("response.output_text.delta", `"sequence_number":1,"delta":"`+texts[0]+`"`),
				nativeRelaySSE("response.output_text.delta", `"sequence_number":2,"delta":"`+texts[1]+`"`),
				nativeRelaySSE("response.completed", `"sequence_number":3,"response":{"id":"resp_reused_buffer","status":"completed","usage":{"input_tokens":1,"output_tokens":2}}`),
			}
			require.Greater(t, len(frames[1]), 32*1024)
			require.Greater(t, len(frames[2]), 32*1024)
			resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Join(frames, "")))}
			svc := nativeRelayService()
			if passthrough {
				result, err := svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, "resp_reused_buffer", result.responseID)
			} else {
				result, err := svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, "resp_reused_buffer", result.responseID)
			}
			require.Len(t, flushed, len(frames))
			require.Len(t, identities, len(frames))
			for i := 0; i < len(frames)-1; i++ {
				// Recheck earlier snapshots after every later frame has reused the
				// buffer; neither the first large delta nor its identity may change.
				require.Equal(t, strings.Join(frames[:i+1], ""), flushed[i])
			}
			require.Equal(t, out.Body.String(), flushed[len(flushed)-1])
			reader := bufio.NewReader(strings.NewReader(out.Body.String()))
			for i := range frames {
				frame, err := nativeRelayReadEvent(reader)
				require.NoError(t, err)
				payload := nativeSSEEventData([]byte(frame))
				require.True(t, gjson.ValidBytes(payload))
				require.Equal(t, int64(i), gjson.GetBytes(payload, "sequence_number").Int())
				require.Equal(t, "resp_reused_buffer", identities[i].ID)
				require.Equal(t, int64(i), identities[i].Sequence)
				require.Equal(t, int64(42), identities[i].CreatedAt)
				require.Equal(t, "fixture", identities[i].Model)
				if i < len(frames)-1 {
					require.Equal(t, frames[i], frame)
				} else {
					require.Equal(t, "response.completed", gjson.GetBytes(payload, "type").String())
					require.Equal(t, "resp_reused_buffer", gjson.GetBytes(payload, "response.id").String())
					require.Equal(t, "completed", gjson.GetBytes(payload, "response.status").String())
					if passthrough {
						require.Equal(t, frames[i], frame)
					} else {
						require.Equal(t, texts[0]+texts[1], gjson.GetBytes(payload, "response.output.0.content.0.text").String())
					}
				}
			}
			_, err := nativeRelayReadEvent(reader)
			require.ErrorIs(t, err, io.EOF)
			id, nextSequence, createdAt := NativeResponsesFailureIdentity(c)
			require.Equal(t, "resp_reused_buffer", id)
			require.Equal(t, int64(4), nextSequence)
			require.Equal(t, int64(42), createdAt)
		})
	}
}
