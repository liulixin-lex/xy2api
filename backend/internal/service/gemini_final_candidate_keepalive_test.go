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

func TestGeminiLastCandidateHeartbeatBoundary(t *testing.T) {
	for _, entry := range []string{"native", "messages", "chat"} {
		for _, shape := range []string{"budget_last", "last_resort_budget_last", "last_resort_excluded", "mixed_available", "last_resort_mixed_available", "last_resort_soft_peer"} {
			t.Run(entry+"/"+shape, func(t *testing.T) {
				r, _, repo, _ := newFallbackFixture(t)
				SetResilienceInstance(r)
				t.Cleanup(func() { SetResilienceInstance(nil) })
				withResilienceSettings(t, func(s *resilience.Settings) {
					s.Keepalive.DelaySeconds, s.Keepalive.IntervalSeconds = 1, 1
				})
				synctest.Test(t, func(t *testing.T) {
					ctx := geminiAttemptTestContext(context.Background(), 0)
					st := resilience.StateFrom(ctx)
					st.SetGroup(20)
					st.NoteTurn(20, "gemini-2.5-flash", "")
					st.SetEdge("cloudflare")
					a := geminiPoolModeAPIKeyAccount()
					a.Status, a.Schedulable = StatusActive, true
					delete(a.Credentials, "pool_mode")
					peer := *a
					peer.ID = a.ID + 1
					peer.Status, peer.Schedulable = StatusActive, true
					peer.Platform = PlatformGemini
					peer.Credentials = map[string]any{"model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"}}
					if strings.Contains(shape, "mixed") {
						peer.Platform = PlatformAntigravity
						peer.Extra = map[string]any{"mixed_scheduling": true}
					}
					if shape == "last_resort_soft_peer" {
						until := time.Now().Add(time.Minute)
						peer.TempUnschedulableUntil = &until
						peer.TempUnschedulableReason = "transient:TRANSIENT HTTP 502"
					}
					require.True(t, peer.IsModelSupported("gemini-2.5-flash"))
					repo.byGroup[20] = []Account{*a, peer}
					if strings.HasPrefix(shape, "last_resort") {
						st.SetLastResort(a.ID)
					}
					wantHeartbeat := strings.HasSuffix(shape, "budget_last") || shape == "last_resort_excluded"
					if shape == "last_resort_excluded" {
						st.MarkAccountFailed(peer.ID)
					}
					if strings.HasSuffix(shape, "budget_last") {
						for i := 1; i < st.Budget().MaxAttempts(); i++ {
							st.Budget().RecordAttemptAt(time.Now())
						}
					}
					w := &protocolHeartbeatWriter{gwWriter: newGWWriter()}
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
					defer StopGeminiUniqueCandidateKeepalive(c)
					stub := &geminiDispatchProbe{before: func(req *http.Request) {
						require.False(t, geminiRetryHasTimer(req.Context()), "last candidate must remain untimed")
						time.Sleep(3 * time.Second)
						require.Equal(t, wantHeartbeat, len(w.heartbeatTimes()) > 0, "before response headers")
					}}
					stub.response = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}]}\n\n"))}
					svc := &GeminiMessagesCompatService{httpUpstream: stub, tokenProvider: &GeminiTokenProvider{}, cfg: &config.Config{}}
					_, err := forwardGeminiTestEntry(svc, entry, ctx, c, a)
					StopGeminiUniqueCandidateKeepalive(c)
					require.NoError(t, err)
				})
			})
		}
	}
}
