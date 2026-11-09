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
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type geminiUniverseCountingRepo struct {
	AccountRepository
	calls int
}

func (r *geminiUniverseCountingRepo) ListByGroup(ctx context.Context, id int64) ([]Account, error) {
	r.calls++
	return r.AccountRepository.ListByGroup(ctx, id)
}

func TestGeminiStaticCandidateUniverseHeartbeat(t *testing.T) {
	for _, entry := range []string{"native", "messages", "chat"} {
		for _, shape := range []string{"unique", "budget_last", "last_resort_excluded", "hard_paused", "mixed", "overages", "fallback", "fallback_other_platform", "fallback_unknown", "fallback_same_member", "fallback_origin"} {
			t.Run(entry+"/"+shape, func(t *testing.T) {
				r, _, repo, _ := newFallbackFixture(t)
				SetResilienceInstance(r)
				t.Cleanup(func() { SetResilienceInstance(nil) })
				withResilienceSettings(t, func(s *resilience.Settings) { s.Keepalive.DelaySeconds, s.Keepalive.IntervalSeconds = 1, 1 })
				synctest.Test(t, func(t *testing.T) {
					group := &Group{ID: 20, Platform: PlatformGemini, Status: StatusActive, Hydrated: true}
					ctx := context.WithValue(geminiAttemptTestContext(context.Background(), 0), ctxkey.Group, group)
					st := resilience.StateFrom(ctx)
					st.SetGroup(20)
					st.NoteTurn(20, "gemini-2.5-flash", "")
					st.SetEdge("cloudflare")
					a := geminiPoolModeAPIKeyAccount()
					a.ID = 101
					a.Status, a.Schedulable = StatusActive, true
					delete(a.Credentials, "pool_mode")
					peer := *a
					peer.ID = 102
					peer.Credentials = map[string]any{"model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"}}
					if shape == "mixed" || shape == "overages" {
						peer.Platform = PlatformAntigravity
						peer.Extra = map[string]any{"mixed_scheduling": true}
					}
					if shape == "overages" {
						peer.Extra["allow_overages"] = true
						peer.Extra["model_rate_limits"] = map[string]any{"gemini-2.5-flash": map[string]any{"rate_limit_reset_at": time.Now().Add(10 * time.Minute).Format(time.RFC3339)}}
					}
					if shape == "hard_paused" {
						until := time.Now().Add(time.Hour)
						peer.TempUnschedulableUntil = &until
						peer.TempUnschedulableReason = "auth:AUTH"
					}
					repo.byGroup[20] = []Account{*a, peer}
					st.SetLastResort(a.ID)
					if shape == "unique" || strings.HasPrefix(shape, "fallback") {
						repo.byGroup[20] = []Account{*a}
					}
					if shape == "last_resort_excluded" {
						st.MarkAccountFailed(peer.ID)
					}
					if shape == "budget_last" {
						for i := 1; i < st.Budget().MaxAttempts(); i++ {
							st.Budget().RecordAttemptAt(time.Now())
						}
					}
					if strings.HasPrefix(shape, "fallback") {
						next := int64(30)
						group.AvailabilityFallbackGroupID = &next
						if shape != "fallback_unknown" {
							fallbackPlatform := PlatformGemini
							if shape == "fallback_other_platform" {
								peer.Platform, fallbackPlatform = PlatformOpenAI, PlatformOpenAI
							}
							members := []Account{peer}
							if shape == "fallback_same_member" {
								members = []Account{*a}
							}
							r.lastResortGroups.entries = map[int64]lastResortGroupEntry{30: {accounts: members, expires: time.Now().Add(time.Minute)}}
							r.fallbackGroupCache.entries = map[int64]fallbackGroupEntry{30: {group: &Group{ID: 30, Platform: fallbackPlatform, Hydrated: true}, expires: time.Now().Add(time.Minute)}}
						}
						if shape == "fallback_origin" {
							group.AvailabilityFallbackGroupID = nil
							st.MarkFallback(30)
							st.SetFallbackOrigin(&APIKey{Group: &Group{ID: 30, Platform: PlatformGemini, Hydrated: true}})
						}
					}
					counter := &geminiUniverseCountingRepo{AccountRepository: repo}
					r.fallbackAccounts = counter
					w := &protocolHeartbeatWriter{gwWriter: newGWWriter()}
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
					defer StopGeminiUniqueCandidateKeepalive(c)
					wantHeartbeat := shape == "unique" || shape == "fallback_same_member"
					stub := &geminiDispatchProbe{before: func(req *http.Request) {
						require.False(t, geminiRetryHasTimer(req.Context()), "existing last-candidate timer exemption stays intact")
						time.Sleep(3 * time.Second)
						require.Equal(t, wantHeartbeat, len(w.heartbeatTimes()) > 0, "before response headers")
					}}
					stub.response = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}]}\n\n"))}
					svc := &GeminiMessagesCompatService{httpUpstream: stub, tokenProvider: &GeminiTokenProvider{}, cfg: &config.Config{}}
					_, err := forwardGeminiTestEntry(svc, entry, ctx, c, a)
					require.NoError(t, err)
					StopGeminiUniqueCandidateKeepalive(c)
					require.Equal(t, 1, counter.calls, "one cached group-list fill, as in 7797dcf4d")
					if shape == "overages" {
						require.True(t, peer.IsOveragesEnabled())
						require.False(t, peer.isCreditsExhausted())
						tier, _, _ := resiliencePenaltyOf(&peer, "gemini-2.5-flash", time.Now())
						require.Equal(t, resilience.TierHard, tier, "the retired dynamic predictor would exclude this usable fallback")
						require.True(t, peer.IsSchedulableForModel("gemini-2.5-flash"))
						accounts := &mockAccountRepoForPlatform{accounts: []Account{*a, peer}, accountsByID: map[int64]*Account{a.ID: a, peer.ID: &peer}}
						gateway := &GatewayService{accountRepo: accounts, groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{20: group}}, cache: &mockGatewayCacheForPlatform{}, cfg: testConfig(), resilience: r}
						selected, err := gateway.SelectAccountWithLoadAwareness(ctx, &group.ID, "", "gemini-2.5-flash", map[int64]struct{}{a.ID: {}}, "", 0)
						require.NoError(t, err)
						require.NotNil(t, selected)
						if selected.Acquired && selected.ReleaseFunc != nil {
							defer selected.ReleaseFunc()
						}
						require.Equal(t, peer.ID, selected.Account.ID)
						require.Equal(t, PlatformAntigravity, selected.Account.Platform)
						t.Logf("heartbeat=false actual_next_account=%d platform=%s", selected.Account.ID, selected.Account.Platform)
					}
				})
			})
		}
	}
}

func TestGeminiCandidateUniverseReusesGroupListCache(t *testing.T) {
	r, _, repo, _ := newFallbackFixture(t)
	SetResilienceInstance(r)
	t.Cleanup(func() { SetResilienceInstance(nil) })
	ctx := context.WithValue(geminiAttemptTestContext(context.Background(), 0), ctxkey.Group, &Group{ID: 20, Platform: PlatformGemini, Hydrated: true})
	st := resilience.StateFrom(ctx)
	st.SetGroup(20)
	st.NoteTurn(20, "gemini-2.5-flash", "")
	a := geminiPoolModeAPIKeyAccount()
	repo.byGroup[20] = []Account{*a}
	counter := &geminiUniverseCountingRepo{AccountRepository: repo}
	r.fallbackAccounts = counter
	for i := 0; i < 2; i++ {
		g := newGeminiAttempt(ctx, a, "gemini-2.5-flash", "generateContent", []byte(geminiTransportTestNativeBody), true, PreOutputProtocolGemini, time.Now())
		g.close()
	}
	require.Equal(t, 1, counter.calls, "7797dcf4d also fills this existing cache once across attempts")
}
