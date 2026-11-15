//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Match the production first-writer Claim operation for an absent primary key.
// The regression is about which key the service uses, not Redis CAS internals.
type atomicAliasCache struct{ *selectionRuntimeCache }

func (c *atomicAliasCache) ClaimSessionAccountID(_ context.Context, _ int64, key string, owner int64, _ time.Duration, _ bool) (int64, error) {
	if c.sessionBindings[key] == 0 {
		c.sessionBindings[key] = owner
	}
	return c.sessionBindings[key], nil
}

func (c *atomicAliasCache) DeleteSessionAccountIDIfOwner(_ context.Context, _ int64, key string, owner int64) error {
	if c.sessionBindings[key] == owner {
		delete(c.sessionBindings, key)
	}
	return nil
}

func TestStickyLegacyCompletionCannotBeHiddenByPrimaryRefresh(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 1))
	f.svc.cache = &atomicAliasCache{selectionRuntimeCache: f.cache}
	f.cache.sessionBindings["openai:legacy"] = 10
	group := int64(7)
	newContext := func() context.Context {
		return withOpenAILegacySessionHash(resilience.WithRequestState(context.Background(), resilience.NewRequestState()), "legacy")
	}
	one, two := newContext(), newContext()
	target, _, err := f.svc.SelectAccountWithSchedulerForCapability(one, &group, "", "v2:session", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.NoError(t, err)
	require.EqualValues(t, 1, target.Account.ID)
	defer target.ReleaseFunc()
	// A second request cannot use the target; it keeps the old owner and
	// refreshes it while the first request is still forwarding to the target.
	owner, _, err := f.svc.SelectAccountWithSchedulerForCapability(two, &group, "", "v2:session", "m", map[int64]struct{}{1: {}}, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.NoError(t, err)
	require.EqualValues(t, 10, owner.Account.ID)
	defer owner.ReleaseFunc()
	CompleteOpenAIStickyChange(one, target.Account.ID, true, false)
	actual, err := f.svc.getStickySessionAccountID(newContext(), &group, "v2:session")
	require.NoError(t, err)
	t.Logf("after successful takeover: bindings=%v", f.cache.sessionBindings)
	require.EqualValues(t, target.Account.ID, actual, "owner TTL refresh must not create a higher-precedence alias hiding the completed takeover")
}
