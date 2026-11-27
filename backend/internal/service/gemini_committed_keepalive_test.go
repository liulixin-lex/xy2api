//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// The first read blocks in fake time, so assertions run before JSON collection
// or SSE scanning can finish and before any semantic output reaches the writer.
type geminiKeepaliveReadProbe struct {
	io.Reader
	before func()
}

func (r *geminiKeepaliveReadProbe) Read(p []byte) (int, error) {
	if r.before != nil {
		before := r.before
		r.before = nil
		before()
	}
	return r.Reader.Read(p)
}

func requireGeminiKeepaliveBeats(t *testing.T, c *gin.Context, w *protocolHeartbeatWriter, count int) {
	t.Helper()
	for range count {
		before := w.heartbeatTimes()
		flushes := len(w.flushed)
		time.Sleep(10 * time.Second)
		synctest.Wait()
		after := w.heartbeatTimes()
		require.Len(t, after, len(before)+1, "each interval must emit a heartbeat")
		require.Equal(t, flushes+1, len(w.flushed), "every heartbeat must flush")
		require.Equal(t, 10*time.Second, after[len(after)-1].Sub(before[len(before)-1]))
		require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c), "heartbeats are not billable output")
	}
}

func TestGeminiCommittedKeepaliveBridgeBodyWait(t *testing.T) {
	for _, entry := range []string{"chat", "messages"} {
		for _, mode := range []string{"multi_sse", "multi_json", "sole_json", "uncommitted_json"} {
			t.Run(entry+"/"+mode, func(t *testing.T) {
				r, _, repo, _ := newFallbackFixture(t)
				SetResilienceInstance(r)
				t.Cleanup(func() { SetResilienceInstance(nil) })
				withResilienceSettings(t, func(s *resilience.Settings) {
					s.Keepalive.DelaySeconds, s.Keepalive.IntervalSeconds = 1, 10
				})
				synctest.Test(t, func(t *testing.T) {
					ctx := geminiAttemptTestContext(context.Background(), 0)
					st := resilience.StateFrom(ctx)
					st.SetGroup(20)
					st.SetEdge("cloudflare")
					a := geminiPoolModeAPIKeyAccount()
					a.Status, a.Schedulable = StatusActive, true
					delete(a.Credentials, "pool_mode")
					repo.byGroup[20] = []Account{*a}
					sole := mode == "sole_json" || mode == "uncommitted_json"
					if !sole {
						other := *a
						other.ID++
						repo.byGroup[20] = append(repo.byGroup[20], other)
					}
					w := &protocolHeartbeatWriter{gwWriter: newGWWriter()}
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
					if !sole {
						protocol := PreOutputProtocolChat
						if entry == "messages" {
							protocol = PreOutputProtocolAnthropic
						}
						defer StartPreOutputKeepalive(c, protocol, time.Second, 10*time.Second)()
					}
					defer StopGeminiUniqueCandidateKeepalive(c)
					var guard *geminiAttempt
					var owner *openAICompactSSEKeepalive
					stub := &geminiDispatchProbe{before: func(req *http.Request) {
						guard, _ = req.Context().Value(geminiAttemptKey{}).(*geminiAttempt)
						require.Equal(t, sole, guard.keepalive, "do not broaden prelude eligibility")
						require.Equal(t, !sole, guard.hasTimer())
						owner = keepaliveFrom(c)
						if mode != "uncommitted_json" {
							time.Sleep(2 * time.Second)
							synctest.Wait()
							require.True(t, PreOutputKeepaliveCommitted(c))
						}
					}}
					body := geminiFallbackObject
					contentType := "application/json"
					if mode == "multi_sse" {
						body = "data: " + strings.ReplaceAll(geminiFallbackObject, "\n", "") + "\n\n"
						contentType = "text/event-stream"
					}
					probe := &geminiKeepaliveReadProbe{Reader: strings.NewReader(body), before: func() {
						require.Same(t, owner, keepaliveFrom(c))
						if mode == "uncommitted_json" {
							time.Sleep(31 * time.Second)
							synctest.Wait()
							require.False(t, PreOutputKeepaliveCommitted(c), "JSON collection must not newly commit SSE")
							require.Empty(t, w.heartbeatTimes())
							return
						}
						timer := guard.timer
						requireGeminiKeepaliveBeats(t, c, w, 3)
						require.Same(t, timer, guard.timer, "keepalive must preserve the attempt timer")
						require.NoError(t, guard.ctx.Err())
					}}
					stub.response = &http.Response{StatusCode: 200, Header: http.Header{
						"Content-Type": {contentType}, "X-Request-Id": {"synthetic-success"},
					}, Body: io.NopCloser(probe)}
					svc := &GeminiMessagesCompatService{httpUpstream: stub, tokenProvider: &GeminiTokenProvider{}, cfg: &config.Config{}}
					result, err := forwardGeminiTestEntry(svc, entry, ctx, c, a)
					require.NoError(t, err)
					rawPrompt := 7
					require.Equal(t, ClaudeUsage{InputTokens: 7, OutputTokens: 3, InputTokensPresent: true, OutputTokensPresent: true, RawPromptTokens: &rawPrompt}, result.Usage)
					require.Equal(t, 1, strings.Count(w.Body(), "hello world"))
					n := len(w.heartbeatTimes())
					resumeGeminiPreOutputKeepalive(c, stub.response)
					time.Sleep(30 * time.Second)
					synctest.Wait()
					require.Len(t, w.heartbeatTimes(), n, "semantic output permanently stops pre-output heartbeats")
				})
			})
		}
	}
}

func TestGeminiCommittedKeepaliveNativeHandoff(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		t.Run(map[bool]string{false: "api_key", true: "oauth"}[oauth], func(t *testing.T) {
			r, _, repo, _ := newFallbackFixture(t)
			SetResilienceInstance(r)
			t.Cleanup(func() { SetResilienceInstance(nil) })
			withResilienceSettings(t, func(s *resilience.Settings) {
				s.Keepalive.DelaySeconds, s.Keepalive.IntervalSeconds = 1, 10
			})
			synctest.Test(t, func(t *testing.T) {
				ctx := geminiAttemptTestContext(context.Background(), 0)
				st := resilience.StateFrom(ctx)
				st.SetGroup(20)
				st.SetEdge("cloudflare")
				a := geminiPoolModeAPIKeyAccount()
				a.Status, a.Schedulable = StatusActive, true
				delete(a.Credentials, "pool_mode")
				repo.byGroup[20] = []Account{*a}
				w := &protocolHeartbeatWriter{gwWriter: newGWWriter()}
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
				defer StopGeminiUniqueCandidateKeepalive(c)
				stub := &geminiDispatchProbe{before: func(*http.Request) { time.Sleep(2 * time.Second) }}
				stub.response = &http.Response{StatusCode: 408, Header: http.Header{"X-Request-Id": {"synthetic-failure"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"timeout"}}`))}
				svc := &GeminiMessagesCompatService{httpUpstream: stub, tokenProvider: &GeminiTokenProvider{}, cfg: &config.Config{}}
				_, err := forwardGeminiTestEntry(svc, "native", ctx, c, a)
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.True(t, PreOutputKeepaliveCommitted(c))
				owner := keepaliveFrom(c)
				requireGeminiKeepaliveBeats(t, c, w, 2)
				// A new attempt must also recover an owner suspended by header access.
				c.Header("X-Test-Handoff", "synthetic")
				other := *a
				other.ID++
				if oauth {
					other.Type = AccountTypeOAuth
					other.Credentials = map[string]any{"access_token": "test-token", "project_id": "test-project"}
				}
				spare := *a
				spare.ID += 2
				repo.byGroup[20] = append(repo.byGroup[20], other, spare)
				stub.before = func(req *http.Request) {
					g, _ := req.Context().Value(geminiAttemptKey{}).(*geminiAttempt)
					require.False(t, g.unique)
					require.False(t, g.keepalive)
					require.Same(t, owner, keepaliveFrom(c))
					requireGeminiKeepaliveBeats(t, c, w, 2)
				}
				payload := strings.ReplaceAll(geminiFallbackObject, "\n", "")
				if oauth {
					payload = `{"response":` + payload + "}"
				}
				stub.response = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + payload + "\n\n"))}
				_, err = forwardGeminiTestEntry(svc, "native", ctx, c, &other)
				require.NoError(t, err)
			})
		})
	}
}

func TestGeminiCommittedKeepaliveTerminalCannotResume(t *testing.T) {
	for _, terminal := range []string{"semantic", "error", "cancel", "cleanup"} {
		t.Run(terminal, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Keepalive.IntervalSeconds = 10 })
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				w := &protocolHeartbeatWriter{gwWriter: newGWWriter()}
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
				stop := StartPreOutputKeepalive(c, PreOutputProtocolGemini, 0, 10*time.Second)
				defer stop()
				synctest.Wait()
				switch terminal {
				case "semantic":
					_, err := c.Writer.WriteString("data: " + strings.ReplaceAll(geminiFallbackObject, "\n", "") + "\n\n")
					require.NoError(t, err)
				case "error":
					c.JSON(503, gin.H{"error": gin.H{"message": "busy"}})
				case "cancel":
					cancel()
				case "cleanup":
					stop()
				}
				synctest.Wait()
				n := len(w.heartbeatTimes())
				resumeGeminiPreOutputKeepalive(c, nil)
				ResumeCommittedPreOutputKeepalive(c, 10*time.Second)
				time.Sleep(30 * time.Second)
				synctest.Wait()
				require.Len(t, w.heartbeatTimes(), n)
			})
		})
	}
}
