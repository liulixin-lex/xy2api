package handler

import (
	"context"
	"sync/atomic"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesWebSocketQueuedTurnRechecksAndReleasesBothSlots(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		t.Run(mode, func(t *testing.T) {
			var disabled atomic.Bool
			var acquisitions atomic.Int32
			var usersBefore, accountsBefore int32
			var slots *concurrencyCacheMock
			waiting, changed, stop := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer close(stop)
			go func() {
				select {
				case <-waiting:
				case <-stop:
					return
				}
				disabled.Store(true)
				close(changed)
			}()
			got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
				firstPayload: wsGroupPricingFirst, secondPayload: wsGroupPricingSecond,
				ingressMode: mode, secondTurnCloseExpected: true,
				closeStatus: coderws.StatusTryAgainLater, closeReason: "no longer eligible",
				configureAdmission: func(repo *openAIWSUsageHandlerAccountRepoStub, cache *concurrencyCacheMock) {
					slots = cache
					repo.lookupAccount = func(account service.Account) service.Account {
						if disabled.Load() {
							account.Schedulable = false
						}
						return account
					}
					cache.acquireAccountSlotFn = func(context.Context, int64, int, string) (bool, error) {
						if acquisitions.Add(1) == 1 {
							usersBefore = atomic.LoadInt32(&cache.releaseUserCalled)
							accountsBefore = atomic.LoadInt32(&cache.releaseAccountCalled)
							close(waiting)
							<-changed
							return false, nil
						}
						return true, nil
					}
				},
			})
			require.EqualValues(t, 2, acquisitions.Load(), "one denied slot acquisition followed by success")
			require.Len(t, got.upstreamPayloads, 1, "the rejected queued turn must not reach upstream")
			require.Equal(t, usersBefore+1, atomic.LoadInt32(&slots.releaseUserCalled))
			require.Equal(t, accountsBefore+1, atomic.LoadInt32(&slots.releaseAccountCalled))
		})
	}
}
