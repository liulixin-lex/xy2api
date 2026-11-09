//go:build unit

package handler

import (
	"bytes"
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
	middleware "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type geminiFinalHTTPStub struct {
	service.HTTPUpstream
	calls int
}

func (s *geminiFinalHTTPStub) Do(*http.Request, string, int64, int) (*http.Response, error) {
	s.calls++
	time.Sleep(3 * time.Second)
	return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"bad key"}}`))}, nil
}

func TestGeminiNativeHandlerMultipleCandidatesAtSwitchLimitStayUncommitted(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) {
		s.Keepalive.DelaySeconds, s.Keepalive.IntervalSeconds = 1, 1
	})
	groupID := int64(9200)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformGemini, Status: service.StatusActive}
	a := &service.Account{ID: 9201, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1, Credentials: map[string]any{"api_key": "test-key"}, AccountGroups: []service.AccountGroup{{AccountID: 9201, GroupID: groupID}}}
	peer := *a
	peer.ID, peer.Priority = 9204, 2
	peer.AccountGroups = []service.AccountGroup{{AccountID: peer.ID, GroupID: groupID}}
	h, cleanup := newTestGatewayHandler(t, group, []*service.Account{a, &peer})
	defer cleanup()
	h.maxAccountSwitchesGemini = 0
	upstream := &geminiFinalHTTPStub{}
	h.geminiCompatService = service.NewGeminiMessagesCompatService(nil, nil, nil, nil, nil, nil, upstream, nil, &config.Config{})
	synctest.Test(t, func(t *testing.T) {
		st := resilience.NewRequestState()
		ctx := resilience.WithRequestState(context.Background(), st)
		st.SetGroup(groupID)
		st.SetEdge("cf")
		st.SetLastResort(a.ID)
		st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
		key := &service.APIKey{ID: 9202, UserID: 9203, GroupID: &groupID, Group: group, Status: service.StatusActive, User: &service.User{ID: 9203, Concurrency: 10, Balance: 100}}
		router := gin.New()
		router.POST("/v1beta/models/*modelAction", func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyAPIKey), key)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 10})
			h.GeminiV1BetaModels(c)
		})
		req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", bytes.NewBufferString(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)).WithContext(ctx)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, 1, upstream.calls)
		require.NotContains(t, rec.Body.String(), ":\n\n", "multiple configured accounts never authorize an early heartbeat")
		require.Equal(t, http.StatusBadGateway, rec.Code)
		require.Equal(t, 1, st.Budget().Attempts())
	})
}

func TestGeminiCommittedFinalKeepalivePreventsHandlerSwitch(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncommitted", true: "committed"}[committed], func(t *testing.T) {
			withHandlerResilience(t, func(s *resilience.Settings) {
				s.Keepalive.DelaySeconds, s.Keepalive.IntervalSeconds = 1, 1
			})
			synctest.Test(t, func(t *testing.T) {
				c, _ := keepaliveCtxWithRecorder(t, "cf", resilience.KindText)
				a := &service.Account{ID: 7, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey}
				require.True(t, service.StartGeminiUniqueCandidateKeepalive(c, a, true, true, service.PreOutputProtocolGemini))
				defer stopPreOutputKeepalive(c)
				if committed {
					time.Sleep(2 * time.Second)
					synctest.Wait()
				}
				failure := &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway}
				fs := NewFailoverState(2, false)
				action := fs.HandleFailoverError(c.Request.Context(), nil, a.ID, a.Platform, 0, failure)
				require.Equal(t, FailoverContinue, action, "retain the original retry/backoff decision")
				for _, next := range []*service.Account{
					a,
					{ID: a.ID + 1, Platform: service.PlatformGemini},
					{ID: a.ID + 1, Platform: service.PlatformAntigravity},
					{ID: a.ID, Platform: service.PlatformAntigravity},
				} {
					released := false
					selection := &service.AccountSelectionResult{Account: next, Acquired: true, ReleaseFunc: func() { released = true }}
					wantBlocked := committed && (next.ID != a.ID || next.Platform != a.Platform)
					require.Equal(t, wantBlocked, geminiKeepaliveBlocksSelection(c.Request.Context(), selection))
					require.Equal(t, wantBlocked, released, "a blocked selection releases its acquired slot")
				}
				require.Same(t, failure, fs.LastFailoverErr)
				require.Equal(t, 1, resilience.StateFrom(c.Request.Context()).Budget().Attempts(), "retain the existing failure accounting exactly once")
			})
		})
	}
}

func TestGeminiCommittedKeepalivePreservesSelectionBackoff(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) {
		s.Keepalive.DelaySeconds, s.Keepalive.IntervalSeconds = 1, 1
	})
	synctest.Test(t, func(t *testing.T) {
		c, _ := keepaliveCtxWithRecorder(t, "cf", resilience.KindText)
		a := &service.Account{ID: 7, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey}
		require.True(t, service.StartGeminiUniqueCandidateKeepalive(c, a, true, true, service.PreOutputProtocolGemini))
		defer stopPreOutputKeepalive(c)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		failure := &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}
		fs := NewFailoverState(2, false)
		require.Equal(t, FailoverContinue, fs.HandleFailoverError(c.Request.Context(), nil, a.ID, a.Platform, 0, failure))
		require.Contains(t, fs.FailedAccountIDs, a.ID)
		started := time.Now()
		require.Equal(t, FailoverContinue, fs.HandleSelectionExhausted(c.Request.Context()))
		require.Equal(t, singleAccountBackoffDelay, time.Since(started))
		require.Empty(t, fs.FailedAccountIDs)
		require.Same(t, failure, fs.LastFailoverErr)
		require.Equal(t, 1, resilience.StateFrom(c.Request.Context()).Budget().Attempts())
	})
}

func TestGeminiHandlerWaitsForServiceKeepaliveAdmission(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) {
		s.Keepalive.DelaySeconds, s.Keepalive.IntervalSeconds = 1, 1
	})
	synctest.Test(t, func(t *testing.T) {
		c, rec := keepaliveCtxWithRecorder(t, "cf", resilience.KindText)
		armPreOutputKeepalive(c, service.PreOutputProtocolChat, true, &service.Account{ID: 7, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey})
		defer stopPreOutputKeepalive(c)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		require.False(t, service.PreOutputKeepaliveCommitted(c), "generic compat arming must not bypass Gemini's cross-platform admission")
		require.Empty(t, rec.Body.String())
	})
}

func TestGeminiCommittedKeepalivePreservesSameAccountRetry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		retryLimit int
		errorCap   int
		deadline   bool
		want       int
	}{
		{name: "pool_429", status: http.StatusTooManyRequests, retryLimit: 2, want: 2},
		{name: "transient_503", status: http.StatusServiceUnavailable, retryLimit: 2, want: 2},
		{name: "error_cap", status: http.StatusTooManyRequests, retryLimit: 3, errorCap: 1, want: 1},
		{name: "deadline_window", status: http.StatusTooManyRequests, deadline: true, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withHandlerResilience(t, func(s *resilience.Settings) {
				s.Keepalive.DelaySeconds, s.Keepalive.IntervalSeconds = 1, 1
				s.Budget.MaxAttempts, s.Budget.SameAccountRetryMax = 5, 2
			})
			synctest.Test(t, func(t *testing.T) {
				c, _ := keepaliveCtxWithRecorder(t, "cf", resilience.KindText)
				a := &service.Account{ID: 7, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey}
				require.True(t, service.StartGeminiUniqueCandidateKeepalive(c, a, true, true, service.PreOutputProtocolGemini))
				defer stopPreOutputKeepalive(c)
				time.Sleep(2 * time.Second)
				synctest.Wait()
				require.True(t, service.PreOutputKeepaliveCommitted(c))
				failure := &service.UpstreamFailoverError{StatusCode: tc.status, RetryableOnSameAccount: true, SameAccountRetryMax: tc.errorCap, SameAccountRetryDelay: time.Millisecond}
				if tc.deadline {
					failure.SameAccountRetryDeadline = time.Now().Add(time.Minute)
				}
				fs := NewFailoverState(0, true)
				scheduler := &mockTempUnscheduler{}
				for attempt := 1; attempt <= tc.want; attempt++ {
					action := fs.HandleFailoverError(c.Request.Context(), scheduler, a.ID, a.Platform, tc.retryLimit, failure)
					t.Logf("attempt=%d action=%d retries=%d switches=%d", attempt, action, fs.SameAccountRetryCount[a.ID], fs.SwitchCount)
					require.Equal(t, FailoverContinue, action)
					require.Equal(t, attempt, fs.SameAccountRetryCount[a.ID])
					require.Zero(t, fs.SwitchCount)
					require.Empty(t, fs.FailedAccountIDs)
					require.Empty(t, scheduler.calls)
					require.False(t, fs.ForceCacheBilling)
				}
				require.Equal(t, FailoverExhausted, fs.HandleFailoverError(c.Request.Context(), scheduler, a.ID, a.Platform, tc.retryLimit, failure))
				require.Equal(t, tc.want, fs.SameAccountRetryCount[a.ID])
				require.Zero(t, fs.SwitchCount)
				require.Len(t, scheduler.calls, 1)
				require.Contains(t, fs.FailedAccountIDs, a.ID)
				require.True(t, fs.ForceCacheBilling)
				require.Same(t, failure, fs.LastFailoverErr)
				require.Equal(t, tc.want+1, resilience.StateFrom(c.Request.Context()).Budget().Attempts())
			})
		})
	}
}
