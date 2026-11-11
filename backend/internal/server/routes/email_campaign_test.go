//go:build unit

package routes

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type campaignRouteRepo struct {
	service.EmailCampaignRepository
}

func (campaignRouteRepo) Action(context.Context, int64, string) (*service.EmailCampaign, int64, error) {
	return &service.EmailCampaign{ID: 1, Status: "paused"}, 0, nil
}
func (campaignRouteRepo) ReadSecret(context.Context) (string, error) {
	return "", service.ErrEmailCampaignInvalidInput
}
func TestEmailCampaignRoutesStepUpAndPublic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := service.NewEmailCampaignService(campaignRouteRepo{}, nil, nil, nil, nil)
	h := &handler.Handlers{EmailCampaign: handler.NewEmailCampaignHandler(svc)}
	router := gin.New()
	v1 := router.Group("/api/v1")
	admin := v1.Group("/admin")
	admin.Use(func(c *gin.Context) { c.Header("X-Admin-Chain", "yes"); c.Next() })
	step := middleware.StepUpAuthMiddleware(func(c *gin.Context) { c.AbortWithStatus(428) })
	registerEmailCampaignRoutes(v1, admin, h, step, nil)
	for _, tc := range []struct {
		method, path string
		status       int
		admin        bool
	}{{"POST", "/api/v1/admin/email-campaigns", 428, true}, {"POST", "/api/v1/admin/email-campaigns/1/pause", 200, true}, {"GET", "/api/v1/email-campaigns/unsubscribe?token=bad", 400, false}, {"POST", "/api/v1/email-campaigns/unsubscribe?token=bad", 400, false}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}")))
		require.Equal(t, tc.status, w.Code)
		require.Equal(t, tc.admin, w.Header().Get("X-Admin-Chain") == "yes")
	}
	count := 0
	for _, route := range router.Routes() {
		if strings.HasPrefix(route.Path, "/api/v1/admin/") {
			entry, ok := operatoraccess.Lookup(operatoraccess.Key(route.Method, route.Path))
			require.True(t, ok)
			require.Equal(t, operatoraccess.KindSuperOnly, entry.Kind)
			count++
		}
	}
	require.Equal(t, 16, count)
}
