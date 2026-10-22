//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// fallbackMovedContext is a request the availability fallback middleware
// moved from group 10 to group 20.
func fallbackMovedContext(t *testing.T) (*gin.Context, *service.APIKey) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/batches", nil).WithContext(ctx)
	originID := int64(10)
	authenticated := &service.APIKey{ID: 22, UserID: 11, GroupID: &originID, Group: &service.Group{ID: originID, Platform: service.PlatformGemini}}
	require.True(t, st.MarkFallback(originID))
	service.RememberAvailabilityFallbackOrigin(ctx, authenticated)
	moved := service.CloneAPIKeyWithGroup(authenticated, &service.Group{ID: 20, Platform: service.PlatformGemini})
	c.Set(string(middleware.ContextKeyAPIKey), moved)
	return c, authenticated
}

func TestBatchImageOwnerUnderFallbackCarriesTheBillingGroup(t *testing.T) {
	c, _ := fallbackMovedContext(t)
	owner, ok := batchImageOwnerFromContext(c)
	require.True(t, ok)
	require.Equal(t, int64(20), *owner.GroupID, "the serving group's accounts take the job")
	require.NotNil(t, owner.BillingGroupID)
	require.Equal(t, int64(10), *owner.BillingGroupID)
}

// The Responses WebSocket checks every response.create against the model
// allow-list of the group the request was made for (PM decision Q1).
func TestResponsesWebSocketAllowlistUnderFallbackIsTheOriginGroup(t *testing.T) {
	c, authenticated := fallbackMovedContext(t)
	moved, _ := middleware.GetAPIKeyFromContext(c)
	require.Same(t, authenticated.Group, modelAllowlistGroup(c, moved))

	plain, _ := gin.CreateTestContext(httptest.NewRecorder())
	plain.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	require.Same(t, moved.Group, modelAllowlistGroup(plain, moved), "no fallback: the key's own group")
	require.Nil(t, modelAllowlistGroup(plain, nil))
}

// Users never see an availability fallback group: the available channel
// groups carry no fallback field (fallback-billing design §2.4).
func TestUserAvailableGroupHasNoFallback(t *testing.T) {
	typ := reflect.TypeOf(userAvailableGroup{})
	for i := 0; i < typ.NumField(); i++ {
		require.NotContains(t, typ.Field(i).Tag.Get("json"), "availability_fallback", typ.Field(i).Name)
	}
}
