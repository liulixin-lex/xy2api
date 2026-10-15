//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDisconnectFieldsOnOpsRecord(t *testing.T) {
	withHandlerResilience(t, nil)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	client, cancel := context.WithCancel(context.Background())
	st := resilience.NewRequestState()
	st.InitDelivery(client, time.Now().Add(-125*time.Second), func() int64 { return 0 })
	st.SetStage(resilience.StageHeaderWait)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(resilience.WithRequestState(client, st))
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 9, User: &service.User{ID: 5}})

	entry := &service.OpsInsertErrorLogInput{}
	applyResilienceDisconnectFields(c, entry)
	require.Nil(t, entry.DisconnectStage, "client still connected")

	cancel()
	st.ResolveDisconnect()
	entry = &service.OpsInsertErrorLogInput{}
	applyResilienceDisconnectFields(c, entry)
	require.Equal(t, resilience.StageHeaderWait, *entry.DisconnectStage)
	require.Zero(t, *entry.DeliveredBytes)
	require.GreaterOrEqual(t, *entry.ResponseLatencyMs, int64(125000))

	// The record is claimed once.
	require.True(t, st.ClaimUndeliveredRecord())
	require.False(t, st.ClaimUndeliveredRecord())
	recordResilienceUndelivered(c) // already claimed: no-op, no panic
}
