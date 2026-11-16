//go:build unit

package handler

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"net/http/httptest"
	"testing"
)

type admissionReadCountRepo struct {
	openAIWSUsageHandlerAccountRepoStub
	reads int
}

func (r *admissionReadCountRepo) GetByID(ctx context.Context, id int64) (*service.Account, error) {
	r.reads++
	return r.openAIWSUsageHandlerAccountRepoStub.GetByID(ctx, id)
}
func TestOpenAIHTTPAdmissionReadsOnceOrTwiceAfterQueue(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "queued"}[queued], func(t *testing.T) {
			account := profitSlotTestAccount(1, 0.3)
			repo := &admissionReadCountRepo{openAIWSUsageHandlerAccountRepoStub: openAIWSUsageHandlerAccountRepoStub{account: *account}}
			selectionSlots := &concurrencyCacheMock{acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return !queued, nil }}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, service.NewConcurrencyService(selectionSlots), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			group := &service.Group{ID: 50, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
			ctx := context.WithValue(context.Background(), ctxkey.Group, group)
			selected, err := gateway.SelectAccountWithLoadAwareness(ctx, &group.ID, "", "", nil)
			require.NoError(t, err)
			require.Equal(t, 1, repo.reads, "initial admission has one authoritative account read")
			slots := &profitCountingConcurrencyCache{queued: queued}
			h := &OpenAIGatewayHandler{gatewayService: gateway, concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(slots), SSEPingFormatNone, 0)}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			started := false
			release, result := h.acquireResponsesAccountSlot(c, &group.ID, "", selected, false, &started, zap.NewNop())
			require.Equal(t, openAISlotAcquireOK, result)
			require.NotNil(t, release)
			release()
			want := 1
			if queued {
				want = 2
			}
			require.Equal(t, want, repo.reads)
		})
	}
}
