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

func TestBudgetTraceSlotWaitOnlyRecordsRemainingExhaustion(t *testing.T) {
	for _, mode := range []string{"remaining", "own_timeout", "parent_cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, trace := resilience.WithBudgetTraceForTest(context.Background())
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			st := resilience.NewRequestState()
			ctx = resilience.WithRequestState(ctx, st)
			cfg := resilience.Defaults()
			remaining, own := 40*time.Millisecond, time.Second
			if mode == "own_timeout" {
				remaining, own = time.Second, 40*time.Millisecond
			}
			st.SetBudget(resilience.NewBudget(ctx, &cfg, resilience.KindText, time.Now().Add(-240*time.Second+remaining)))
			if mode == "parent_cancel" {
				cancel()
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			helper := NewConcurrencyHelper(service.NewConcurrencyService(&helperConcurrencyCacheStub{}), SSEPingFormatNone, time.Second)
			streamStarted := false
			_, err := helper.waitForSlotWithPingTimeout(c, "account", 101, 1, own, false, &streamStarted, false)
			require.Error(t, err)
			events := trace.Events()
			if mode != "remaining" {
				require.Empty(t, events)
				return
			}
			require.Len(t, events, 1)
			require.Equal(t, "slot_wait_exhausted", events[0].Kind)
			require.EqualValues(t, 101, events[0].AccountID)
			require.Equal(t, own, events[0].Input)
			require.LessOrEqual(t, events[0].Output, remaining)
			require.GreaterOrEqual(t, events[0].Offset, 240*time.Second)
		})
	}
}
