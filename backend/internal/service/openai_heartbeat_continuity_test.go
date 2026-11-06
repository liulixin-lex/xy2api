//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type continuityWriter struct {
	*gwWriter
	mu      sync.Mutex
	latency time.Duration
	times   []time.Time
}

type protocolHeartbeatWriter struct {
	*gwWriter
	mu     sync.Mutex
	times  []time.Time
	frames []string
}

func (w *protocolHeartbeatWriter) Write(p []byte) (int, error) {
	n, err := w.gwWriter.Write(p)
	if err == nil && (string(p) == string(preOutputCommentFrame) || string(p) == string(preOutputPingFrame)) {
		w.mu.Lock()
		w.times = append(w.times, time.Now())
		w.frames = append(w.frames, string(p))
		w.mu.Unlock()
	}
	return n, err
}

func (w *protocolHeartbeatWriter) heartbeatTimes() []time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Time(nil), w.times...)
}

func runProtocolHeartbeatHandoff(t *testing.T, name string, atHeartbeatBoundary bool) {
	t.Helper()
	w := &protocolHeartbeatWriter{gwWriter: newGWWriter()}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+name, nil)
	stop := StartPreOutputKeepalive(c, map[string]string{
		"chat/completions": PreOutputProtocolChat,
		"messages":         PreOutputProtocolAnthropic,
		"responses":        PreOutputProtocolResponses,
	}[name], 5*time.Second, 5*time.Second)
	defer stop()
	start := time.Now()
	time.Sleep(5 * time.Second)
	if !atHeartbeatBoundary {
		synctest.Wait()
		time.Sleep(3500 * time.Millisecond)
		synctest.Wait()
	}

	up := newGWUpstream(nil)
	t.Cleanup(func() { _ = up.Close() })
	var done <-chan error
	switch name {
	case "chat/completions":
		ch := make(chan error, 1)
		go func() {
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamKeepaliveInterval: 5}}}
			_, err := svc.handleChatStreamingResponse(up.response(nil), c, rawChatCompletionsTestAccount(), "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now(), 0)
			ch <- err
		}()
		done = ch
		up.send(t, ptCreated)
	case "messages":
		ch := make(chan error, 1)
		go func() {
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamKeepaliveInterval: 5}}}
			_, err := svc.handleAnthropicStreamingResponse(up.response(nil), c, rawChatCompletionsTestAccount(), "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
			ch <- err
		}()
		done = ch
		up.send(t, ptCreated)
	case "responses":
		ch := gwRun(gwConvertPaths()[0], gwConvertService(5, 0), c, up.response(nil))
		resultDone := make(chan error, 1)
		go func() {
			got := <-ch
			resultDone <- got.err
		}()
		done = resultDone
		up.send(t, gwMsgStart)
	}
	synctest.Wait()
	if atHeartbeatBoundary {
		time.Sleep(5 * time.Second)
		synctest.Wait()
	} else {
		time.Sleep(1500 * time.Millisecond)
		synctest.Wait()
	}
	require.Equal(t, []time.Time{start.Add(5 * time.Second), start.Add(10 * time.Second)}, w.heartbeatTimes())
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.NotContains(t, w.Body(), "response.created")

	if name == "responses" {
		up.send(t, gwTextStart, gwTextDelta, gwBlockStop, gwMsgDelta("end_turn"), gwMsgStop)
	} else {
		up.send(t, ptCompleted)
	}
	_ = up.pw.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("protocol stream did not finish")
	}
}

func TestProtocolKeepaliveHandoffUsesRequestHeartbeatClock(t *testing.T) {
	for _, name := range []string{"chat/completions", "messages", "responses"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				withResilienceSettings(t, nil)
				runProtocolHeartbeatHandoff(t, name, false)
			})
		})
	}
}

func TestProtocolKeepaliveHandoffAtHeartbeatBoundary(t *testing.T) {
	for _, name := range []string{"chat/completions", "messages", "responses"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				withResilienceSettings(t, nil)
				runProtocolHeartbeatHandoff(t, name, true)
			})
		})
	}
}

func TestGatewayConvertHandoffReadsPhaseAfterRequestHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		withResilienceSettings(t, nil)
		w := &protocolHeartbeatWriter{gwWriter: newGWWriter()}
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		stop := StartPreOutputKeepalive(c, PreOutputProtocolChat, 5*time.Second, 5*time.Second)
		defer stop()

		up := newGWUpstream(nil)
		defer func() { _ = up.Close() }()
		svc := gwConvertService(5, 0)
		stream := svc.newGatewayConvertStream(c, up.response(nil), gwTestAccount(), gatewayConvertChat, "gpt-5", gatewayConvertHooks{
			convert:  func(_ *apicompat.AnthropicStreamEvent, _ string, _ bool) []string { return nil },
			finalize: func() []string { return nil },
			failure:  func([]byte) []string { return nil },
		})
		start := time.Now()
		time.Sleep(5 * time.Second)
		synctest.Wait()

		done := make(chan gatewayConvertOutcome, 1)
		go func() { done <- stream.run() }()
		up.send(t, gwMsgStart)
		synctest.Wait()
		require.Equal(t, []time.Time{start.Add(5 * time.Second)}, w.heartbeatTimes())

		time.Sleep(5 * time.Second)
		synctest.Wait()
		require.Equal(t, []time.Time{start.Add(5 * time.Second), start.Add(10 * time.Second)}, w.heartbeatTimes())

		_ = up.pw.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("conversion stream did not finish")
		}
	})
}

func TestHeartbeatCommentBeforeFirstBeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		withResilienceSettings(t, nil)
		w := newGWWriter()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
		stop := StartPreOutputKeepalive(c, PreOutputProtocolAnthropic, 5*time.Second, 5*time.Second)
		defer stop()
		time.Sleep(time.Second)
		_, err := writeOpenAIStreamKeepalive(c, c.Writer, []byte(": upstream\n\n"), false)
		require.NoError(t, err)
		time.Sleep(time.Second)
		StartPreOutputKeepalive(c, PreOutputProtocolAnthropic, 0, 5*time.Second)
		time.Sleep(2999 * time.Millisecond)
		synctest.Wait()
		require.Equal(t, ": upstream\n\n", w.Body())
		time.Sleep(time.Millisecond)
		synctest.Wait()
		require.Equal(t, ": upstream\n\n"+string(preOutputPingFrame), w.Body())
	})
}

func TestStagedHeartbeatResumesAfterFailover(t *testing.T) {
	for _, protocol := range []string{PreOutputProtocolAnthropic, PreOutputProtocolChat} {
		t.Run(protocol, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				withResilienceSettings(t, nil)
				w := newGWWriter()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
				stop := StartPreOutputKeepalive(c, protocol, 5*time.Second, 5*time.Second)
				defer stop()
				time.Sleep(5 * time.Second)
				synctest.Wait()
				c.Header("X-Attempt", "one")
				time.Sleep(5 * time.Second)
				if protocol == PreOutputProtocolAnthropic {
					stage := newAnthropicStreamStage(c.Request.Context(), c, func() {})
					defer stage.close()
					_, err := stage.submit("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
					require.NoError(t, err)
					require.NoError(t, stage.heartbeat(""))
					_, err = stage.submit("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n")
					err = (&GatewayService{}).anthropicStreamEventError(c.Request.Context(), c, &http.Response{Header: http.Header{}}, &Account{}, "", err)
					var failover *UpstreamFailoverError
					require.ErrorAs(t, err, &failover)
				} else {
					st := &gatewayConvertStream{c: c, guarded: true, protocol: gatewayConvertChat, resp: &http.Response{Header: http.Header{}}}
					st.heartbeat(": ping\n\n", true)
					outcome, done := st.onEvent("error", `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
					require.True(t, done)
					var failover *UpstreamFailoverError
					require.ErrorAs(t, outcome.err, &failover)
				}
				before := w.Body()
				time.Sleep(2 * time.Second)
				StartPreOutputKeepalive(c, protocol, 0, 5*time.Second)
				time.Sleep(2999 * time.Millisecond)
				synctest.Wait()
				require.Equal(t, before, w.Body())
				time.Sleep(time.Millisecond)
				synctest.Wait()
				require.Equal(t, before+string(map[string][]byte{PreOutputProtocolAnthropic: preOutputPingFrame, PreOutputProtocolChat: preOutputCommentFrame}[protocol]), w.Body())
				require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
				require.NotContains(t, w.Body(), "message_start")
				k := keepaliveFrom(c)
				k.mu.Lock()
				kBytes := k.bytes
				k.mu.Unlock()
				streamBytes, _ := c.Get(openAIStreamKeepaliveBytesKey)
				streamCount, _ := streamBytes.(int)
				require.Equal(t, len(w.Body()), kBytes+streamCount)
			})
		})
	}
}

type replayHeartbeatUpstream struct {
	*httpUpstreamRecorder
	secondStarted chan struct{}
	release       chan struct{}
}

func (u *replayHeartbeatUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	if len(u.requests) == 1 {
		close(u.secondStarted)
		<-u.release
	}
	return u.httpUpstreamRecorder.Do(req, proxy, id, concurrency)
}

func TestForwardReplayResumesCommittedHeartbeat(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "native"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				withResilienceSettings(t, func(s *resilience.Settings) { s.Keepalive.IntervalSeconds = 5 })
				resetWebSearchDeclarations(t)
				up := newGWUpstream(context.Background())
				defer func() { _ = up.Close() }()
				recorder := &httpUpstreamRecorder{responses: []*http.Response{up.response(context.Background()), webSearchSuccessStream("resp_replayed")}}
				upstream := &replayHeartbeatUpstream{httpUpstreamRecorder: recorder, secondStarted: make(chan struct{}), release: make(chan struct{})}
				defer func() {
					select {
					case <-upstream.release:
					default:
						close(upstream.release)
					}
				}()
				svc := newOpenAIImageGenerationControlTestService(recorder)
				svc.httpUpstream = upstream
				svc.cfg.Gateway.StreamKeepaliveInterval = 5
				req := newWebSearchReplayRequest(t)
				w := &continuityWriter{gwWriter: newGWWriter()}
				c, _ := gin.CreateTestContext(w)
				c.Request = req.c.Request
				stop := StartPreOutputKeepalive(c, PreOutputProtocolResponses, 5*time.Second, 5*time.Second)
				defer stop()
				start := time.Now()
				time.Sleep(6 * time.Second)
				synctest.Wait()
				done := make(chan error, 1)
				go func() {
					_, err := svc.Forward(req.ctx, c, webSearchReplayAccount(passthrough), []byte(openAIWebSearchHistoryCompactionBody))
					done <- err
				}()
				up.send(t, ptCreated)
				synctest.Wait()
				time.Sleep(6 * time.Second)
				up.send(t, webSearchRejectionEvent)
				<-upstream.secondStarted
				synctest.Wait()
				time.Sleep(8 * time.Second)
				synctest.Wait()
				require.Equal(t, []time.Time{start.Add(5 * time.Second), start.Add(10 * time.Second), start.Add(15 * time.Second), start.Add(20 * time.Second)}, w.pingTimes())
				require.NotContains(t, w.Body(), "response.created")
				close(upstream.release)
				require.NoError(t, <-done)
			})
		})
	}
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
		for _, scenario := range []string{"handoff", "slow_write", "failover_after_ping", "failover_after_comment", "failover_before_ping", "headers_before_first_ping"} {
			if scenario == "failover_after_comment" && path != "passthrough" {
				continue
			}
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
					case "failover_after_ping", "failover_after_comment", "failover_before_ping":
						headersAt = 6 * time.Second
					case "headers_before_first_ping":
						headersAt = time.Second
					}
					advance(headersAt)
					startStream()
					if scenario == "failover_after_ping" || scenario == "failover_after_comment" || scenario == "failover_before_ping" {
						failAt := 12 * time.Second
						if scenario == "failover_after_comment" {
							advance(12 * time.Second)
							up.send(t, ptComment)
							synctest.Wait()
							failAt = 13 * time.Second
						}
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
						gap := 5 * time.Second
						if scenario == "slow_write" && (path == "native" || i == 1) {
							gap += w.latency
						}
						require.Equal(t, gap, times[i].Sub(times[i-1]), "ping times: %v", times)
					}
					if scenario == "failover_after_ping" || scenario == "failover_after_comment" || scenario == "failover_before_ping" {
						require.Equal(t, start.Add(15*time.Second), times[2], "failover must preserve the previous beat")
					} else {
						up.send(t, ptOverload)
						var failover *UpstreamFailoverError
						require.ErrorAs(t, <-done, &failover)
					}
					body := w.Body()
					pingBody := body
					if scenario == "failover_after_comment" {
						require.Equal(t, 1, strings.Count(body, ptComment))
						pingBody = strings.Replace(body, ptComment, "", 1)
					}
					requireResponsesPingEvents(t, pingBody)
					k := keepaliveFrom(c)
					k.mu.Lock()
					kBytes := k.bytes
					k.mu.Unlock()
					streamBytes, _ := c.Get(openAIStreamKeepaliveBytesKey)
					streamCount, _ := streamBytes.(int)
					require.Equal(t, len(body), kBytes+streamCount)
					require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))
					require.NotContains(t, body, "response.created")
				})
			})
		}
	}
}
