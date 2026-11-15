//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// selectionVersionedCache is the production binding contract on the selection
// test cache: selection only plans, and a successful delivery commits.
type selectionVersionedCache struct{ *selectionTestCache }

func (c selectionVersionedCache) GetSessionBinding(ctx context.Context, group int64, key string) (StickySessionBinding, error) {
	id, err := c.GetSessionAccountID(ctx, group, key)
	if err != nil {
		return StickySessionBinding{}, err
	}
	return StickySessionBinding{AccountID: id, Generation: "g1"}, nil
}

func (c selectionVersionedCache) CommitSessionBinding(_ context.Context, _ int64, key string, expected StickySessionBinding, target int64, _ time.Duration, _ ...string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessionBindings[key] != expected.AccountID {
		return false, nil
	}
	c.sessionBindings[key] = target
	return true, nil
}

func (c selectionVersionedCache) RecordSessionBindingFailure(context.Context, int64, string, string, StickySessionBinding, int64, time.Duration, ...time.Time) (bool, error) {
	return false, nil
}

func (e *openAISelectionTestEnv) useVersionedBindings() {
	e.svc.cache = selectionVersionedCache{e.cache}
}

// deliverSelection plays a complete successful delivery on the selected account.
func deliverSelection(ctx context.Context, selection *AccountSelectionResult) {
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
	CompleteOpenAIStickyChange(ctx, selection.Account.ID, true, false)
}

// RC-4: an ordinary placement elsewhere replaces the owner only where the
// eager path deleted the owner's binding (hard unavailable) or for a new
// session; every other bypass serves this request and keeps the owner.
func TestSelectionStickyChangeKeepsOwnerUnlessReleased(t *testing.T) {
	const key = "openai:" + lrSession
	gid := selectionTestGroupID
	selectWith := func(t *testing.T, env *openAISelectionTestEnv, ctx context.Context) *AccountSelectionResult {
		t.Helper()
		sel, _, err := env.svc.SelectAccountWithScheduler(ctx, &gid, "", lrSession, "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.NotNil(t, sel)
		return sel
	}
	healthyB := func(env *openAISelectionTestEnv) {
		b := env.account(lrAccountB)
		b.TempUnschedulableUntil, b.TempUnschedulableReason = nil, ""
		b.RateLimitResetAt, b.OverloadUntil = nil, nil
	}

	t.Run("soft_owner_preserved", func(t *testing.T) {
		env := newOpenAILastResortEnv(t, 5, 1)
		env.useVersionedBindings()
		healthyB(env)
		env.cache.bind(key, lrAccountA, time.Hour-time.Minute)
		st := resilience.NewRequestState()
		ctx := resilience.WithRequestState(context.Background(), st)
		sel := selectWith(t, env, ctx)
		require.Equal(t, lrAccountB, sel.Account.ID)
		require.Equal(t, "soft_owner_preserved", st.StickyBypassReason())
		deliverSelection(ctx, sel)
		require.Equal(t, lrAccountA, env.cache.sessionBindings[key], "a success elsewhere does not move a soft-penalised owner")
	})

	t.Run("last_resort", func(t *testing.T) {
		env := newOpenAILastResortEnv(t, 5, 1)
		env.useVersionedBindings()
		env.cache.bind(key, lrAccountA, time.Hour-time.Minute)
		st := resilience.NewRequestState()
		ctx := resilience.WithRequestState(context.Background(), st)
		sel := selectWith(t, env, ctx)
		require.Equal(t, lrAccountB, sel.Account.ID)
		require.Equal(t, lrAccountB, st.LastResort())
		deliverSelection(ctx, sel)
		require.Equal(t, lrAccountA, env.cache.sessionBindings[key])
	})

	t.Run("owner_excluded_in_request", func(t *testing.T) {
		env := newOpenAILastResortEnv(t, 1, 5)
		env.useVersionedBindings()
		healthyB(env)
		a := env.account(lrAccountA)
		a.TempUnschedulableUntil, a.TempUnschedulableReason = nil, ""
		env.cache.bind(key, lrAccountA, time.Hour-time.Minute)
		ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		sel, _, err := env.svc.SelectAccountWithScheduler(ctx, &gid, "", lrSession, "gpt-5.1", map[int64]struct{}{lrAccountA: {}}, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.Equal(t, lrAccountB, sel.Account.ID)
		deliverSelection(ctx, sel)
		require.Equal(t, lrAccountA, env.cache.sessionBindings[key], "a retry elsewhere within one request keeps the owner")
	})

	t.Run("owner_hard_unavailable", func(t *testing.T) {
		env := newOpenAILastResortEnv(t, 5, 1)
		env.useVersionedBindings()
		healthyB(env)
		env.account(lrAccountA).Schedulable = false
		env.cache.bind(key, lrAccountA, time.Hour-time.Minute)
		ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		sel := selectWith(t, env, ctx)
		require.Equal(t, lrAccountB, sel.Account.ID)
		require.Equal(t, lrAccountA, env.cache.sessionBindings[key], "selection never writes")
		deliverSelection(ctx, sel)
		require.Equal(t, lrAccountB, env.cache.sessionBindings[key], "a released owner is replaced after success")
	})

	t.Run("new_session", func(t *testing.T) {
		env := newOpenAILastResortEnv(t, 5, 1)
		env.useVersionedBindings()
		healthyB(env)
		ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		sel := selectWith(t, env, ctx)
		require.Equal(t, lrAccountB, sel.Account.ID)
		deliverSelection(ctx, sel)
		require.Equal(t, lrAccountB, env.cache.sessionBindings[key])
	})

	t.Run("last_resort_without_soft_owner", func(t *testing.T) {
		f := selectionNewSessionFixture()
		state := resilience.NewRequestState()
		ctx := resilience.WithRequestState(context.Background(), state)
		selected := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
		require.NotNil(t, selected)
		defer selected.ReleaseFunc()
		state.SetLastResort(selected.Account.ID)
		deliverSelection(ctx, selected)
		require.Equal(t, int64(10), f.binding.owner, "last-resort service must not establish session ownership")
		require.Empty(t, f.binding.casKeys)
	})
}

// RC-4: a child thread that avoids accounts for its own rejection must not
// bind the shared root session, as the eager path skipped that write.
func TestSelectionStickyChangeChildRejectionKeepsSharedSession(t *testing.T) {
	withResilienceSettings(t, nil)
	env := newOpenAISelectionTestEnv(t, twoAccountSelectionSetup(), 1, 2)
	env.svc.resilience = NewResilienceService(nil, newMemResilienceStore())
	env.useVersionedBindings()
	const session = "root-thread"
	c, _ := newOpenAIUpstreamErrorTestContext(t)
	c.Request.Header.Set("session-id", session)
	c.Request.Header.Set(openAIWSThreadIDHeader, "child-thread")
	c.Request.Header.Set(codexParentThreadIDHeader, session)
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	ctx = env.svc.WithOpenAIGuardianParentAffinity(ctx, c, []byte(`{"model":"gpt-5.1","input":"turn"}`), "gpt-5.1")
	env.svc.AvoidOpenAIStateAccount(ctx, selectionTestGroupID, openAIRejectionScope(ctx).session, 1)
	result, _ := env.selectAccount(t, ctx, "", session, nil)
	require.Equal(t, int64(2), result.Account.ID)
	require.True(t, preserveOpenAIRejectionBinding(ctx, session), "the shared owner is unknown")
	deliverSelection(ctx, result)
	_, bound := env.cache.sessionBindings["openai:"+session]
	require.False(t, bound, "the child's placement does not become the shared session's owner")
}
