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

func TestGeminiNativeHandlerLastResortAtSwitchLimitKeepsAlive(t *testing.T) {
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
		require.Contains(t, rec.Body.String(), ":\n\n", "handler switch limit must authorize the non-sole last-resort heartbeat")
		require.Contains(t, rec.Body.String(), "data: ")
		require.Equal(t, http.StatusOK, rec.Code)
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
				want := FailoverContinue
				if committed {
					want = FailoverExhausted
					require.Zero(t, fs.SwitchCount, "do not select another platform after committing heartbeat")
				}
				require.Equal(t, want, action)
				require.Same(t, failure, fs.LastFailoverErr)
				require.Equal(t, 1, resilience.StateFrom(c.Request.Context()).Budget().Attempts(), "retain the existing failure accounting exactly once")
			})
		})
	}
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
