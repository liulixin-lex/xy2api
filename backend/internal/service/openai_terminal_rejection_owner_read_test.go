//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestEnc252XRootDoesNotClearChangedOwner(t *testing.T) {
	withResilienceSettings(t, nil)
	svc, _, _ := terminalRejectionTestService()
	cache := &atomicStickyTestCache{bindings: map[string]int64{}}
	svc.cache = cache
	account := terminalRejectionTestAccount(39101, "relay-a.example")
	c, _ := newOpenAIUpstreamErrorTestContext(t)
	c.Request.Header.Set(openAIWSThreadIDHeader, "root-thread")
	const session = "v2:owner-changed"
	cache.bindings[svc.openAISessionCacheKey(session)] = 39102
	state := resilience.NewRequestState()
	state.SetSession(9, session, svc.openAISessionRecorder())
	ctx := WithOpenAITerminalRejectionScope(resilience.WithRequestState(c.Request.Context(), state), c, []byte(`{"model":"gpt-5.1","input":"turn"}`))
	for range resilience.Current().Session.StreakThreshold {
		svc.noteOpenAITerminalRejection(ctx, account, http.StatusBadRequest, nil, []byte(terminalRejectionBody), errors.New("terminal"))
	}
	t.Logf("changed_owner_after_old_rejection=%d", cache.bindings[svc.openAISessionCacheKey(session)])
	require.Equal(t, int64(39102), cache.bindings[svc.openAISessionCacheKey(session)])
}

type enc252XOwnerReadFailureCache struct {
	*shareTestCache
	calls   int
	failed  bool
	unknown bool
}

func (c *enc252XOwnerReadFailureCache) GetSessionAccountID(ctx context.Context, g int64, k string) (int64, error) {
	c.calls++
	if c.calls == 2 {
		c.failed = true
		if c.unknown {
			return 0, nil
		}
		return 0, errors.New("synthetic one-shot cache read timeout")
	}
	return c.shareTestCache.GetSessionAccountID(ctx, g, k)
}
func TestEnc252XChildAvoidanceSurvivesOwnerReadFailure(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run("unknown_"+fmtBool(unknown), func(t *testing.T) {
			testChildAvoidancePreservesUnknownOwner(t, unknown)
		})
	}
}

func testChildAvoidancePreservesUnknownOwner(t *testing.T, unknown bool) {
	withResilienceSettings(t, nil)
	env := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
	env.svc.resilience = NewResilienceService(nil, newMemResilienceStore())
	const session = "root-thread"
	_, owner := openAIShareTarget(t, session)
	env.cache.bind("openai:"+session, owner, 0)
	cache := &enc252XOwnerReadFailureCache{shareTestCache: env.cache, unknown: unknown}
	env.svc.cache = cache
	c, _ := newOpenAIUpstreamErrorTestContext(t)
	c.Request.Header.Set("session-id", session)
	c.Request.Header.Set(openAIWSThreadIDHeader, "child-thread")
	c.Request.Header.Set(codexParentThreadIDHeader, session)
	ctx := env.svc.WithOpenAIGuardianParentAffinity(cacheableContext(), c, []byte(`{"model":"gpt-5.1","input":"turn"}`), "gpt-5.1")
	env.svc.AvoidOpenAIStateAccount(ctx, shareTestGroupID, openAIRejectionScope(ctx).session, owner)
	result, _ := env.selectAccount(t, ctx, "", session, nil)
	if result.ReleaseFunc != nil {
		result.ReleaseFunc()
	}
	group := shareTestGroupID
	require.NoError(t, env.svc.BindStickySessionAfterProfitAdmission(ctx, &group, session, result.Account.ID))
	require.True(t, cache.failed)
	require.True(t, preserveOpenAIRejectionBinding(ctx, session))
	t.Logf("selected=%d previous_shared_owner=%d shared_owner_after=%d writes_suppressed=%t", result.Account.ID, owner, env.cache.sessionBindings["openai:"+session], preserveOpenAIRejectionBinding(ctx, session))
	require.Equal(t, owner, env.cache.sessionBindings["openai:"+session], "a transient read failure must not let a temporarily avoided child replace the shared owner")
}
