//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// RunCollabScopeRegressions exercises aliases arriving after preparation using
// real storage. Active requests have no legacy identity; excluded requests keep
// exactly the ordinary alias effects, including its lookup TTL clamp.
func RunCollabScopeRegressions(t *testing.T, cache GatewayCache, store ResilienceStore, leases SchedTrialLeases, reset func(), advance func(time.Duration), operations func() int64) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Session.StreakThreshold = 1 })
	for migration := range 4 {
		for _, scenario := range []string{"late_refresh", "lookup_ttl", "captured_recorder", "legacy_commit_fence"} {
			t.Run(fmt.Sprintf("migration=%d/%s", migration, scenario), func(t *testing.T) {
				var outcomes []string
				var counts []int64
				for _, on := range []bool{false, true} {
					reset()
					f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
					f.svc.cache, f.svc.resilience.store, f.svc.selectionBoard.leases = cache, store, leases
					f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = migration&1 != 0
					f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = migration&2 != 0
					legacy := ""
					if migration != 0 || scenario == "legacy_commit_fence" {
						legacy = "scope-parent"
					}
					before := operations()
					ctx, session := collabAtomicRequest(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), "scope-child", on, false, legacy)
					require.Equal(t, before, operations(), "preparation has no storage operations")
					b := openAICollabBindingFrom(ctx)
					primary, alias := "openai:"+session, "openai:scope-parent"
					if migration != 0 || legacy != "" {
						require.Nil(t, b)
					}
					var outcome string
					switch scenario {
					case "late_refresh":
						require.NoError(t, cache.SetSessionAccountID(ctx, 7, primary, 1, time.Minute))
						_, _, err := f.svc.lookupStickySessionAccount(ctx, ptrInt64Selection(7), session)
						require.NoError(t, err)
						require.NoError(t, cache.DeleteSessionAccountID(ctx, 7, primary))
						require.NoError(t, cache.SetSessionAccountID(ctx, 7, alias, 2, time.Minute))
						before = operations()
						require.NoError(t, f.svc.refreshStickySessionTTL(ctx, ptrInt64Selection(7), session, time.Hour, 1))
						counts = append(counts, operations()-before)
						f.loads.acquireResults = map[int64]bool{1: true, 2: false}
						retry, _ := collabAtomicRequest(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), "scope-child", on, false, legacy)
						retry, cancel := context.WithTimeout(retry, 500*time.Millisecond)
						result, _, err := f.svc.SelectAccountWithSchedulerForCapability(retry, ptrInt64Selection(7), "", session, "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
						cancel()
						require.NoError(t, err)
						require.EqualValues(t, 1, result.Account.ID)
						require.True(t, result.Acquired)
						result.ReleaseFunc()
						outcome = fmt.Sprintf("account=%d wait=%v", result.Account.ID, result.WaitPlan)
					case "lookup_ttl":
						require.NoError(t, f.svc.setStickySessionAccountID(ctx, ptrInt64Selection(7), session, 2, time.Hour))
						require.NoError(t, cache.SetSessionAccountID(ctx, 7, alias, 1, 10*time.Minute))
						before = operations()
						owner, _, err := f.svc.lookupStickySessionAccount(ctx, ptrInt64Selection(7), session)
						require.NoError(t, err)
						counts = append(counts, operations()-before)
						_, ttl, err := cache.(gatewaySessionBindingReader).GetSessionAccountIDWithTTL(ctx, 7, alias)
						require.NoError(t, err)
						if migration == 0 {
							require.Equal(t, 10*time.Minute, ttl)
						}
						advance(31 * time.Second)
						remaining, err := cache.GetSessionAccountID(ctx, 7, alias)
						outcome = fmt.Sprintf("owner=%d ttl=%v after=%d/%v", owner, ttl, remaining, err)
					case "captured_recorder":
						require.NoError(t, cache.SetSessionAccountID(ctx, 7, primary, 2, time.Minute))
						_, _, err := f.svc.lookupStickySessionAccount(ctx, ptrInt64Selection(7), session)
						require.NoError(t, err)
						rec := f.svc.collabSessionRecorder(ctx)
						require.NoError(t, cache.DeleteSessionAccountID(ctx, 7, primary))
						require.NoError(t, cache.SetSessionAccountID(ctx, 7, alias, 2, time.Minute))
						_, _, _ = f.svc.lookupStickySessionAccount(ctx, ptrInt64Selection(7), session)
						before = operations()
						rec.Failure(context.Background(), 7, session, 2, resilience.CategoryTransient)
						counts = append(counts, operations()-before)
						scope, other := session, openAIRejectionScope(ctx).session
						if b != nil {
							scope, other = other, scope
						}
						avoided, err := store.SessionAvoid(ctx, 7, scope)
						require.NoError(t, err)
						require.Contains(t, avoided, int64(2))
						unrelated, err := store.SessionAvoid(ctx, 7, other)
						require.NoError(t, err)
						require.Empty(t, unrelated)
						outcome = fmt.Sprintf("avoid=%d other=%d", len(avoided), len(unrelated))
					case "legacy_commit_fence":
						before = operations()
						result := selectTrialRuntime(t, f, ctx, session)
						selected := operations() - before
						require.NoError(t, cache.SetSessionAccountID(ctx, 7, alias, 2, time.Minute))
						before = operations()
						CompleteOpenAIStickyChange(ctx, result.Account.ID, true, false)
						counts = append(counts, selected+operations()-before)
						result.ReleaseFunc()
						owner, err := cache.GetSessionAccountID(ctx, 7, primary)
						outcome = fmt.Sprintf("selected=%d owner=%d err=%v", result.Account.ID, owner, err)
					}
					outcomes = append(outcomes, outcome)
				}
				require.Equal(t, outcomes[0], outcomes[1])
				require.Equal(t, counts[0], counts[1], "ordinary and enabled operation counts")
				t.Logf("operations=%d outcome=%s", counts[0], outcomes[0])
			})
		}
	}
}
