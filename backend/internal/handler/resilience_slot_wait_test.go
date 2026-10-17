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
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// A slot wait is part of the pre-output budget (design §6.1): it must not
// outlast the budget and leave the client to its own idle timeout.
func TestSlotWaitEndsWithThePreOutputBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := resilience.Defaults()
	budgetLeft := 200 * time.Millisecond
	started := time.Now().Add(budgetLeft - time.Duration(cfg.Budget.PreOutputBudgetSeconds)*time.Second)
	st := resilience.NewRequestState()
	st.SetBudget(resilience.NewBudget(context.Background(), &cfg, resilience.KindText, started))

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), st))
	helper := NewConcurrencyHelper(service.NewConcurrencyService(&helperConcurrencyCacheStub{}), SSEPingFormatNone, time.Second)

	begin := time.Now()
	streamStarted := false
	release, err := helper.waitForSlotWithPingTimeout(c, "account", 101, 1, 3*time.Second, false, &streamStarted, true)
	waited := time.Since(begin)
	require.Nil(t, release)
	var cErr *ConcurrencyError
	require.ErrorAs(t, err, &cErr)
	require.True(t, cErr.IsTimeout)
	require.Less(t, waited, time.Second, "the wait ends with the budget, not after its own 3 s")

	// Without a pre-output deadline the wait keeps its own timeout.
	plain, _ := gin.CreateTestContext(httptest.NewRecorder())
	plain.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).
		WithContext(resilience.WithRequestState(context.Background(), resilience.NewRequestState()))
	require.Equal(t, 3*time.Second, resilienceSlotWaitTimeout(plain, 3*time.Second))
}
