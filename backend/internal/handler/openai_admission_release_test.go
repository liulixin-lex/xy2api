//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type releaseAdmissionRepo struct {
	service.AccountRepository
	read func(context.Context) (*service.Account, error)
}

func (r *releaseAdmissionRepo) GetByID(ctx context.Context, _ int64) (*service.Account, error) {
	return r.read(ctx)
}

type releaseAdmissionCache struct {
	service.SchedulerCache
	read func(context.Context) (*service.Account, error)
}

func (c *releaseAdmissionCache) GetAccount(ctx context.Context, _ int64) (*service.Account, error) {
	return c.read(ctx)
}

func TestOpenAIAdmissionReleasesExactlyOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"acquired", "quick", "queued"} {
		for _, result := range []string{"pass", "veto", "panic", "cancel", "read_error"} {
			// An immediate acquisition deliberately performs no authoritative read.
			// Cache read errors retain the fresh selection and proceed as before.
			t.Run(path+"/"+result, func(t *testing.T) {
				account := profitSlotTestAccount(1, 0.3)
				slots := &profitCountingConcurrencyCache{queued: path == "queued"}
				var cancel context.CancelFunc
				reads := 0
				read := func(ctx context.Context) (*service.Account, error) {
					reads++
					require.Zero(t, slots.accountReleases.Load(), "admission still owns the slot")
					switch result {
					case "panic":
						panic("synthetic admission panic")
					case "veto":
						latest := *account
						latest.Schedulable = false
						return &latest, nil
					case "read_error":
						return nil, errors.New("synthetic read error")
					case "cancel":
						cancel()
						return nil, ctx.Err()
					}
					return account, nil
				}
				snapshot := service.NewSchedulerSnapshotService(&releaseAdmissionCache{read: read}, nil, nil, nil, nil)
				gateway := service.NewOpenAIGatewayService(&releaseAdmissionRepo{read: read}, nil, nil, nil, nil, nil, nil, nil, snapshot, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				ctx, stop := context.WithCancel(profitSlotTestContext(t, gateway, 50, false))
				cancel = stop
				defer stop()
				h := &OpenAIGatewayHandler{gatewayService: gateway, concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(slots), SSEPingFormatNone, 0)}
				selection := &service.AccountSelectionResult{Account: account, WaitPlan: &service.AccountWaitPlan{AccountID: account.ID, MaxConcurrency: 2, Timeout: time.Second, MaxWaiting: 2}}
				var reservationReleases atomic.Int64
				selection.ReleaseFunc = func() { reservationReleases.Add(1) }
				if path == "acquired" {
					selection.Acquired = true
					selection.ReleaseFunc = func() { slots.accountReleases.Add(1); reservationReleases.Add(1) }
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
				started := false
				call := func() {
					release, outcome := h.acquireResponsesAccountSlot(c, nil, "", selection, false, &started, zap.NewNop())
					veto := result == "veto" || path == "queued" && (result == "read_error" || result == "cancel")
					if veto {
						require.Equal(t, openAISlotAcquireProfitVetoed, outcome)
						require.Nil(t, release)
					} else {
						require.Equal(t, openAISlotAcquireOK, outcome)
						require.NotNil(t, release)
						if result != "cancel" {
							require.Zero(t, slots.accountReleases.Load(), "normal lifecycle owns the transferred slot")
						}
						cancel()
						release()
						release() // racing context cleanup shares the same once
					}
				}
				if result == "panic" {
					require.PanicsWithValue(t, "synthetic admission panic", call)
				} else {
					call()
				}
				require.Equal(t, 1, reads)
				require.Equal(t, int64(1), slots.accountReleases.Load())
				require.Equal(t, int64(1), reservationReleases.Load(), "trial reservation cleanup stays composed with the slot")
				if path == "queued" {
					require.Equal(t, int64(1), slots.waitAdds.Load())
					require.Equal(t, int64(1), slots.waitRemoves.Load())
				}
			})
		}
	}
}

func TestOpenAIWebSocketAdmissionPanicReleasesBothSlots(t *testing.T) {
	var slots *concurrencyCacheMock
	var ready atomic.Bool
	var accountAcquisitions atomic.Int32
	got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
		ingressMode:            service.OpenAIWSIngressModeCtxPool,
		firstPayload:           `{"type":"response.create","model":"gpt-5.6-sol"}`,
		secondPayload:          `{"type":"response.create","model":"gpt-5.6-terra"}`,
		admissionPanicExpected: "synthetic WS admission panic",
		configureAdmission: func(repo *openAIWSUsageHandlerAccountRepoStub, cache *concurrencyCacheMock) {
			slots = cache
			cache.acquireAccountSlotFn = func(context.Context, int64, int, string) (bool, error) { accountAcquisitions.Add(1); return true, nil }
			repo.lookupAccount = func(account service.Account) service.Account {
				if ready.Load() {
					panic("synthetic WS admission panic")
				}
				return account
			}
		},
		afterFirstUpstreamRequest: func(*service.ChannelService) error { ready.Store(true); return nil },
	})
	require.Len(t, got.upstreamPayloads, 1, "panicking admission must not forward the second turn")
	require.Equal(t, int32(2), atomic.LoadInt32(&slots.releaseUserCalled), "one release per acquired user slot")
	require.Equal(t, int32(1), accountAcquisitions.Load(), "this fixture obtains its initial selection without the concurrency service; only the later turn acquires an account slot")
	require.Equal(t, accountAcquisitions.Load(), atomic.LoadInt32(&slots.releaseAccountCalled), "one release per acquired account slot")
}
