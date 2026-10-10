//go:build unit

package handler

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/internal/testutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type cacheSpilloverConcurrency struct {
	fakeConcurrencyCache
	queued   bool
	attempts atomic.Int64
	releases atomic.Int64
}

func (c *cacheSpilloverConcurrency) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	return c.attempts.Add(1) > 1 || !c.queued, nil
}

func (c *cacheSpilloverConcurrency) ReleaseAccountSlot(context.Context, int64, string) error {
	c.releases.Add(1)
	return nil
}

func TestOpenAICacheSpilloverAdmissionPreservesAffinity(t *testing.T) {
	for _, path := range []string{"acquired", "quick", "queued"} {
		for _, spillover := range []bool{false, true} {
			name := path + "/bind"
			if spillover {
				name = path + "/spillover"
			}
			t.Run(name, func(t *testing.T) {
				cache := testutil.NewRedisGatewayCache(t)
				concurrency := &cacheSpilloverConcurrency{queued: path == "queued"}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				gw := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, cache, cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				h := &OpenAIGatewayHandler{gatewayService: gw, concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(concurrency), SSEPingFormatClaude, 0)}
				group := int64(10)
				ctx := context.Background()
				// The old owner may expire during queueing; spillover must still not rebind.
				require.NoError(t, cache.SetSessionAccountID(ctx, group, "openai:v2:session", 1, time.Minute))
				require.NoError(t, cache.DeleteSessionAccountID(ctx, group, "openai:v2:session"))
				selection := &service.AccountSelectionResult{Account: &service.Account{ID: 2, Platform: service.PlatformOpenAI}, StickyCapacitySpillover: spillover}
				if path == "acquired" {
					selection.Acquired = true
					selection.ReleaseFunc = func() { concurrency.releases.Add(1) }
				} else {
					selection.WaitPlan = &service.AccountWaitPlan{AccountID: 2, MaxConcurrency: 2, MaxWaiting: 2, Timeout: time.Second}
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
				started := false
				release, result := h.acquireResponsesAccountSlot(c, &group, "v2:session", selection, false, &started, zap.NewNop())
				require.Equal(t, openAISlotAcquireOK, result)
				require.NotNil(t, release)
				release()
				require.Equal(t, int64(1), concurrency.releases.Load())
				owner, err := cache.GetSessionAccountID(ctx, group, "openai:v2:session")
				if spillover {
					require.ErrorIs(t, err, service.ErrStickySessionNotFound)
				} else {
					require.NoError(t, err)
					require.Equal(t, int64(2), owner)
				}
				if path == "queued" {
					require.GreaterOrEqual(t, concurrency.attempts.Load(), int64(2))
				}
			})
		}
	}
}
