//go:build unit

package service

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabAffinityHintRespectsQualityAndTrialRounds(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mode := range []string{"degraded", "full_trial", "trial_race", "trial_capacity", "owner_migration"} {
		t.Run(mode, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
			s := resilience.NewCollabTreeAffinity(32, time.Hour)
			settings := collabAffinitySettings()
			NoteOpenAICollabAdmission(collabAffinityContext(s, settings, "root", ""), 1, false)
			leases := newSelectionTrialLeaseFake()
			switch mode {
			case "degraded", "owner_migration":
				f.view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
				if mode == "owner_migration" {
					f.cache.sessionBindings["openai:shared-session"] = 1
				}
			default:
				selectionSetTrial(f, 1, leases)
				leases.full[1] = mode == "full_trial"
				leases.lose[1] = mode == "trial_race"
			}
			ctx := collabAffinityContext(s, settings, "child", "root")
			selected, _, err := collabAffinitySelect(f, ctx, "m", nil)
			require.NoError(t, err)
			require.NotNil(t, selected)
			defer selected.ReleaseFunc()
			if mode == "trial_capacity" {
				require.EqualValues(t, 1, selected.Account.ID)
				require.NotEmpty(t, leases.held, "an affinity hit must acquire trial capacity")
			} else {
				require.EqualValues(t, 2, selected.Account.ID)
			}
		})
	}
}

func TestCollabAffinityPreferenceOrdering(t *testing.T) {
	hint := selectionKey{ID: 1, Tier: 0, Priority: 2}
	ordinary := selectionKey{ID: 2, Tier: 0, Priority: 1}
	require.False(t, openAICollabSelectionLess(hint, hint, hint.ID))
	require.True(t, openAICollabSelectionLess(hint, ordinary, hint.ID))
	require.False(t, openAICollabSelectionLess(ordinary, hint, hint.ID))
	hint.Tier = 1
	require.False(t, openAICollabSelectionLess(hint, ordinary, hint.ID))
	require.True(t, openAICollabSelectionLess(ordinary, hint, hint.ID))
}

func TestCollabAffinityRecoveredRouteRemainsSoft(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mode := range []string{"busy", "degraded", "trial_full", "all_busy"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
				f.cache.sessionBindings["openai:shared-session"] = 2
				settings := collabAffinitySettings()
				local := resilience.NewCollabTreeAffinity(32, time.Hour)
				ctx := collabAffinityContext(local, settings, "child", "root")
				f.svc.AvoidOpenAICollabStateAccount(ctx, 7, "shared-session", 3, true)
				NoteOpenAICollabSuccess(ctx, 1, false)
				switch mode {
				case "busy", "all_busy":
					f.loads.acquireResults = map[int64]bool{1: false, 2: mode != "all_busy"}
				case "degraded":
					f.view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
				case "trial_full":
					leases := newSelectionTrialLeaseFake()
					leases.full[1] = true
					selectionSetTrial(f, 1, leases)
				}
				start := time.Now()
				baseline, _, baselineErr := collabAffinitySelect(f, selectionReviewContext(), "m", nil)
				baselineElapsed := time.Since(start)
				require.NoError(t, baselineErr)
				if baseline.ReleaseFunc != nil {
					baseline.ReleaseFunc()
				}
				start = time.Now()
				selected, _, err := collabAffinitySelect(f, collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), settings, "child", "root"), "m", nil)
				require.NoError(t, err)
				require.Equal(t, baselineElapsed, time.Since(start))
				require.Equal(t, baseline.Account.ID, selected.Account.ID)
				require.Equal(t, baseline.WaitPlan, selected.WaitPlan)
				require.Equal(t, baseline.Acquired, selected.Acquired)
				if selected.ReleaseFunc != nil {
					selected.ReleaseFunc()
				}
			})
		})
	}
}

type collabAffinityTTLCache struct {
	GatewayCache
	routes  map[string]int64
	expires map[string]time.Time
	ttl     time.Duration
}

func (c *collabAffinityTTLCache) SetSessionAccountID(_ context.Context, _ int64, key string, account int64, ttl time.Duration) error {
	c.routes[key], c.expires[key], c.ttl = account, time.Now().Add(ttl), ttl
	return nil
}

func (c *collabAffinityTTLCache) GetSessionAccountID(_ context.Context, _ int64, key string) (int64, error) {
	if time.Now().Before(c.expires[key]) {
		return c.routes[key], nil
	}
	return 0, ErrStickySessionNotFound
}

func TestCollabAffinitySharedExpiryAndReset(t *testing.T) {
	withResilienceSettings(t, nil)
	synctest.Test(t, func(t *testing.T) {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
		f.svc.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60
		cache := &collabAffinityTTLCache{GatewayCache: f.svc.cache, routes: make(map[string]int64), expires: make(map[string]time.Time)}
		f.svc.cache = cache
		settings := collabAffinitySettings()
		ctx := collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), settings, "child", "root")
		f.svc.AvoidOpenAICollabStateAccount(ctx, 7, "tree", 1, true)
		NoteOpenAICollabSuccess(ctx, 2, false)
		require.Equal(t, time.Minute, cache.ttl)
		read := func() openAICollabThreadRouting {
			ctx = f.svc.withOpenAICollabThreadRouting(ctx, "tree")
			routing, _ := ctx.Value(openAICollabThreadRoutingKey{}).(openAICollabThreadRouting)
			return routing
		}
		require.Contains(t, read().avoided, int64(1))
		require.EqualValues(t, 2, read().route)
		time.Sleep(time.Second)
		reset := time.Now()
		f.svc.resilience.sessionResetAt = func(int64) time.Time { return reset }
		require.Empty(t, read().avoided, "local state must not revive a shared reset")
		time.Sleep(59 * time.Second)
		require.Zero(t, read().route, "local state must not revive the expired shared binding")
		// New avoidance retains today's configured TTL, independently of hints.
		f.svc.AvoidOpenAICollabStateAccount(ctx, 7, "tree", 1, true)
		ttl := time.Duration(resilience.Current().Session.AvoidSeconds) * time.Second
		time.Sleep(ttl - time.Nanosecond)
		require.Contains(t, read().avoided, int64(1))
		time.Sleep(time.Nanosecond)
		require.Empty(t, read().avoided)
	})
}

func TestCollabAffinityRecoverySurvivesAnotherInstance(t *testing.T) {
	withResilienceSettings(t, nil)
	first := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 3), selectionFixtureAccount(3, 2))
	settings := collabAffinitySettings()
	local := resilience.NewCollabTreeAffinity(32, time.Hour)
	ctx := collabAffinityContext(local, settings, "child", "root")
	first.svc.AvoidOpenAICollabStateAccount(ctx, 7, "shared-session", 1, true)
	other := newSelectionRuntimeFixture(t, first.repo.accounts...)
	other.svc.cache, other.svc.resilience = first.svc.cache, first.svc.resilience
	// No local hints or legacy execution scope are carried to the other instance.
	identity := collabAffinityIdentity("child", "root")
	identity.Scope.Tree = "" // a restarted instance has no membership registry
	retry := WithOpenAICollabTreeAffinity(selectionReviewContext(), resilience.NewCollabTreeAffinity(32, time.Hour), settings, 7, identity, resilience.CollabRoleChild, true)
	selected, _, err := collabAffinitySelect(other, retry, "m", nil)
	require.NoError(t, err)
	require.NotEqualValues(t, 1, selected.Account.ID, "shared avoidance must be read immediately")
	selected.ReleaseFunc()
	NoteOpenAICollabSuccess(ctx, 2, false)
	third := newSelectionRuntimeFixture(t, first.repo.accounts...)
	third.svc.cache, third.svc.resilience = first.svc.cache, first.svc.resilience
	retry = WithOpenAICollabTreeAffinity(selectionReviewContext(), resilience.NewCollabTreeAffinity(32, time.Hour), settings, 7, identity, resilience.CollabRoleChild, true)
	selected, _, err = collabAffinitySelect(third, retry, "m", nil)
	require.NoError(t, err)
	require.EqualValues(t, 2, selected.Account.ID, "shared recovery route must outlive the local store")
	selected.ReleaseFunc()
}

func TestCollabAffinityAvoidancePreservesTreeAliases(t *testing.T) {
	withResilienceSettings(t, nil)
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	f.cache.sessionBindings["openai:v2:tree"] = 1
	f.cache.sessionBindings["openai:legacy-tree"] = 1
	ctx := collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root")
	ctx = withOpenAILegacySessionHash(ctx, "legacy-tree")
	f.svc.AvoidOpenAICollabStateAccount(ctx, 7, "v2:tree", 1, true)
	require.EqualValues(t, 1, f.cache.sessionBindings["openai:v2:tree"])
	require.EqualValues(t, 1, f.cache.sessionBindings["openai:legacy-tree"], "thread rejection must not call any binding deletion")
}

func TestCollabAffinityRejectsContradictoryTreeIdentity(t *testing.T) {
	for _, member := range []string{"self", "parent"} {
		t.Run(member, func(t *testing.T) {
			s := resilience.NewCollabTreeAffinity(32, time.Hour)
			settings := collabAffinitySettings()
			root := collabAffinityContext(s, settings, "root", "")
			NoteOpenAICollabAdmission(root, 1, false)
			if member == "self" {
				NoteOpenAICollabAdmission(collabAffinityContext(s, settings, "child", "root"), 1, false)
			}
			identity := collabAffinityIdentity("child", "root")
			identity.Scope.Tree = "contradictory-tree"
			base := selectionReviewContext()
			ctx := WithOpenAICollabTreeAffinity(base, s, settings, 7, identity, resilience.CollabRoleChild, true)
			require.True(t, base == ctx, "only an empty tree may be resolved through membership")
		})
	}
}
