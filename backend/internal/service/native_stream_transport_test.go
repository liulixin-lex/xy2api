package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type nativeRelayHTTPClient struct {
	HTTPUpstream
	client *http.Client
}

func (c *nativeRelayHTTPClient) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return c.client.Do(r)
}

type nativeRelaySignalWriter struct {
	*httptest.ResponseRecorder
	first chan struct{}
	once  sync.Once
}

func (w *nativeRelaySignalWriter) Flush() {
	w.ResponseRecorder.Flush()
	w.once.Do(func() { close(w.first) })
}

// Exercise the real net/http transport (including its post-close drain), not an
// io.Pipe double. Every path joins the reader and observes the upstream close.
func TestNativeStreamHTTPRealTransportCloseRaces(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		for _, mode := range []string{"client_cancel", "admin_cancel", "user_stop", "write_failure", "terminal"} {
			t.Run(fmt.Sprintf("passthrough_%v_%s", passthrough, mode), func(t *testing.T) {
				for iteration := 0; iteration < 20; iteration++ {
					func() {
						upstreamEnded := make(chan struct{})
						upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							defer close(upstreamEnded)
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, nativeRelaySSE("response.created", "\"sequence_number\":0,\"response\":{\"id\":\"resp_close\"}"))
							if err := http.NewResponseController(w).Flush(); err != nil {
								return
							}
							if mode == "terminal" {
								_, _ = io.WriteString(w, nativeRelaySSE("response.completed", "\"sequence_number\":1,\"response\":{\"id\":\"resp_close\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}"))
								_ = http.NewResponseController(w).Flush()
								return
							}
							<-r.Context().Done()
						}))
						defer upstream.Close()
						parent, cancel := context.WithCancel(context.Background())
						defer cancel()
						ctx := NewControlledRequestContext(nativeRelayContext(parent), "responses")
						requestCtx, release := detachUpstreamContext(ctx)
						release() // Preparation release must not detach or pre-cancel the request.
						require.NoError(t, requestCtx.Err())
						req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, upstream.URL, nil)
						require.NoError(t, err)
						svc := nativeRelayService()
						svc.httpUpstream = &nativeRelayHTTPClient{client: upstream.Client()}
						resp, err := svc.sendOpenAIUpstream(req, "", nativeRelayAccount())
						require.NoError(t, err)
						defer func() { _ = resp.Body.Close() }()
						signal := &nativeRelaySignalWriter{ResponseRecorder: httptest.NewRecorder(), first: make(chan struct{})}
						var w http.ResponseWriter = signal
						if mode == "write_failure" {
							w = &nativeRelayPartialWriter{header: make(http.Header)}
						}
						c, _ := gin.CreateTestContext(w)
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
						done := make(chan error, 1)
						go func() {
							var err error
							if passthrough {
								_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
							} else {
								_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
							}
							done <- err
						}()
						if mode != "write_failure" {
							select {
							case <-signal.first:
							case <-time.After(time.Second):
								t.Fatal("created was not flushed")
							}
						}
						switch mode {
						case "client_cancel":
							_ = CancelControlledRequest(ctx, ControlledClientDetached)
							cancel()
						case "admin_cancel":
							_ = CancelControlledRequest(ctx, ControlledAdminCancel)
							cancel()
						case "user_stop":
							_ = CancelControlledRequest(ctx, ControlledUserStop)
							cancel()
						}
						select {
						case err := <-done:
							if mode == "terminal" {
								require.NoError(t, err)
							} else {
								require.ErrorIs(t, err, context.Canceled)
							}
						case <-time.After(time.Second):
							t.Fatal("real HTTP relay failed to join its canceled reader")
						}
						select {
						case <-upstreamEnded:
						case <-time.After(time.Second):
							t.Fatal("upstream execution remained live after relay exit")
						}
					}()
				}
				t.Logf("real_http_teardown mode=%s passthrough=%v iterations=20 all_readers_joined=true", mode, passthrough)
			})
		}
	}
}

func (*nativeRelaySignalWriter) NativeStreamMemoryWriter() bool { return true }
