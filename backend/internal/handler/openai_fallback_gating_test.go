//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// PM decision P2-3: allow_messages_dispatch is the origin group's eligibility
// and the fallback group's capability. The origin's refusal is the permission
// error the origin group gives when it serves; the fallback group's is the
// no-account 503, which never names the fallback group.
func TestMessagesDispatchUnderFallbackFollowsOriginEligibility(t *testing.T) {
	withHandlerResilience(t, nil)
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name             string
		origin, fallback bool
	}{
		{"origin_denies", false, true},
		{"fallback_denies", true, false},
		{"both_allow", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originID := int64(4310)
			origin := &service.Group{ID: originID, Name: "primary-line", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, AllowMessagesDispatch: tc.origin}
			fallback := &service.Group{ID: 4320, Name: "spare-pool", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, AllowMessagesDispatch: tc.fallback}
			authenticated := &service.APIKey{ID: 5310, GroupID: &originID, Group: origin, User: &service.User{ID: 6310}}
			moved := service.CloneAPIKeyWithGroup(authenticated, fallback)
			st := resilience.NewRequestState()
			st.MarkFallback(originID)
			ctx := resilience.WithRequestState(context.Background(), st)
			service.RememberAvailabilityFallbackOrigin(ctx, authenticated)

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
			c.Set(string(middleware.ContextKeyAPIKey), moved)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 6310, Concurrency: 1})

			(&OpenAIGatewayHandler{}).Messages(c)

			body := rec.Body.String()
			require.NotContains(t, body, "spare-pool")
			switch tc.name {
			case "origin_denies":
				require.Equal(t, http.StatusForbidden, rec.Code)
				require.Equal(t, "permission_error", gjson.Get(body, "error.type").String())
				require.Contains(t, body, "This group does not allow /v1/messages dispatch")
			case "fallback_denies":
				require.Equal(t, http.StatusServiceUnavailable, rec.Code)
				require.Equal(t, "api_error", gjson.Get(body, "error.type").String())
				require.True(t, strings.HasPrefix(gjson.Get(body, "error.message").String(), "Service temporarily unavailable"), body)
				require.NotEmpty(t, rec.Header().Get("Retry-After"), "answered like the origin group's no-account 503")
			case "both_allow":
				// Past the gate the bare handler stops at its missing gateway
				// dependencies, without the no-account retry hint.
				require.Equal(t, http.StatusServiceUnavailable, rec.Code)
				require.Empty(t, rec.Header().Get("Retry-After"))
			}
		})
	}
}
