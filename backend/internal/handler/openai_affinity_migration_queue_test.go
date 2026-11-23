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

// Count the same scalar/alias operations used by the gateway cache. The fake
// lets the original 120-second queue timeout run under synctest's clock.
type migrationQueueBindings struct {
	service.GatewayCache
	values map[string]int64
	calls  []string
}

func (c *migrationQueueBindings) GetSessionAccountID(_ context.Context, _ int64, key string) (int64, error) {
	c.calls = append(c.calls, "get")
	if owner := c.values[key]; owner > 0 {
		return owner, nil
	}
	return 0, service.ErrStickySessionNotFound
}
func (c *migrationQueueBindings) SetSessionAccountID(_ context.Context, _ int64, key string, owner int64, _ time.Duration) error {
	c.calls = append(c.calls, "set")
	c.values[key] = owner
	return nil
}
func (c *migrationQueueBindings) RefreshSessionTTL(_ context.Context, _ int64, _ string, _ time.Duration) error {
	c.calls = append(c.calls, "refresh")
	return nil
}
func (c *migrationQueueBindings) DeleteSessionAccountID(_ context.Context, _ int64, key string) error {
	c.calls = append(c.calls, "delete")
	delete(c.values, key)
	return nil
}
func (c *migrationQueueBindings) ResolveSessionBinding(_ context.Context, _ int64, primary, legacy string, fallback bool) (service.StickySessionBinding, error) {
	c.calls = append(c.calls, "resolve")
	owner := c.values[primary]
	if owner == 0 && fallback {
		owner = c.values[legacy]
		if owner > 0 {
			c.values[primary] = owner
		}
	}
	if owner == 0 {
		return service.StickySessionBinding{BothKeysMissing: c.values[legacy] == 0}, service.ErrStickySessionNotFound
	}
	if c.values[legacy] > 0 {
		c.values[legacy] = owner
	}
	return service.StickySessionBinding{AccountID: owner, LegacyKey: legacy}, nil
}
func (c *migrationQueueBindings) SyncSessionBindingAlias(_ context.Context, _ int64, primary, legacy string, owner int64, _ time.Duration, create bool) error {
	c.calls = append(c.calls, "sync")
	current := c.values[primary]
	if current > 0 && (owner == 0 || current == owner) && (create || c.values[legacy] > 0) {
		c.values[legacy] = current
	}
	return nil
}
func (c *migrationQueueBindings) DeleteSessionBindingAliases(_ context.Context, _ int64, primary, legacy string, owner int64) (bool, error) {
	c.calls = append(c.calls, "delete_aliases")
	current := c.values[primary]
	if current == 0 {
		current = c.values[legacy]
	}
	if owner != 0 && current != owner {
		return false, nil
	}
	delete(c.values, primary)
	delete(c.values, legacy)
	return current > 0, nil
}

func migrationQueueAffinity(ctx context.Context, on bool) context.Context {
	settings := resilience.Defaults()
	settings.Collab.TreeAffinity = on
	return service.WithOpenAICollabTreeAffinity(ctx, resilience.NewCollabTreeAffinity(16, time.Hour), &settings, 0, resilience.CollabTreeHint{Scope: resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}, Thread: "child", Parent: "root"}, resilience.CollabRoleChild, true)
}

func TestCollabAffinityMigrationQueueOperations(t *testing.T) {
	withHandlerResilience(t, nil)
	type outcome struct {
		status      int
		body, retry string
		elapsed     time.Duration
		selected    int64
	}
	for migration := range 4 {
		for _, legacy := range []bool{false, true} {
			for _, free := range []bool{false, true} {
				var ordinaryCalls []string
				var ordinaryOutcome, capacityOutcome outcome
				for _, path := range []string{"off", "ordinary", "affinity"} {
					t.Run(fmt.Sprintf("migration=%d/legacy=%t/free=%t/%s", migration, legacy, free, path), func(t *testing.T) {
						cfg := &config.Config{RunMode: config.RunModeSimple}
						cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = migration&1 != 0
						cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = migration&2 != 0
						cfg.Gateway.Scheduling.StickySessionWaitTimeout = 120 * time.Second
						cfg.Gateway.Scheduling.FallbackWaitTimeout = 120 * time.Second
						bindings := &migrationQueueBindings{values: map[string]int64{}}
						cache := &queueReselectionCache{freeB: false, attempts: map[int64]int{}}
						concurrency := service.NewConcurrencyService(cache)
						repo := &queueReselectionRepo{accounts: []service.Account{*profitSlotTestAccount(1, 1), *profitSlotTestAccount(2, 1)}}
						gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, bindings, cfg, nil, concurrency, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
						h := &OpenAIGatewayHandler{gatewayService: gateway, concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatClaude, 0)}
						synctest.Test(t, func(t *testing.T) {
							w := httptest.NewRecorder()
							c, _ := gin.CreateTestContext(w)
							c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
							c.Request.Header.Set("session_id", "queue-example")
							session := gateway.GenerateExplicitSessionHash(c, nil)
							ctx := context.Background()
							if legacy {
								ctx = c.Request.Context()
							}
							if path != "ordinary" {
								ctx = service.WithOpenAIEncryptedContentMigrationGuard(ctx, []byte(`{"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"synthetic"}]}]}`))
							}
							if path != "ordinary" {
								ctx = migrationQueueAffinity(ctx, path == "affinity")
							}
							c.Request = c.Request.WithContext(ctx)
							start := time.Now()
							selection, decision, err := gateway.SelectAccountWithScheduler(ctx, nil, "", session, "m", nil, service.OpenAIUpstreamTransportHTTPSSE, false)
							require.NoError(t, err)
							require.NotNil(t, selection.WaitPlan)
							require.EqualValues(t, 1, selection.Account.ID)
							cache.freeB = free

							selectNext := func(ctx context.Context, excluded map[int64]struct{}) (*service.AccountSelectionResult, service.OpenAIAccountScheduleDecision, error) {
								return gateway.SelectAccountWithScheduler(ctx, nil, "", session, "m", excluded, service.OpenAIUpstreamTransportHTTPSSE, false)
							}
							started := false
							release, result := h.tryOneQueueReselection(c, nil, session, &selection, &decision, nil, false, selectNext, false, &started, zap.NewNop())
							got := outcome{w.Code, w.Body.String(), w.Header().Get("Retry-After"), time.Since(start), 0}
							require.Equal(t, 120*time.Second, got.elapsed)
							if path != "off" && free {
								require.Equal(t, openAISlotAcquireOK, result)
								require.EqualValues(t, 2, selection.Account.ID)
								got.selected = selection.Account.ID
								release()
							} else {
								require.Equal(t, openAISlotAcquireFailed, result)
								require.Equal(t, 429, got.status)
								require.Nil(t, release)
							}
							switch path {
							case "off":
								capacityOutcome = got
								require.NotEmpty(t, bindings.calls, "initial ordinary selection reads the session")
							case "ordinary":
								ordinaryCalls = bindings.calls
								ordinaryOutcome = got
								if !free {
									require.Equal(t, capacityOutcome, got)
								}
							case "affinity":
								require.Equal(t, ordinaryCalls, bindings.calls)
								require.Equal(t, ordinaryOutcome, got)
								if !free {
									require.Equal(t, capacityOutcome, got)
								}
							}
							t.Logf("path=%s operations=%d trace=%v status=%d selected=%d wait=%s", path, len(bindings.calls), bindings.calls, got.status, got.selected, got.elapsed)
						})
					})
				}
			}
		}
	}
}
