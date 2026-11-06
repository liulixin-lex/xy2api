//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type continuityWriter struct {
	*gwWriter
	mu      sync.Mutex
	latency time.Duration
	times   []time.Time
}

func (w *continuityWriter) Write(p []byte) (int, error) {
	if w.latency > 0 {
		time.Sleep(w.latency)
	}
	n, err := w.gwWriter.Write(p)
	if string(p) == string(preOutputPingFrame) && err == nil {
		w.mu.Lock()
		w.times = append(w.times, time.Now())
		w.mu.Unlock()
	}
	return n, err
}

func (w *continuityWriter) pingTimes() []time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Time(nil), w.times...)
}

func TestOpenAIHeartbeatContinuity(t *testing.T) {
	for _, path := range []string{"native", "passthrough"} {
		for _, scenario := range []string{"handoff", "slow_write", "failover_after_ping", "failover_before_ping", "headers_before_first_ping"} {
			t.Run(path+"/"+scenario, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					withResilienceSettings(t, nil)
					w := &continuityWriter{gwWriter: newGWWriter()}
					if scenario == "slow_write" {
						w.latency = time.Millisecond
					}
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					c.Request.Header.Set("originator", "codex_cli_rs")
					start := time.Now()
					stop := StartPreOutputKeepalive(c, PreOutputProtocolResponses, 5*time.Second, 5*time.Second)
					defer stop()
					up := newGWUpstream(context.Background())
					defer func() { _ = up.Close() }()
					resp := up.response(context.Background())
					resp.Header.Set("Content-Type", "text/event-stream")
					resp.Header.Set(openAICodexTurnStateHeader, "turn-state")
					var done <-chan error
					startStream := func() {
						if path == "passthrough" {
							result := ptRun(compatStreamService(nil, 100, 0, 5), c, resp)
							ch := make(chan error, 1)
							go func() { ch <- (<-result).err }()
							done = ch
						} else {
							ch := make(chan error, 1)
							go func() {
								_, err := nativeTurnStateService(5, 0).handleStreamingResponse(c.Request.Context(), resp, c, rawChatCompletionsTestAccount(), time.Now(), "gpt-5.4", "gpt-5.4")
								ch <- err
							}()
							done = ch
						}
						up.send(t, ptCreated)
						synctest.Wait()
					}
					advance := func(at time.Duration) {
						time.Sleep(at - time.Since(start))
						synctest.Wait()
					}
					headersAt := 8500 * time.Millisecond
					switch scenario {
					case "slow_write":
						headersAt = 5500 * time.Millisecond
					case "failover_after_ping", "failover_before_ping":
						headersAt = 6 * time.Second
					case "headers_before_first_ping":
						headersAt = time.Second
					}
					advance(headersAt)
					startStream()
					if scenario == "failover_after_ping" || scenario == "failover_before_ping" {
						failAt := 12 * time.Second
						if scenario == "failover_before_ping" {
							failAt = 8 * time.Second
						}
						advance(failAt)
						up.send(t, ptOverload)
						var failover *UpstreamFailoverError
						require.ErrorAs(t, <-done, &failover)
						StartPreOutputKeepalive(c, PreOutputProtocolResponses, 5*time.Second-time.Since(start), 5*time.Second)
					}
					advance(30*time.Second + 10*time.Millisecond)
					times := w.pingTimes()
					require.GreaterOrEqual(t, len(times), 5, "ping times: %v", times)
					if scenario == "headers_before_first_ping" {
						require.Equal(t, start.Add(6*time.Second), times[0])
						require.Equal(t, "turn-state", nativeTurnStateWireHeaders(w.gwWriter).Get(openAICodexTurnStateHeader))
					}
					for i := 1; i < len(times); i++ {
						require.LessOrEqual(t, times[i].Sub(times[i-1]), 5*time.Second+w.latency, "ping times: %v", times)
					}
					if scenario == "failover_after_ping" || scenario == "failover_before_ping" {
						require.Equal(t, start.Add(15*time.Second), times[2], "failover must preserve the previous beat")
					} else {
						up.send(t, ptOverload)
						var failover *UpstreamFailoverError
						require.ErrorAs(t, <-done, &failover)
					}
					body := w.Body()
					requireResponsesPingEvents(t, body)
					require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
					require.NotContains(t, body, "response.created")
				})
			})
		}
	}
}
