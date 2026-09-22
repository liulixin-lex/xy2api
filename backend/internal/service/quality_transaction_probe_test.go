package service

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestQualityTransactionProbe(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
	body := []byte(`{"model":"gpt-6-astra","prompt_cache_key":"transaction-session","input":"hello"}`)
	repo := stubOpenAIAccountRepo{accounts: []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10, Priority: 1, Credentials: map[string]any{"base_url": "https://bad.example/v1"}},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10, Priority: 2, Credentials: map[string]any{"base_url": "https://good.example/v1"}},
	}}
	svc := &OpenAIGatewayService{accountRepo: repo, cache: &stubGatewayCache{}}
	selectNext := func() (*gin.Context, *Account) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		c.Set("api_key", &APIKey{ID: 77})
		hash := svc.GenerateSessionHash(c, body)
		selected, _, err := svc.SelectAccountWithScheduler(c.Request.Context(), nil, "", hash, "gpt-6-astra", nil, OpenAIUpstreamTransportHTTPSSE, false)
		require.NoError(t, err)
		require.NotNil(t, selected)
		if selected.ReleaseFunc != nil {
			selected.ReleaseFunc()
		}
		return c, selected.Account
	}
	c, first := selectNext()
	beginUpstreamResponseModelObservation(c)
	_, err := svc.buildUpstreamRequest(c.Request.Context(), c, first, body, "fixture", false, "transaction-session", false)
	require.NoError(t, err)
	upstreamResponseModelObserverFromContext(c).ObserveOpenAI([]byte(`{"type":"response.created","response":{"model":"gpt-5.6-luna"}}`), "response.created")
	_, next := selectNext()
	_, third := selectNext()
	fmt.Printf("QUALITY_PROBE first=%d observed=gpt-5.6-luna next=%d stable=%d\n", first.ID, next.ID, third.ID)
}
