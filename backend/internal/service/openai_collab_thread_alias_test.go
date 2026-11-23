//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// RunCollabAliasParity exercises aliases through the repository cache, including
// the scheduler and trial cleanup after storage has changed on another instance.
func RunCollabAliasParity(t *testing.T, cache GatewayCache, store ResilienceStore, leases SchedTrialLeases, reset func(), advance func(time.Duration), members func(int64) []string, operations func() int64) {
	withResilienceSettings(t, nil)
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, scenario := range []string{"claim_ttl", "claim_deadline", "legacy_only", "late_alias"} {
		t.Run(scenario, func(t *testing.T) {
			var outcomes []string
			var counts []int64
			for _, on := range []bool{false, true} {
				reset()
				f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
				f.svc.cache, f.svc.resilience.store = cache, store
				if scenario == "late_alias" {
					selectionSetTrial(f, 1, newSelectionTrialLeaseFake())
				}
				f.svc.selectionBoard.leases = leases
				f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = scenario != "late_alias"
				f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = true
				ctx, session := collabAtomicRequest(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), "alias-child", on, false, "alias-parent")
				primary, legacy := "openai:"+session, "openai:alias-parent"
				var remaining time.Duration
				switch scenario {
				case "claim_ttl", "claim_deadline":
					require.NoError(t, cache.SetSessionAccountID(ctx, 7, primary, 2, 30*time.Second))
					require.NoError(t, cache.SetSessionAccountID(ctx, 7, legacy, 2, 30*time.Second))
					before := operations()
					require.NoError(t, f.svc.setStickySessionAccountID(ctx, ptrInt64Selection(7), session, 2, time.Hour))
					counts = append(counts, operations()-before)
					_, remaining, _ = cache.(gatewaySessionBindingReader).GetSessionAccountIDWithTTL(ctx, 7, legacy)
					advance(31 * time.Second)
					f.loads.acquireResults = map[int64]bool{1: true, 2: false}
				case "legacy_only":
					require.NoError(t, cache.SetSessionAccountID(ctx, 7, legacy, 2, time.Minute))
					before := operations()
					owner, _, err := f.svc.lookupStickySessionAccount(ctx, ptrInt64Selection(7), session)
					require.NoError(t, err)
					require.EqualValues(t, 2, owner)
					require.NoError(t, f.svc.refreshStickySessionTTL(ctx, ptrInt64Selection(7), session, time.Hour, 2))
					counts = append(counts, operations()-before)
					require.Nil(t, openAICollabBindingFrom(ctx), "legacy-only lookup excludes the rest of this request")
					_, remaining, _ = cache.(gatewaySessionBindingReader).GetSessionAccountIDWithTTL(ctx, 7, primary)
					advance(11 * time.Minute)
				case "late_alias":
					before := operations()
					result := selectTrialRuntime(t, f, ctx, session)
					selectedOps := operations() - before
					require.NoError(t, cache.SetSessionAccountID(ctx, 7, legacy, 2, time.Hour))
					before = operations()
					CompleteOpenAIStickyChange(ctx, result.Account.ID, true, false)
					result.ReleaseFunc()
					counts = append(counts, selectedOps+operations()-before)
				}
				if scenario == "late_alias" {
					owner, missing, err := f.svc.lookupStickySessionAccount(ctx, ptrInt64Selection(7), session)
					outcomes = append(outcomes, fmt.Sprintf("owner=%d missing=%t err=%v seats=%d", owner, missing, err, len(members(1))))
				} else {
					retry, _ := collabAtomicRequest(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), "alias-child", on, false, "alias-parent")
					if scenario == "claim_deadline" {
						var cancel context.CancelFunc
						retry, cancel = context.WithTimeout(retry, 500*time.Millisecond)
						defer cancel()
					}
					start := time.Now()
					result, _, err := f.svc.SelectAccountWithSchedulerForCapability(retry, ptrInt64Selection(7), "", session, "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
					t.Logf("on=%t ttl=%s selection elapsed=%s error=%v", on, remaining, time.Since(start), err)
					outcome := fmt.Sprintf("ttl=%d error=%v", remaining/time.Second, err)
					if result != nil {
						outcome += fmt.Sprintf(" selected=%d acquired=%t wait=%v", result.Account.ID, result.Acquired, result.WaitPlan)
						if result.ReleaseFunc != nil {
							result.ReleaseFunc()
						}
					}
					outcomes = append(outcomes, outcome)
				}
			}
			require.Equal(t, outcomes[0], outcomes[1])
			require.Equal(t, counts[0], counts[1], "shared operation counts")
			t.Logf("tree and thread operations=%d", counts[0])
		})
	}
	t.Run("generated_alias_oracle", func(t *testing.T) {
		for _, seed := range []int64{43, 907} {
			random := rand.New(rand.NewSource(seed))
			for i := range 60 {
				legacyOnly, fallback := i%2 == 0, random.Intn(2) == 0
				owner := int64(1 + random.Intn(3))
				ttl := time.Duration(10+random.Intn(50)) * time.Second
				priorities := []int{random.Intn(3), random.Intn(3), random.Intn(3)}
				var outcomes []string
				var counts []int64
				for _, on := range []bool{false, true} {
					reset()
					f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, priorities[0]), selectionFixtureAccount(2, priorities[1]), selectionFixtureAccount(3, priorities[2]))
					f.svc.cache, f.svc.resilience.store = cache, store
					for id := int64(1); id <= 3; id++ {
						selectionSetTrial(f, id, newSelectionTrialLeaseFake())
					}
					f.svc.selectionBoard.leases = leases
					f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = fallback
					f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = true
					ctx, session := collabAtomicRequest(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), "generated-child", on, false, "generated-parent")
					primary, alias := "openai:"+session, "openai:generated-parent"
					if legacyOnly {
						require.NoError(t, cache.SetSessionAccountID(ctx, 7, alias, owner, ttl))
					}
					before := operations()
					result := selectTrialRuntime(t, f, ctx, session)
					count := operations() - before
					if legacyOnly {
						require.Nil(t, openAICollabBindingFrom(ctx))
					} else {
						require.NoError(t, cache.SetSessionAccountID(ctx, 7, alias, owner, ttl))
					}
					before = operations()
					CompleteOpenAIStickyChange(ctx, result.Account.ID, true, false)
					result.ReleaseFunc()
					counts = append(counts, count+operations()-before)
					bound, _, err := f.svc.lookupStickySessionAccount(ctx, ptrInt64Selection(7), session)
					key := primary
					if b := openAICollabBindingFrom(ctx); b != nil {
						key = b.key
					}
					stored, _, _ := cache.(gatewaySessionBindingReader).GetSessionAccountIDWithTTL(ctx, 7, key)
					legacyOwner, legacyTTL, _ := cache.(gatewaySessionBindingReader).GetSessionAccountIDWithTTL(ctx, 7, alias)
					seats := []int{len(members(1)), len(members(2)), len(members(3))}
					outcomes = append(outcomes, fmt.Sprintf("selected=%d wait=%v bound=%d stored=%d err=%v alias=%d ttl=%d seats=%v bypass=%t", result.Account.ID, result.WaitPlan, bound, stored, err, legacyOwner, legacyTTL/time.Second, seats, resilience.StateFrom(ctx).StickyBypassChangesBinding()))
				}
				require.Equal(t, outcomes[0], outcomes[1], "seed=%d case=%d legacyOnly=%t fallback=%t", seed, i, legacyOnly, fallback)
				require.Equal(t, counts[0], counts[1], "seed=%d case=%d operation count", seed, i)
			}
			t.Logf("seed=%d: zero differences in 60 generated alias cases", seed)
		}
	})
}
