//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	middleware "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNativeImagesActualBillableCount(t *testing.T) {
	for _, metered := range []bool{false, true} {
		t.Run(map[bool]string{false: "unbilled", true: "metered"}[metered], func(t *testing.T) {
			withHandlerResilience(t, nil)
			repo := &requestRecordRepo{}
			reply := `{"data":[]}`
			if metered {
				reply = `{"data":[],"usage":{"input_tokens":10,"output_tokens":5}}`
			}
			upstream := &captureUpstream{reply: func(int, *http.Request, []byte) (*http.Response, error) {
				return captureResponse(200, reply, false), nil
			}}
			handler, _, _ := captureFixture(t, service.Account{}, upstream, captureOptions{usage: repo})
			account := service.Account{ID: 9901, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://capture.example"}}
			billing := service.NewBillingService(handler.cfg, nil)
			handler.gatewayService = service.NewOpenAIGatewayService(&openAIWSUsageHandlerAccountRepoStub{account: account}, repo, nil, nil, nil, nil, nil, handler.cfg, nil, nil, billing, nil, handler.billingCacheService, upstream, &service.DeferredService{}, nil, nil, service.NewModelPricingResolver(nil, billing), nil, nil, nil, nil)
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-1","prompt":"fixture","n":3}`))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState()))
			service.InstallDeliveryTracking(c, time.Now())
			group := &service.Group{ID: 4201, AllowImageGeneration: true, RateMultiplier: 1, ModelPricing: []service.ChannelModelPricing{{Models: []string{"gpt-image-1"}, BillingMode: service.BillingModeToken, InputPrice: new(float64(0.001)), OutputPrice: new(float64(0.002))}}}
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1801, UserID: 1701, GroupID: &group.ID, Group: group, User: &service.User{ID: 1701, Status: service.StatusActive}})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1701, Concurrency: 2})
			handler.Images(c)
			require.Equal(t, http.StatusBadGateway, response.Code)
			require.Equal(t, "invalid_image_response", gjson.GetBytes(response.Body.Bytes(), "error.code").String())
			require.Equal(t, 1, upstream.calls, "an empty generation must not be replayed")
			if metered {
				require.Len(t, repo.bills, 1)
				require.Empty(t, repo.unbilled)
				bill := repo.bills[0]
				require.Zero(t, bill.ImageCount)
				require.Positive(t, bill.TotalCost)
				require.Equal(t, 10, bill.InputTokens)
				require.Equal(t, 5, bill.OutputTokens)
				require.EqualValues(t, resilience.RequestFailed, *bill.Result)
			} else {
				require.Empty(t, repo.bills)
				require.Len(t, repo.unbilled, 1)
				require.EqualValues(t, resilience.RequestFailed, repo.unbilled[0].Result)
			}
		})
	}
}
