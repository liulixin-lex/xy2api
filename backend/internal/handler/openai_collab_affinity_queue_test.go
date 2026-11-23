//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestCollabAffinityQueueReselectionKeepsOriginalTimeoutAndError(t *testing.T) {
	withHandlerResilience(t, nil)
	type outcome struct {
		status      int
		body, retry string
		elapsed     time.Duration
	}
	var baseline outcome
	for _, on := range []bool{false, true} {
		for _, free := range []bool{false, true} {
			t.Run(fmt.Sprintf("affinity=%t/free=%t", on, free), func(t *testing.T) {
				cache := &queueReselectionCache{freeB: free, attempts: map[int64]int{}}
				concurrency := service.NewConcurrencyService(cache)
				repo := &queueReselectionRepo{accounts: []service.Account{*profitSlotTestAccount(1, 1), *profitSlotTestAccount(2, 1)}}
				gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple}, nil, concurrency, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				h := &OpenAIGatewayHandler{gatewayService: gateway, concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatClaude, 0)}
				synctest.Test(t, func(t *testing.T) {
					settings := resilience.Defaults()
					settings.Collab = resilience.CollabSettings{TreeAffinity: on}
					body := []byte(`{"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"synthetic"}]}]}`)
					ctx := service.WithOpenAIEncryptedContentMigrationGuard(context.Background(), body)
					ctx = service.WithOpenAICollabTreeAffinity(ctx, resilience.NewCollabTreeAffinity(16, time.Hour), &settings, 0,
						resilience.CollabTreeHint{Scope: resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}, Thread: "child", Parent: "root"}, resilience.CollabRoleChild, true, &config.Config{})
					w := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
					selection := &service.AccountSelectionResult{Account: &repo.accounts[0], WaitPlan: &service.AccountWaitPlan{AccountID: 1, MaxConcurrency: 1, MaxWaiting: 2, Timeout: 120 * time.Second}}
					decision := service.OpenAIAccountScheduleDecision{}
					calls := 0
					selectNext := func(ctx context.Context, excluded map[int64]struct{}) (*service.AccountSelectionResult, service.OpenAIAccountScheduleDecision, error) {
						calls++
						require.Equal(t, 1, cache.waitRemoves)
						require.Contains(t, excluded, int64(1))
						return gateway.SelectAccountWithScheduler(ctx, nil, "", "", "m", excluded, service.OpenAIUpstreamTransportHTTPSSE, false)
					}
					start := time.Now()
					streamStarted := false
					release, result := h.tryOneQueueReselection(c, nil, "", &selection, &decision, nil, false, selectNext, false, &streamStarted, zap.NewNop())
					got := outcome{status: w.Code, body: w.Body.String(), retry: w.Header().Get("Retry-After"), elapsed: time.Since(start)}
					require.Equal(t, 120*time.Second, got.elapsed)
					if on && free {
						require.Equal(t, openAISlotAcquireOK, result)
						require.EqualValues(t, 2, selection.Account.ID)
						require.True(t, selection.Acquired)
						require.Nil(t, selection.WaitPlan)
						require.Empty(t, got.body)
						release()
					} else {
						require.Equal(t, openAISlotAcquireFailed, result)
						require.Nil(t, release)
						require.Equal(t, 429, got.status)
						if !on && !free {
							baseline = got
						} else {
							require.Equal(t, baseline, got, "no immediate alternative preserves exact legacy error and timing")
						}
					}
					if on {
						require.Equal(t, 1, calls)
					} else {
						require.Zero(t, calls)
					}
				})
			})
		}
	}
}
