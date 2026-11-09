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

type geminiDispatchProbe struct {
	geminiCompatHTTPUpstreamStub
	before func(*http.Request)
}

func (s *geminiDispatchProbe) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	s.before(req)
	return s.geminiCompatHTTPUpstreamStub.Do(req, proxy, id, concurrency)
}

func forwardGeminiTestEntry(s *GeminiMessagesCompatService, entry string, ctx context.Context, c *gin.Context, a *Account) (*ForwardResult, error) {
	body := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":32}`)
	switch entry {
	case "messages":
		return s.Forward(ctx, c, a, body)
	case "chat":
		return s.ForwardAsChatCompletions(ctx, c, a, body)
	default:
		return s.ForwardNative(ctx, c, a, "gemini-2.5-flash", "streamGenerateContent", true, []byte(geminiTransportTestNativeBody))
	}
}

func TestGeminiSoleCandidatePreOutputHeartbeat(t *testing.T) {
	for _, entry := range []string{"messages", "chat", "native"} {
		for _, ending := range []string{"content", "cancel"} {
			t.Run(entry+"/"+ending, func(t *testing.T) {
				r, _, repo, _ := newFallbackFixture(t)
				SetResilienceInstance(r)
				t.Cleanup(func() { SetResilienceInstance(nil) })
				withResilienceSettings(t, func(s *resilience.Settings) {
					s.Keepalive.DelaySeconds = 1
					s.Keepalive.IntervalSeconds = 1
				})
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(geminiAttemptTestContext(context.Background(), 100*time.Millisecond))
					defer cancel()
					st := resilience.StateFrom(ctx)
					st.NoteTurn(20, "gemini-2.5-flash", "")
					st.SetGroup(20)
					st.SetEdge("cloudflare")
					a := geminiPoolModeAPIKeyAccount()
					delete(a.Credentials, "pool_mode")
					repo.byGroup[20] = []Account{*a}
					w := &protocolHeartbeatWriter{gwWriter: newGWWriter()}
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
					reader, writer := io.Pipe()
					defer func() { _ = reader.Close(); _ = writer.Close() }()
					stub := &geminiDispatchProbe{before: func(req *http.Request) {
						require.False(t, geminiRetryHasTimer(req.Context()), "sole candidate must remain untimed")
						time.Sleep(2 * time.Second) // Wait for headers beyond the budget.
						require.NotEmpty(t, w.heartbeatTimes(), "a heartbeat must be sent before upstream headers return")
					}}
					stub.response = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}
					svc := &GeminiMessagesCompatService{httpUpstream: stub, tokenProvider: &GeminiTokenProvider{}, cfg: &config.Config{}}
					done := make(chan error, 1)
					go func() {
						_, err := forwardGeminiTestEntry(svc, entry, ctx, c, a)
						done <- err
					}()
					_, err := io.WriteString(writer, "data: {\"usageMetadata\":{\"promptTokenCount\":1}}\n\n")
					require.NoError(t, err)
					time.Sleep(5 * time.Second)
					synctest.Wait()
					require.GreaterOrEqual(t, len(w.heartbeatTimes()), 4, "heartbeats must cover headers and first-content wait")
					require.LessOrEqual(t, OpenAICompactKeepaliveAdjustedWrittenSize(c), 0, "heartbeat and staged prelude are not output")
					select {
					case err := <-done:
						t.Fatalf("untimed request ended before content/cancellation: %v", err)
					default:
					}
					if ending == "cancel" {
						cancel()
					} else {
						_, err := io.WriteString(writer, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}]}\n\n")
						require.NoError(t, err)
						require.NoError(t, writer.Close())
					}
					err = <-done
					if ending == "content" {
						require.NoError(t, err)
					} else {
						require.Error(t, err)
					}
					// The handler owns final cleanup; cancellation and semantic output
					// must already have stopped the producer before cleanup runs.
					n := len(w.heartbeatTimes())
					time.Sleep(3 * time.Second)
					synctest.Wait()
					require.Len(t, w.heartbeatTimes(), n)
					if k := keepaliveFrom(c); k != nil {
						k.Stop()
					}
				})
			})
		}
	}
}

type delayedGeminiTokenCache struct{ geminiTokenCacheStub }

func (s *delayedGeminiTokenCache) GetAccessToken(context.Context, string) (string, error) {
	time.Sleep(3 * time.Second)
	return "test-token", nil
}

func TestGeminiAttemptStartExcludesRequestPreparation(t *testing.T) {
	for _, entry := range []string{"messages", "chat", "native"} {
		t.Run(entry, func(t *testing.T) {
			withResilienceSettings(t, nil)
			synctest.Test(t, func(t *testing.T) {
				ctx := geminiAttemptTestContext(context.Background(), 0)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
				a := &Account{ID: 101, Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-token", "project_id": "test-project"}}
				stub := &geminiDispatchProbe{before: func(req *http.Request) {
					p := resilience.StateFrom(ctx).AttemptProfile()
					require.WithinDuration(t, time.Now(), p.AttemptStart, time.Millisecond, "sample after request builder")
					g, _ := req.Context().Value(geminiAttemptKey{}).(*geminiAttempt)
					require.NotNil(t, g)
					require.GreaterOrEqual(t, p.AttemptStart.Sub(g.plan.Start()), 3*time.Second, "shared timer keeps its original owner")
				}}
				stub.response = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}]}}\n\n"))}
				svc := &GeminiMessagesCompatService{httpUpstream: stub, tokenProvider: &GeminiTokenProvider{tokenCache: &delayedGeminiTokenCache{}}, cfg: &config.Config{}}
				_, err := forwardGeminiTestEntry(svc, entry, ctx, c, a)
				require.NoError(t, err)
			})
		})
	}
}

func TestGeminiCommittedHeartbeatErrorUsesNativeEnvelope(t *testing.T) {
	c, w := newKeepaliveTestContext(t)
	stop := StartPreOutputKeepalive(c, "gemini", 0, time.Hour)
	waitCommitted(t, c)
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": 503, "status": "UNAVAILABLE", "message": "busy"}})
	stop()
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `data: {"error":{"code":503,"message":"busy","status":"UNAVAILABLE"}}`)
	require.NotContains(t, w.Body.String(), "response.failed")
}

func TestGeminiSoleCandidateHeartbeatEligibility(t *testing.T) {
	for _, exemption := range []string{"no_edge", "oauth", "go_sdk", "python_sdk", "disabled", "multiple_candidates"} {
		t.Run(exemption, func(t *testing.T) {
			r, _, repo, _ := newFallbackFixture(t)
			SetResilienceInstance(r)
			t.Cleanup(func() { SetResilienceInstance(nil) })
			withResilienceSettings(t, func(s *resilience.Settings) {
				s.Keepalive.DelaySeconds = 1
				s.Keepalive.IntervalSeconds = 1
				s.Keepalive.Enabled = exemption != "disabled"
			})
			synctest.Test(t, func(t *testing.T) {
				ctx := geminiAttemptTestContext(context.Background(), 0)
				st := resilience.StateFrom(ctx)
				st.NoteTurn(20, "gemini-2.5-flash", "")
				st.SetGroup(20)
				if exemption != "no_edge" {
					st.SetEdge("cloudflare")
				}
				a := geminiPoolModeAPIKeyAccount()
				if exemption == "oauth" {
					a.Type = AccountTypeOAuth
					a.Credentials = map[string]any{"access_token": "test-token", "project_id": "test-project"}
				}
				repo.byGroup[20] = []Account{*a}
				if exemption == "multiple_candidates" {
					other := healthyAccount(a.ID + 1)
					other.Platform = PlatformGemini
					repo.byGroup[20] = append(repo.byGroup[20], other)
				}
				w := &protocolHeartbeatWriter{gwWriter: newGWWriter()}
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
				if exemption == "go_sdk" || exemption == "python_sdk" {
					language := strings.TrimSuffix(exemption, "_sdk")
					c.Request.Header.Set("X-Goog-Api-Client", "google-genai-sdk/1.71.0 gl-"+language+"/1.0")
				}
				stub := &geminiDispatchProbe{before: func(*http.Request) { time.Sleep(3 * time.Second) }}
				stub.response = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}]}\n\n"))}
				svc := &GeminiMessagesCompatService{httpUpstream: stub, tokenProvider: &GeminiTokenProvider{}, cfg: &config.Config{}}
				_, err := forwardGeminiTestEntry(svc, "native", ctx, c, a)
				require.NoError(t, err)
				require.Empty(t, w.heartbeatTimes())
			})
		})
	}
}
