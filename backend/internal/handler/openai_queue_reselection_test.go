//go:build unit

package handler

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type queueReselectionCache struct {
	fakeConcurrencyCache
	mu                              sync.Mutex
	queueFull, freeB                bool
	attempts                        map[int64]int
	waitAdds, waitRemoves, releases int
}

func (cache *queueReselectionCache) AcquireAccountSlot(ctx context.Context, id int64, _ int, _ string) (bool, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.attempts[id]++
	return id == 2 && cache.freeB && ctx.Err() == nil, ctx.Err()
}
func (cache *queueReselectionCache) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.waitAdds++
	return !cache.queueFull, nil
}
func (cache *queueReselectionCache) DecrementAccountWaitCount(context.Context, int64) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.waitRemoves++
	return nil
}
func (cache *queueReselectionCache) ReleaseAccountSlot(context.Context, int64, string) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.releases++
	return nil
}

type queueReselectionRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (repo *queueReselectionRepo) ListSchedulableByPlatform(context.Context, string) ([]service.Account, error) {
	return repo.accounts, nil
}
func (repo *queueReselectionRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for i := range repo.accounts {
		if repo.accounts[i].ID == id {
			return &repo.accounts[i], nil
		}
	}
	return nil, service.ErrNoAvailableAccounts
}

func TestQueueRejectionGetsOneFreeSlotReselection(t *testing.T) {
	for _, mode := range []string{"queue_full", "wait_timeout", "all_full", "canceled", "deadline", "budget_expired", "budget_attempts", "hard_owner", "once_only"} {
		t.Run(mode, func(t *testing.T) {
			withHandlerResilience(t, nil)
			cache := &queueReselectionCache{queueFull: mode != "wait_timeout", freeB: mode != "all_full", attempts: map[int64]int{}}
			concurrency := service.NewConcurrencyService(cache)
			repo := &queueReselectionRepo{accounts: []service.Account{*profitSlotTestAccount(1, 1), *profitSlotTestAccount(2, 1)}}
			gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple}, nil, concurrency, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			h := &OpenAIGatewayHandler{gatewayService: gateway, concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatClaude, 0)}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			if mode == "deadline" {
				var expiredCancel context.CancelFunc
				ctx, expiredCancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer expiredCancel()
			}
			if mode == "budget_expired" || mode == "budget_attempts" {
				state := resilience.NewRequestState()
				start := time.Now()
				if mode == "budget_expired" {
					start = start.Add(-time.Hour)
				}
				budget := resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, start)
				if mode == "budget_attempts" {
					for i := 0; i < resilience.Current().Budget.MaxAttempts; i++ {
						budget.RecordAttemptAt(time.Now())
					}
				}
				state.SetBudget(budget)
				ctx = resilience.WithRequestState(ctx, state)
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
			selection := &service.AccountSelectionResult{Account: &repo.accounts[0], WaitPlan: &service.AccountWaitPlan{AccountID: 1, MaxConcurrency: 2, MaxWaiting: 2, Timeout: 20 * time.Millisecond}}
			decision := service.OpenAIAccountScheduleDecision{}
			selectorCalls := 0
			selectNext := func(ctx context.Context, excluded map[int64]struct{}) (*service.AccountSelectionResult, service.OpenAIAccountScheduleDecision, error) {
				selectorCalls++
				require.Contains(t, excluded, int64(1))
				if mode == "wait_timeout" {
					require.Equal(t, 1, cache.waitRemoves, "leave A's wait queue before selecting B")
				}
				return gateway.SelectAccountWithSchedulerForCapability(ctx, nil, "", "", "gpt-5.4", excluded, service.OpenAIUpstreamTransportAny, service.OpenAIEndpointCapabilityChatCompletions, false, false, true)
			}
			started := false
			originalExcluded := map[int64]struct{}{}
			release, result := h.tryOneQueueReselection(c, nil, "", &selection, &decision, originalExcluded, mode == "hard_owner", selectNext, false, &started, zap.NewNop())
			require.Empty(t, originalExcluded, "queue pressure must not mark A as an upstream failure")
			if mode == "queue_full" || mode == "wait_timeout" || mode == "once_only" {
				require.Equal(t, openAISlotAcquireOK, result)
				require.Equal(t, int64(2), selection.Account.ID)
				require.True(t, selection.Acquired)
				require.Nil(t, selection.WaitPlan)
				require.Equal(t, 1, selectorCalls)
				require.Empty(t, w.Body.String())
				require.Empty(t, w.Header().Get("Retry-After"))
				require.NotNil(t, release)
				release()
				require.Equal(t, 1, cache.releases)
			} else {
				require.Equal(t, openAISlotAcquireFailed, result)
				require.Nil(t, release)
				if mode == "all_full" {
					require.Equal(t, 1, selectorCalls)
					require.Equal(t, 429, w.Code)
					require.Contains(t, w.Body.String(), gatewayQueueFullCode)
					require.Equal(t, "5", w.Header().Get("Retry-After"))
				} else {
					require.Zero(t, selectorCalls)
				}
			}
			if mode != "canceled" && mode != "deadline" {
				require.Equal(t, 1, cache.waitAdds, "B must never queue again")
				wantRemoves := 0
				if mode == "wait_timeout" {
					wantRemoves = 1
				}
				require.Equal(t, wantRemoves, cache.waitRemoves)
			}
			if mode == "once_only" {
				selection = &service.AccountSelectionResult{Account: &repo.accounts[0], WaitPlan: &service.AccountWaitPlan{AccountID: 1, MaxConcurrency: 2, MaxWaiting: 2, Timeout: time.Second}}
				_, result = h.tryOneQueueReselection(c, nil, "", &selection, &decision, nil, false, selectNext, false, &started, zap.NewNop())
				require.Equal(t, openAISlotAcquireFailed, result)
				require.Equal(t, 1, selectorCalls)
			}
		})
	}
}

func (r *queueReselectionRepo) ListModelAvailabilityCandidates(ctx context.Context, groupID *int64, platforms []string, includeGrouped bool) ([]service.Account, error) {
	return r.accounts, nil
}
