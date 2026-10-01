package handler

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// The production Ops wrapper must remain in the chain: testing only Gin and
// scheduling misses a deadline capability lost before the first network write.
func TestOpsNativeStreamRealNetwork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, h2 := range []bool{false, true} {
		for _, mode := range []scheduling.Mode{scheduling.ModeControlled, scheduling.ModeSub2API} {
			for _, endpoint := range []string{"responses", "chat/completions"} {
				t.Run(fmt.Sprintf("http2=%v/%s/%s", h2, mode, endpoint), func(t *testing.T) {
					first := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"fixture text\"}\n\n"
					last := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"completed\"}}\n\n"
					if endpoint == "chat/completions" {
						first = "data: {\"id\":\"chatcmpl_fixture\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture text\"}}]}\n\n"
						last = "data: [DONE]\n\n"
					}
					finish := make(chan struct{})
					defer func() {
						select {
						case <-finish:
						default:
							close(finish)
						}
					}()
					outcome := make(chan error, 1)
					status := make(chan int, 1)
					router := gin.New()
					router.Use(func(c *gin.Context) { c.Next(); status <- c.Writer.Status() })
					router.Use(OpsErrorLoggerMiddleware(nil))
					router.Use(func(c *gin.Context) {
						c.Request = c.Request.WithContext(service.WithNativeStreamPolicy(c.Request.Context(), service.NativeStreamPolicy{Version: 1, Delivery: true}))
						c.Next()
					})
					router.Use(service.ControlledSchedulingMiddleware(func(context.Context) (scheduling.ModeSnapshot, error) {
						return scheduling.ModeSnapshot{Mode: mode}, nil
					}))
					router.POST("/v1/"+endpoint, func(c *gin.Context) {
						c.Header("Content-Type", "text/event-stream")
						_, err := service.WriteNativeStreamFrame(c.Request.Context(), c.Writer, []byte(first))
						if err == nil {
							select {
							case <-finish:
							case <-c.Request.Context().Done():
							}
							_, err = service.WriteNativeStreamFrame(c.Request.Context(), c.Writer, []byte(last))
						}
						outcome <- err
					})
					server := httptest.NewUnstartedServer(router)
					server.EnableHTTP2 = h2
					server.StartTLS()
					defer server.Close()
					client := server.Client()
					client.Timeout = 5 * time.Second
					response, err := client.Post(server.URL+"/v1/"+endpoint, "application/json", strings.NewReader(`{"stream":true}`))
					require.NoError(t, err)
					defer func() { _ = response.Body.Close() }()
					reader := bufio.NewReader(response.Body)
					line, readErr := reader.ReadString('\n')
					close(finish)
					body, bodyErr := io.ReadAll(reader)
					writeErr := <-outcome
					outerStatus := <-status
					t.Logf("status=%d protocol=%s bytes=%d outer_status=%d write_error=%v", response.StatusCode, response.Proto, len(line)+len(body), outerStatus, writeErr)
					require.NoError(t, writeErr)
					require.NoError(t, readErr)
					require.NoError(t, bodyErr)
					require.Equal(t, first+last, line+string(body))
					require.Equal(t, http.StatusOK, outerStatus)
					require.Equal(t, map[bool]int{false: 1, true: 2}[h2], response.ProtoMajor)
				})
			}
		}
	}
}

type opsNativeControlSink struct {
	*httptest.ResponseRecorder
	deadlines int
	flushes   int
	block     string
	entered   chan struct{}
	resume    chan struct{}
}

func (w *opsNativeControlSink) SetWriteDeadline(time.Time) error {
	w.deadlines++
	if w.block == "deadline" {
		close(w.entered)
		<-w.resume
	}
	return nil
}

func (w *opsNativeControlSink) FlushError() error {
	w.flushes++
	if w.block == "flush" {
		close(w.entered)
		<-w.resume
	}
	return io.ErrClosedPipe
}

func TestOpsNativeStreamFlushErrorAndStaleLease(t *testing.T) {
	pool := &deterministicOpsCaptureWriterStatePool{}
	first := &opsNativeControlSink{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(first)
	stale := acquireOpsCaptureWriterFromPool(pool, c.Writer)
	n, err := service.WriteNativeStreamFrame(context.Background(), stale, []byte("data: fixture\n\n"))
	require.Equal(t, len("data: fixture\n\n"), n)
	require.ErrorIs(t, err, context.Canceled)
	require.Contains(t, err.Error(), io.ErrClosedPipe.Error())
	require.Equal(t, 1, first.flushes, "the underlying FlushError must be observed through Gin")
	releaseOpsCaptureWriter(stale)
	second := &opsNativeControlSink{ResponseRecorder: httptest.NewRecorder()}
	c2, _ := gin.CreateTestContext(second)
	current := acquireOpsCaptureWriterFromPool(pool, c2.Writer)
	defer releaseOpsCaptureWriter(current)
	require.Same(t, stale.state, current.state)
	require.ErrorIs(t, http.NewResponseController(stale).SetWriteDeadline(time.Now()), net.ErrClosed)
	require.ErrorIs(t, http.NewResponseController(stale).Flush(), net.ErrClosed)
	require.Zero(t, second.deadlines)
	require.Zero(t, second.flushes)
	require.NoError(t, http.NewResponseController(current).SetWriteDeadline(time.Now()))
	require.Equal(t, 1, second.deadlines)
}

func TestOpsNativeStreamControlRetainsLeaseWithoutMutex(t *testing.T) {
	for _, operation := range []string{"deadline", "flush"} {
		t.Run(operation, func(t *testing.T) {
			sink := &opsNativeControlSink{ResponseRecorder: httptest.NewRecorder(), block: operation, entered: make(chan struct{}), resume: make(chan struct{})}
			c, _ := gin.CreateTestContext(sink)
			writer := acquireOpsCaptureWriter(c.Writer)
			done := make(chan error, 1)
			go func() {
				if operation == "deadline" {
					done <- http.NewResponseController(writer).SetWriteDeadline(time.Now())
				} else {
					done <- http.NewResponseController(writer).Flush()
				}
			}()
			<-sink.entered
			locked := writer.state.mu.TryLock()
			if locked {
				writer.state.mu.Unlock()
			}
			if !locked {
				close(sink.resume)
				t.Fatal("network control holds the state mutex")
			}
			released := make(chan struct{})
			go func() { releaseOpsCaptureWriter(writer); close(released) }()
			select {
			case <-released:
				close(sink.resume)
				t.Fatal("lease released while network control was in flight")
			case <-time.After(20 * time.Millisecond):
			}
			close(sink.resume)
			err := <-done
			if operation == "deadline" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, io.ErrClosedPipe)
			}
			select {
			case <-released:
			case <-time.After(time.Second):
				t.Fatal("lease did not release")
			}
		})
	}
}
