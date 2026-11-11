//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAITerminalRejectionShareChildAffinityWithoutAvoidance(t *testing.T) {
	testOpenAIChildShareAffinity(t, OpenAIClientTransportHTTP)
}

func TestOpenAITerminalRejectionShareAvoidancePreservesOnlyExcludedOwner(t *testing.T) {
	withResilienceSettings(t, nil)
	env := newOpenAISelectionTestEnv(t, twoAccountSelectionSetup(), 1, 2)
	env.svc.resilience = NewResilienceService(nil, newMemResilienceStore())
	const session = "root-thread"
	_, owner := openAISelectionTarget(t, session)
	env.cache.bind("openai:"+session, owner, 0)
	c, _ := newOpenAIUpstreamErrorTestContext(t)
	c.Request.Header.Set("session-id", session)
	c.Request.Header.Set(openAIWSThreadIDHeader, "child-thread")
	c.Request.Header.Set(codexParentThreadIDHeader, session)
	ctx := env.svc.WithOpenAIGuardianParentAffinity(context.Background(), c,
		[]byte(`{"model":"gpt-5.1","input":"turn"}`), "gpt-5.1")
	scope := openAIRejectionScope(ctx).session
	env.svc.AvoidOpenAIStateAccount(ctx, selectionTestGroupID, scope, owner)
	result, _ := env.selectAccount(t, ctx, "", session, nil)
	require.Equal(t, 3-owner, result.Account.ID)
	require.True(t, preserveOpenAIRejectionBinding(ctx, session))
	if result.ReleaseFunc != nil {
		result.ReleaseFunc()
	}
	group := selectionTestGroupID
	require.NoError(t, env.svc.BindStickySessionAfterProfitAdmission(ctx, &group, session, result.Account.ID))
	require.Equal(t, owner, env.cache.sessionBindings["openai:"+session])
	require.Empty(t, env.cache.replaced, "temporary child placement must not overwrite the owner")

	// The same scope's avoidance must not disable reads or writes for an
	// unrelated healthy owner, including a later selection on this context.
	const healthySession = "healthy-parent-binding"
	env.cache.bind("openai:"+healthySession, 3-owner, 0)
	result, decision := env.selectAccount(t, ctx, "", healthySession, nil)
	require.Equal(t, 3-owner, result.Account.ID)
	require.True(t, decision.StickySessionHit)
	require.False(t, preserveOpenAIRejectionBinding(ctx, healthySession))
	require.Contains(t, env.cache.refreshed, "openai:"+healthySession)
	if result.ReleaseFunc != nil {
		result.ReleaseFunc()
	}
}

func TestOpenAITerminalRejectionWSChildAffinityWithoutAvoidance(t *testing.T) {
	testOpenAIChildShareAffinity(t, OpenAIClientTransportWS)
}

func testOpenAIChildShareAffinity(t *testing.T, transport OpenAIClientTransport) {
	t.Helper()
	follows := 0
	const rounds = 12
	for range rounds {
		env := newOpenAISelectionTestEnv(t, twoAccountSelectionSetup(), 1, 2)
		_, owner := openAISelectionTarget(t, "root-thread")
		env.cache.bind("openai:root-thread", owner, 0)
		c, _ := newOpenAIUpstreamErrorTestContext(t)
		SetOpenAIClientTransport(c, transport)
		c.Request.Header.Set("session-id", "root-thread")
		c.Request.Header.Set(openAIWSThreadIDHeader, "child-thread")
		c.Request.Header.Set(codexParentThreadIDHeader, "root-thread")
		ctx := env.svc.WithOpenAIGuardianParentAffinity(context.Background(), c,
			[]byte(`{"model":"gpt-5.1","input":"turn"}`), "gpt-5.1")
		result, _ := env.selectAccount(t, ctx, "", "root-thread", nil)
		if result.Account.ID == owner {
			follows++
		}
		if result.ReleaseFunc != nil {
			result.ReleaseFunc()
		}
	}
	t.Logf("transport=%s follows_shared_binding=%d/%d", transport, follows, rounds)
	require.Equal(t, rounds, follows, "a child without rejection avoidance must keep shared affinity")
}
