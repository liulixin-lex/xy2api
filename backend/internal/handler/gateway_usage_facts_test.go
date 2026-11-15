//go:build unit

package handler

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGatewayUsageFactsBeforeZeroTokenReturn(t *testing.T) {
	for _, openai := range []bool{false, true} {
		st := resilience.NewRequestState()
		st.EnableGatewayRequest(time.Now())
		st.NoteGatewaySend(9)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), st))
		c.Set(opsAccountIDKey, int64(9))
		if openai {
			recordOpenAIForwardUsage(c, &service.Account{ID: 9, Platform: service.PlatformOpenAI}, &service.OpenAIForwardResult{ResponseFactsKnown: true, UsagePresent: true, UsageSource: "upstream_json", ResponseBodyKind: "json"})
		} else {
			recordGatewayForwardUsage(c, &service.ForwardResult{ResponseFactsKnown: true, UsagePresent: true, UsageSource: "upstream_json", ResponseBodyKind: "json"})
		}
		meta, _ := st.ClaimGatewayRequest(true)
		require.NotNil(t, meta.UsageObserved)
		require.True(t, *meta.UsageObserved)
		require.NotNil(t, meta.TerminalEventSeen)
		require.False(t, *meta.TerminalEventSeen)
	}
}
