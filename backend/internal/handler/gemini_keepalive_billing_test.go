//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type geminiKeepaliveBillingAccounts struct {
	service.AccountRepository
	snapshot *fakeSchedulerCache
}

func (r *geminiKeepaliveBillingAccounts) ListByGroup(context.Context, int64) ([]service.Account, error) {
	accounts := make([]service.Account, 0, len(r.snapshot.accounts))
	for _, a := range r.snapshot.accounts {
		accounts = append(accounts, *a)
	}
	return accounts, nil
}

type geminiKeepaliveSettlement struct {
	input, output, cacheRead, cacheWrite int
	balance, keyQuota, accountQuota      float64
}

// Observe every call without deduplicating: duplicate handler submissions must
// fail this regression even if a database would deduplicate their request IDs.
type geminiKeepaliveBillingRepo struct {
	service.UsageBillingRepository
	mu       sync.Mutex
	commands []*service.UsageBillingCommand
	states   []*resilience.RequestState
	total    geminiKeepaliveSettlement
}

func (r *geminiKeepaliveBillingRepo) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, cmd)
	r.states = append(r.states, resilience.StateFrom(ctx))
	r.total.input += cmd.InputTokens
	r.total.output += cmd.OutputTokens
	r.total.cacheRead += cmd.CacheReadTokens
	r.total.cacheWrite += cmd.CacheCreationTokens
	r.total.balance += cmd.BalanceCost
	r.total.keyQuota += cmd.APIKeyQuotaCost
	r.total.accountQuota += cmd.AccountQuotaCost
	// Model the usage row committed atomically with the debit.
	cmd.Usage.ID = int64(len(r.commands))
	return &service.UsageBillingApplyResult{Applied: true, CachesManagedDurably: true}, nil
}

func (r *geminiKeepaliveBillingRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.commands)
}

// One paired request trajectory exercises the real native handler, failover,
// delivery accounting, durable usage task and production Apply billing branch.
func TestGeminiCommittedKeepaliveRequestBillsOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var withoutHeartbeat geminiKeepaliveSettlement
	for _, heartbeat := range []bool{false, true} {
		t.Run(map[bool]string{false: "without_heartbeat", true: "committed_heartbeat"}[heartbeat], func(t *testing.T) {
			withHandlerResilience(t, func(s *resilience.Settings) {
				s.Keepalive.Enabled = heartbeat
				s.Keepalive.DelaySeconds, s.Keepalive.IntervalSeconds = 1, 10
			})
			cfg := &config.Config{RunMode: config.RunModeStandard}
			cfg.Default.RateMultiplier = 1
			cfg.Pricing.DataDir = t.TempDir()
			cfg.Pricing.UpdateIntervalHours = 24
			require.NoError(t, os.WriteFile(filepath.Join(cfg.Pricing.DataDir, "model_pricing.json"),
				[]byte(`{"gemini-2.5-flash":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`), 0600))
			pricing := service.NewPricingService(cfg, nil)
			require.NoError(t, pricing.Initialize())
			t.Cleanup(pricing.Stop)
			billing := service.NewBillingService(cfg, pricing)
			group := &service.Group{ID: 20, Hydrated: true, Platform: service.PlatformGemini, Status: service.StatusActive, RateMultiplier: 1.5}
			key := &service.APIKey{ID: 21, UserID: 22, GroupID: &group.ID, Group: group, Quota: 100,
				User: &service.User{ID: 22, Status: service.StatusActive, Balance: 100, Concurrency: 1}}
			account := func(id int64) *service.Account {
				return &service.Account{ID: id, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: int(id),
					Credentials: map[string]any{"api_key": "synthetic-key"}, Extra: map[string]any{"quota_limit": float64(100)},
					AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: group.ID}}}
			}
			first, second, spare := account(31), account(32), account(33)
			snapshot := &fakeSchedulerCache{accounts: []*service.Account{first}}
			accounts := &geminiKeepaliveBillingAccounts{snapshot: snapshot}
			runtime := service.NewResilienceService(nil, nil)
			runtime.ConfigureFallbackGroups(accounts, &fakeGroupRepo{group: group})
			previousRuntime := service.Resilience()
			service.SetResilienceInstance(runtime)
			t.Cleanup(func() { service.SetResilienceInstance(previousRuntime) })
			ledger := &geminiKeepaliveBillingRepo{}
			usage := &gwUsageLogRepo{}
			cache := service.NewBillingCacheService(newHandlerInflightCache(100), nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(cache.Stop)
			upstream := &collabWiringUpstream{}
			gateway := service.NewGatewayService(accounts, &fakeGroupRepo{group: group}, usage, ledger, nil, nil, nil,
				&handlerCompatStore{}, cfg, service.NewSchedulerSnapshotService(snapshot, nil, nil, nil, nil), nil,
				billing, nil, cache, nil, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
				service.NewModelPricingResolver(nil, billing), nil, nil, nil)
			pool := newUsageRecordTestPool(t)
			h := &GatewayHandler{
				gatewayService: gateway, cfg: cfg, billingCacheService: cache, usageRecordWorkerPool: pool,
				apiKeyService:            service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
				concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatNone, 0),
				maxAccountSwitchesGemini: 2,
				geminiCompatService:      service.NewGeminiMessagesCompatService(accounts, nil, nil, nil, &service.GeminiTokenProvider{}, nil, upstream, nil, cfg),
			}
			var requestState *resilience.RequestState
			synctest.Test(t, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				w := &lockedKeepaliveResponseWriter{ResponseWriter: c.Writer, recorder: recorder}
				c.Writer = w
				requestState = resilience.NewRequestState()
				requestState.SetGroup(group.ID)
				requestState.SetAPIKey(key.ID)
				requestState.SetEdge("cloudflare")
				requestState.NoteTurn(group.ID, "gemini-2.5-flash", "")
				ctx := resilience.WithRequestState(context.Background(), requestState)
				ctx = context.WithValue(ctx, ctxkey.Group, group)
				ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "synthetic-gemini-request")
				requestState.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
				c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse",
					strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)).WithContext(ctx)
				c.Params = gin.Params{{Key: "modelAction", Value: "/gemini-2.5-flash:streamGenerateContent"}}
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 1})
				service.InstallDeliveryTracking(c, time.Now())
				var sent []int64
				upstream.respond = func(req *http.Request, id int64) (*http.Response, error) {
					sent = append(sent, id)
					require.Zero(t, ledger.count(), "failed attempt and heartbeats must not submit billing")
					if len(sent) == 1 {
						time.Sleep(2 * time.Second)
						synctest.Wait()
						require.Equal(t, heartbeat, service.PreOutputKeepaliveCommitted(c))
						snapshot.accounts = []*service.Account{second, spare}
						return &http.Response{StatusCode: 408, Header: http.Header{"X-Request-Id": {"synthetic-failure"}},
							Body: io.NopCloser(strings.NewReader(`{"error":{"message":"timeout"}}`)), Request: req}, nil
					}
					require.Equal(t, second.ID, id)
					before := strings.Count(w.body(), ":\n\n")
					for i := 1; i <= 2; i++ {
						time.Sleep(10 * time.Second)
						synctest.Wait()
						expected := 0
						if heartbeat {
							expected = before + i
						}
						require.Equal(t, expected, strings.Count(w.body(), ":\n\n"), "second attempt must keep the committed heartbeat running")
						require.Zero(t, ledger.count(), "no settlement before successful output")
						require.Zero(t, requestState.DeliveredBytes(), "heartbeats are not delivered content")
						require.LessOrEqual(t, service.OpenAICompactKeepaliveAdjustedWrittenSize(c), 0)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"synthetic-success"}},
						Body: io.NopCloser(strings.NewReader("data: " + `{"candidates":[{"content":{"parts":[{"text":"hello world"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3}}` + "\n\n")), Request: req}, nil
				}
				h.GeminiV1BetaModels(c)
				synctest.Wait()
				require.Equal(t, []int64{first.ID, second.ID}, sent)
				require.Equal(t, 200, recorder.Code, w.body())
				require.Equal(t, 1, strings.Count(w.body(), "hello world"))
			})
			// Financial tasks currently run inline before handler return; drain
			// the configured real pool as well so deferred duplicates cannot hide.
			pool.Stop()
			cache.Stop()
			require.Zero(t, pool.Stats().WaitingTasks)
			require.Equal(t, pool.Stats().SubmittedTasks, pool.Stats().CompletedTasks)
			require.Equal(t, 1, ledger.count(), "exactly one production Apply call after all tasks finish")
			require.Same(t, requestState, ledger.states[0], "billing retains this request's delivery state")
			cmd := ledger.commands[0]
			require.Equal(t, second.ID, cmd.AccountID)
			require.Equal(t, "client:synthetic-gemini-request", cmd.RequestID)
			require.Positive(t, requestState.DeliveredBytes())
			require.Equal(t, service.PlatformGemini, cmd.QuotaPlatform)
			require.Equal(t, 7, cmd.Usage.InputTokens)
			require.Equal(t, 3, cmd.Usage.OutputTokens)
			require.Zero(t, cmd.SubscriptionCost)
			require.Zero(t, cmd.APIKeyRateLimitCost)
			require.Empty(t, usage.recorded(), "Apply committed the only usage row atomically")
			require.InDelta(t, 0.0000195, ledger.total.balance, 1e-12)
			require.InDelta(t, 0.0000195, ledger.total.keyQuota, 1e-12)
			require.InDelta(t, 0.000013, ledger.total.accountQuota, 1e-12)
			if heartbeat {
				require.Equal(t, withoutHeartbeat, ledger.total, "heartbeats and failover do not change customer usage, debit or quota increments")
			} else {
				withoutHeartbeat = ledger.total
			}
		})
	}
}
