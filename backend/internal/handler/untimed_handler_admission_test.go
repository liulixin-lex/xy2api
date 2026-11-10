//go:build unit

package handler

// Regression coverage for paired arrival/body_eof admission boundaries.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestPairedArrivalAdmissionBoundaries(t *testing.T) {
	withHandlerResilience(t, nil)
	gin.SetMode(gin.TestMode)
	// t = T0+offset; body EOF at T0+30s, so body_eof D = T0+270 vs arrival T0+240.
	for _, offset := range []time.Duration{230 * time.Second, 237500 * time.Millisecond, 238 * time.Second, 239 * time.Second, 241 * time.Second} {
		var rows [2]string
		for i, mode := range []string{resilience.BudgetStartArrival, resilience.BudgetStartBodyEOF} {
			settings := resilience.Defaults()
			settings.Budget.BudgetStart = mode
			t0 := time.Now().Add(-offset)
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			st.SetBudget(resilience.NewBudget(ctx, &settings, resilience.KindText, t0))
			st.MarkBodyReceived(t0.Add(30*time.Second), "body_eof", 1)
			b := st.Budget()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)

			// Slot wait after admission: bounded by rem (not rem-2s) in arrival.
			slot := resilienceSlotWaitTimeout(c, 30*time.Second).Round(time.Second)
			// Back-off between attempts (jitter applied; bound is deterministic for a large input).
			backoff := resilienceRetryDelay(ctx, time.Hour).Round(time.Second)
			// Same-account retry with an upstream-requested delay.
			fe := newTestFailoverErr(http.StatusBadGateway, true, false)
			fe.SameAccountRetryDelay = time.Second
			same := sameAccountRetryAllowedCtx(ctx, 3, fe, 0, 3)
			// Selection-exhausted re-selection and its outcome label.
			sel := NewFailoverState(10, false).HandleSelectionExhausted(ctx)
			can := b.CanAttempt(time.Now())
			rows[i] = fmt.Sprintf("slot=%s backoff=%s same=%t selection=%v can=%t exhausted=%s outcome=%s",
				slot, backoff, same, sel, can, b.Exhausted(), deref(st.RecoveryOutcome()))
		}
		t.Logf("REVIEW-B offset=%s arrival={%s} body_eof={%s}", offset, rows[0], rows[1])
		require.Equal(t, rows[0], rows[1], "offset %s", offset)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
