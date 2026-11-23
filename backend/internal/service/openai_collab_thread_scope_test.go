//go:build unit

package service

// Binding scenarios here exercise unfinished code through the test-only hook.
// They do not establish equivalence or enable thread bindings in this release.

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabThreadBindingMigrationScope(t *testing.T) {
	for migration := range 4 {
		for _, legacy := range []string{"", "legacy"} {
			t.Run(fmt.Sprintf("migration=%d/legacy=%s", migration, legacy), func(t *testing.T) {
				f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1))
				f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = migration&1 != 0
				f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = migration&2 != 0
				cache := &collabSlowBindingCache{GatewayCache: f.svc.cache, fail: true}
				f.svc.cache = cache
				ctx := prepareAffinityTest(f, withOpenAILegacySessionHash(collabAffinityContext(resilience.NewCollabTreeAffinity(16, time.Hour), collabAffinitySettings(), "child", "root"), legacy), "v2:tree")
				active := migration == 0 && legacy == ""
				require.Equal(t, active, openAICollabBindingFrom(ctx) != nil)
				require.Zero(t, cache.reads.Load(), "eligibility must not read storage")
				if !active {
					a := ctx.Value(openAICollabAffinityKey{}).(*openAICollabAffinity)
					require.Nil(t, a.binding.Load(), "no thread identity is allocated")
					require.Nil(t, openAICollabAffinityFrom(ctx))
					require.IsType(t, f.svc.openAISessionRecorder(), f.svc.collabSessionRecorder(ctx))
				}
				// Configuration belongs to ingress. Later storage observations and
				// repeated preparation cannot change this request's scope.
				f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = !active
				f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = active
				require.Equal(t, active, f.svc.enableUnshippedCollabThreadBinding(ctx, 1, "v2:tree", PlatformOpenAI, OpenAIClientTransportHTTP, true))
			})
		}
	}
}

func TestCollabThreadBindingInactiveAllocations(t *testing.T) {
	withResilienceSettings(t, nil)
	for migration := range 4 {
		for _, legacy := range []string{"", "legacy"} {
			if migration == 0 && legacy == "" {
				continue
			}
			var counts []float64
			for _, on := range []bool{false, true} {
				f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1))
				f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = migration&1 != 0
				f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = migration&2 != 0
				ctx, session := collabAtomicRequest(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), "allocation-child", on, false, legacy)
				require.Nil(t, openAICollabBindingFrom(ctx))
				f.cache.sessionBindings["openai:"+session] = 1
				counts = append(counts, testing.AllocsPerRun(20, func() {
					request := resilience.WithRequestState(ctx, resilience.NewRequestState())
					result := selectTrialRuntime(t, f, request, session)
					CompleteOpenAIStickyChange(request, result.Account.ID, true, false)
					result.ReleaseFunc()
				}))
			}
			require.Equal(t, counts[0], counts[1], "migration=%d legacy=%s", migration, legacy)
			t.Logf("migration=%d legacy=%s selection/completion allocations=%v", migration, legacy, counts)
		}
	}
}

func testCollabLegacyDirectWriters(t *testing.T) { collabLegacyEffectsParity(t, "direct") }
func testCollabLegacyRefresh(t *testing.T)       { collabLegacyEffectsParity(t, "refresh") }
func testCollabLegacyPromotion(t *testing.T)     { collabLegacyEffectsParity(t, "promote") }
func testCollabLegacyAvoidance(t *testing.T)     { collabLegacyEffectsParity(t, "avoid") }

// Preserve the former alias fixtures as ordinary-path comparisons, including
// detached writers. Legacy context alone excludes binding even with flags off.
func collabLegacyEffectsParity(t *testing.T, action string) {
	withResilienceSettings(t, nil)
	for migration := range 4 {
		synctest.Test(t, func(t *testing.T) {
			outcome := func(on bool) string {
				f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
				f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = migration&1 != 0
				f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = migration&2 != 0
				cache := &collabEffectsCache{GatewayCache: f.svc.cache, values: map[string]int64{}, until: map[string]time.Time{}}
				f.svc.cache = cache
				settings := collabAffinitySettings()
				settings.Collab.TreeAffinity = on
				ctx := prepareAffinityTest(f, withOpenAILegacySessionHash(collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), settings, "child", "root"), "legacy"), "v2:tree")
				require.Nil(t, openAICollabBindingFrom(ctx))
				for _, key := range []string{"openai:v2:tree", "openai:legacy"} {
					require.NoError(t, cache.SetSessionAccountID(ctx, 7, key, 1, time.Minute))
				}
				time.Sleep(10 * time.Second)
				switch action {
				case "direct":
					require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(ctx, ptrInt64Selection(7), "v2:tree", 2))
					require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(ctx, ptrInt64Selection(7), "v2:tree", 1))
					require.NoError(t, f.svc.setStickySessionAccountID(ctx, ptrInt64Selection(7), "v2:tree", 2, time.Hour))
					rec := f.svc.collabSessionRecorder(ctx)
					for range resilience.Current().Session.StreakThreshold {
						rec.Failure(context.Background(), 7, "v2:tree", 2, resilience.CategoryTransient)
					}
				case "refresh":
					require.NoError(t, f.svc.refreshStickySessionTTL(ctx, ptrInt64Selection(7), "v2:tree", time.Hour, 1))
				case "promote":
					ctx = context.WithValue(ctx, openAIStickyPromotionKey{}, true)
					require.NoError(t, f.svc.setStickySessionAccountID(ctx, ptrInt64Selection(7), "v2:tree", 1, time.Hour))
				case "avoid":
					f.svc.AvoidOpenAICollabStateAccount(ctx, 7, "v2:tree", 1, true)
				}
				tree, te := cache.GetSessionAccountID(ctx, 7, "openai:v2:tree")
				alias, ae := cache.GetSessionAccountID(ctx, 7, "openai:legacy")
				avoid, err := f.svc.resilience.store.SessionAvoid(ctx, 7, "v2:tree")
				require.NoError(t, err)
				return fmt.Sprintf("tree=%d/%v/%v alias=%d/%v/%v avoid=%v bypass=%t", tree, te, time.Until(cache.until["openai:v2:tree"]), alias, ae, time.Until(cache.until["openai:legacy"]), avoid, resilience.StateFrom(ctx).StickyBypassChangesBinding())
			}
			require.Equal(t, outcome(false), outcome(true), "migration=%d action=%s", migration, action)
		})
	}
}
