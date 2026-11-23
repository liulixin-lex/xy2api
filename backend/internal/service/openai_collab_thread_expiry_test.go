//go:build unit

package service

// Binding scenarios here exercise unfinished code through the test-only hook.
// They do not establish equivalence or enable thread bindings in this release.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

var collabSetupContext context.Context

func TestCollabThreadBindingPreparationAllocations(t *testing.T) {
	for migration := range 4 {
		for _, legacy := range []string{"", "legacy"} {
			if migration == 0 && legacy == "" {
				continue
			}
			f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1))
			f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = migration&1 != 0
			f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = migration&2 != 0
			local := resilience.NewCollabTreeAffinity(32, time.Hour)
			base := withOpenAILegacySessionHash(withOpenAINoStickyMigration(selectionReviewContext()), legacy)
			identity := collabAffinityIdentity("child", "root")
			var counts []float64
			for _, on := range []bool{false, true} {
				settings := collabAffinitySettings()
				settings.Collab.TreeAffinity = on
				counts = append(counts, testing.AllocsPerRun(100, func() {
					ctx := WithOpenAICollabTreeAffinity(base, local, settings, 7, identity, resilience.CollabRoleChild, true, f.svc.cfg)
					if ctx != base || f.svc.PrepareOpenAICollabThreadBinding(ctx, 1, "v2:tree", PlatformOpenAI, OpenAIClientTransportHTTP, true) {
						panic("unexpected active binding or allocated context")
					}
					collabSetupContext = ctx
				}))
			}
			t.Logf("migration=%d legacy=%s preparation allocations off/on=%v", migration, legacy, counts)
			require.Equal(t, counts[0], counts[1])
			require.Zero(t, counts[1])
		}
	}
}

func RunCollabProvisionalExpiry(t *testing.T, cache GatewayCache, store ResilienceStore, leases SchedTrialLeases, reset func(), advance func(time.Duration), operations func() int64) {
	withResilienceSettings(t, nil)
	for _, history := range []string{"delete", "ciphertext_failure", "scheduler"} {
		t.Run("provisional_expiry/"+history, func(t *testing.T) {
			var outcomes []string
			var counts []int64
			for _, on := range []bool{false, true} {
				reset()
				f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
				f.svc.cache, f.svc.resilience.store, f.svc.selectionBoard.leases = cache, store, leases
				ctx, session := collabAtomicRequest(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), "expiry-child", on, false)
				primary := "openai:" + session
				require.NoError(t, cache.SetSessionAccountID(ctx, 7, primary, 1, time.Hour))
				if history == "scheduler" {
					// Production scheduler discovers that the inherited owner is disabled,
					// clears it and eagerly reserves a replacement before handler admission.
					f.repo.accounts[0].Schedulable = false
					first := selectTrialRuntime(t, f, ctx, session)
					require.EqualValues(t, 2, first.Account.ID)
					t.Logf("on=%t scheduler pending=%t", on, pendingOpenAIStickyChange(ctx) != nil)
					first.ReleaseFunc()
					f.repo.accounts[0].Schedulable = true
				} else if history == "delete" {
					require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(ctx, ptrInt64Selection(7), session, 1))
				} else {
					f.svc.AvoidOpenAIStateAccount(ctx, 7, session, 1)
				}
				if history != "scheduler" {
					require.NoError(t, f.svc.setStickySessionAccountID(ctx, ptrInt64Selection(7), session, 2, time.Hour))
				}
				effective := primary
				if b := openAICollabBindingFrom(ctx); b != nil {
					effective = b.key
				}
				claimed, ttl, err := cache.(gatewaySessionBindingReader).GetSessionAccountIDWithTTL(ctx, 7, effective)
				require.NoError(t, err)
				require.EqualValues(t, 2, claimed)
				require.LessOrEqual(t, ttl, 30*time.Second)
				if history != "ciphertext_failure" {
					advance(31 * time.Second)
				} else {
					advance(11 * time.Minute)
				}
				// Fresh affinity store models a different instance/restart, with no local avoidance.
				retry, _ := collabAtomicRequest(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), "expiry-child", on, false)
				owner, missing, lookupErr := f.svc.lookupStickySessionAccount(retry, ptrInt64Selection(7), session)
				f.loads.acquireResults = map[int64]bool{1: false, 2: true}
				retry, cancel := context.WithTimeout(retry, 500*time.Millisecond)
				before := operations()
				start := time.Now()
				result, _, selectErr := f.svc.SelectAccountWithSchedulerForCapability(retry, ptrInt64Selection(7), "", session, "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
				elapsed := time.Since(start)
				count := operations() - before
				cancel()
				outcome := fmt.Sprintf("err=%v", selectErr)
				if result != nil {
					outcome += fmt.Sprintf(" account=%d acquired=%t wait=%v", result.Account.ID, result.Acquired, result.WaitPlan)
					if result.ReleaseFunc != nil {
						result.ReleaseFunc()
					}
				}
				outcomes = append(outcomes, outcome)
				counts = append(counts, count)
				t.Logf("on=%t expiredClaimTTL=%s effectiveOwner=%d missing=%t lookupErr=%v operations=%d elapsed=%s %s", on, ttl, owner, missing, lookupErr, count, elapsed, outcome)
			}
			require.Equal(t, outcomes[0], outcomes[1])
			require.Equal(t, counts[0], counts[1], "selection storage operations")
		})
	}
	t.Run("inflight_migration_flip", func(t *testing.T) {
		reset()
		f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
		f.svc.cache, f.svc.resilience.store, f.svc.selectionBoard.leases = cache, store, leases
		ctx, session := collabAtomicRequest(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), "flip-child", true, false)
		b := openAICollabBindingFrom(ctx)
		require.NotNil(t, b)
		require.NoError(t, cache.SetSessionAccountID(ctx, 7, "openai:"+session, 1, time.Hour))
		require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(ctx, ptrInt64Selection(7), session, 1))
		f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = true
		f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = true
		require.True(t, f.svc.enableUnshippedCollabThreadBinding(ctx, 1, session, PlatformOpenAI, OpenAIClientTransportHTTP, true))
		result := selectTrialRuntime(t, f, ctx, session)
		CompleteOpenAIStickyChange(ctx, result.Account.ID, true, false)
		result.ReleaseFunc()
		parent, err := cache.GetSessionAccountID(ctx, 7, "openai:"+session)
		require.NoError(t, err)
		require.EqualValues(t, 1, parent)
		require.Same(t, b, openAICollabBindingFrom(ctx))
	})
}
