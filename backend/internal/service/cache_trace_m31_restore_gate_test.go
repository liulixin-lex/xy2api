package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// M3.1 FX-L01-1: restoreOpenAIUpstreamIdentityForClient records the restore
// fallback into the trace, so the ingress structure test treats it as a trace
// hook. Its only gate is openAIUpstreamIdentityResponseApplied, which must be
// false and allocation-free whenever L-01 is not enabled, even if a stale or
// forged wire value is present in the gin context.
func TestCacheTraceM31RestoreGateFalseWhenL01Disabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.False(t, CachePolicyFromGin(c).UpstreamIdentity().Enabled())
	require.False(t, openAIUpstreamIdentityResponseApplied(c, account))
	c.Set(openAIUpstreamIdentityWireGinKey, openAIUpstreamIdentityWire{accountID: account.ID, uuid: "00000000-0000-4000-8000-000000000000"})
	require.False(t, openAIUpstreamIdentityResponseApplied(c, account), "policy gate precedes the wire lookup")
	require.Zero(t, testing.AllocsPerRun(100, func() { _ = openAIUpstreamIdentityResponseApplied(c, account) }))
}
