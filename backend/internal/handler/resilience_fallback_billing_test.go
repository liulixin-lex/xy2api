//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
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
