package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// End-to-end local gateway relay benchmark: complete upstream event read through
// actual HTTP ResponseWriter.Flush, measured separately from client socket drain.
func TestNativeStreamRelayReadToFlushPerformance(t *testing.T) {
	if os.Getenv("XY2_NATIVE_RELAY_PERF") != "1" {
		t.Skip("dedicated gate: XY2_NATIVE_RELAY_PERF=1 GOMAXPROCS=2, isolated from race and parallel package builds")
	}
	gin.SetMode(gin.TestMode)
	data := nativeRelaySSE("response.output_text.delta", "\"sequence_number\":0,\"delta\":\""+strings.Repeat("x", 63*1024)+"\"")
	require.LessOrEqual(t, len(data), 64*1024)
	terminal := nativeRelaySSE("response.completed", "\"sequence_number\":1,\"response\":{\"id\":\"resp_perf\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, data)
		require.NoError(t, http.NewResponseController(w).Flush())
		_, _ = io.WriteString(w, terminal)
		require.NoError(t, http.NewResponseController(w).Flush())
	}))
	defer upstream.Close()
	for _, passthrough := range []bool{false, true} {
		for _, concurrency := range []int{1, 16, 64} {
			t.Run(fmt.Sprintf("passthrough_%v_concurrency_%d", passthrough, concurrency), func(t *testing.T) {
				var mu sync.Mutex
				var samples []float64
				var clientSamples []float64
				gatewayErrors := make(chan error, concurrency*10)
				gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx := context.WithValue(nativeRelayContext(r.Context()), nativeStreamFlushObserverKey{}, func(readAt, flushedAt time.Time) {
						mu.Lock()
						samples = append(samples, float64(flushedAt.Sub(readAt))/float64(time.Millisecond))
						mu.Unlock()
					})
					request, _ := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
					resp, err := http.DefaultClient.Do(request)
					if err != nil {
						gatewayErrors <- err
						return
					}
					defer func() { _ = resp.Body.Close() }()
					c, _ := gin.CreateTestContext(w)
					c.Request = r.WithContext(ctx)
					svc := nativeRelayService()
					if passthrough {
						_, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
					} else {
						_, err = svc.handleStreamingResponse(ctx, resp, c, nativeRelayAccount(), time.Now(), "fixture", "fixture")
					}
					if err != nil {
						gatewayErrors <- err
					}
				}))
				defer gateway.Close()
				client := &http.Client{Timeout: 15 * time.Second}
				var workers sync.WaitGroup
				for i := 0; i < concurrency; i++ {
					workers.Add(1)
					go func() {
						defer workers.Done()
						for iteration := 0; iteration < 8; iteration++ {
							start := time.Now()
							resp, err := client.Get(gateway.URL)
							if err == nil {
								_, err = io.Copy(io.Discard, resp.Body)
								_ = resp.Body.Close()
							}
							if err != nil {
								gatewayErrors <- err
								return
							}
							mu.Lock()
							clientSamples = append(clientSamples, float64(time.Since(start))/float64(time.Millisecond))
							mu.Unlock()
						}
					}()
				}
				workers.Wait()
				close(gatewayErrors)
				for err := range gatewayErrors {
					require.NoError(t, err)
				}
				mu.Lock()
				defer mu.Unlock()
				require.Len(t, clientSamples, concurrency*8)
				require.Len(t, samples, concurrency*8*2)
				sort.Float64s(samples)
				sort.Float64s(clientSamples)
				percentile := func(values []float64, p float64) float64 {
					index := int(float64(len(values)-1) * p)
					return values[index]
				}
				p95, p99 := percentile(samples, .95), percentile(samples, .99)
				t.Logf("events=%d payload_bytes=%d concurrent=%d read_to_flush_p95_ms=%.3f read_to_flush_p99_ms=%.3f client_complete_p95_ms=%.3f", len(samples), len(data), concurrency, p95, p99, percentile(clientSamples, .95))
				require.LessOrEqual(t, p95, 10.0)
				require.LessOrEqual(t, p99, 30.0)
			})
		}
	}
}
