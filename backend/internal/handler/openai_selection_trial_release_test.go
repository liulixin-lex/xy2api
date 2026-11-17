//go:build unit

package handler

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type trialWaitConcurrency struct {
	profitCountingConcurrencyCache
	waiting    chan struct{}
	resume     chan struct{}
	cancelWait bool
}

func (c *trialWaitConcurrency) IncrementAccountWaitCount(ctx context.Context, _ int64, _ int) (bool, error) {
	c.waitAdds.Add(1)
	close(c.waiting)
	<-c.resume
	return true, nil
}
func (c *trialWaitConcurrency) AcquireAccountSlot(ctx context.Context, _ int64, _ int, _ string) (bool, error) {
	n := c.attempts.Add(1)
	if n == 1 {
		return false, nil
	}
	if c.cancelWait {
		return false, ctx.Err()
	}
	return true, nil
}

func TestOpenAISelectionTrialWaitCleanup(t *testing.T) {
	for _, cancelWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued_success", true: "queued_cancel"}[cancelWait], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			gw := &service.OpenAIGatewayService{}
			cache := &trialWaitConcurrency{waiting: make(chan struct{}), resume: make(chan struct{}), cancelWait: cancelWait}
			h := &OpenAIGatewayHandler{gatewayService: gw, concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatClaude, 0)}
			ctx, cancel := context.WithCancel(profitSlotTestContext(t, gw, 50, false))
			defer cancel()
			var leaseHeld atomic.Bool
			leaseHeld.Store(true)
			var releases atomic.Int32
			selection := &service.AccountSelectionResult{Account: profitSlotTestAccount(1, 0.3), WaitPlan: &service.AccountWaitPlan{AccountID: 1, MaxConcurrency: 2, Timeout: time.Second, MaxWaiting: 2}, ReleaseFunc: func() { leaseHeld.Store(false); releases.Add(1) }}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				<-cache.waiting
				if cancelWait {
					cancel()
				}
				close(cache.resume)
			}()
			started := false
			group := int64(50)
			release, outcome := h.acquireResponsesAccountSlot(c, &group, "", selection, false, &started, zap.NewNop())
			<-done
			if cancelWait {
				require.NotEqual(t, openAISlotAcquireOK, outcome)
				require.Nil(t, release)
			} else {
				require.Equal(t, openAISlotAcquireOK, outcome)
				require.True(t, leaseHeld.Load())
				release()
				release()
				require.EqualValues(t, 1, cache.accountReleases.Load())
			}
			require.False(t, leaseHeld.Load())
			require.EqualValues(t, 1, releases.Load())
			require.EqualValues(t, 1, cache.waitAdds.Load())
			require.EqualValues(t, 1, cache.waitRemoves.Load())
		})
	}
}
