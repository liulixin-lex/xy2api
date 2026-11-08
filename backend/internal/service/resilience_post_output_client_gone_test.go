//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// postOutputRequestWithClient is postOutputRequest with delivery tracking
// bound to client, as the handler does for every tracked request.
func postOutputRequestWithClient(rec resilience.SessionRecorder, client context.Context) context.Context {
	ctx := postOutputRequest(rec)
	resilience.StateFrom(ctx).InitDelivery(client, time.Now(), func() int64 { return 1 })
	return ctx
}

// A client that leaves after partial delivery lets the upstream drain and
// then be cancelled. Whatever ends that drain is not a failure of the
// account: no failed mark, no session rebind, no post-output count, no
// cooldown and no stall.
func TestPostOutputFailureIgnoredAfterClientLeft(t *testing.T) {
	rl, repo, account, r := newPenaltyFixture(t, PlatformOpenAI)
	store := newMemResilienceStore()
	cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": account.ID}}
	svc := &GatewayService{cache: cache, resilience: NewResilienceService(nil, store)}

	client, cancel := context.WithCancel(context.Background())
	cancel()
	before := postOutputMetric(account.ID, postOutputReadError)
	for i := 0; i < postOutputCooldownThreshold+1; i++ {
		ctx := postOutputRequestWithClient(svc.gatewaySessionRecorder(), client)
		require.False(t, noteResiliencePostOutputFailure(ctx, rl, account, &http.Response{}, postOutputReadError))
		noteResilienceStall(ctx, rl, account)
		require.False(t, resilience.StateFrom(ctx).AccountFailed(account.ID))
		require.False(t, resilience.StateFrom(ctx).Avoided(account.ID))
	}

	require.Equal(t, account.ID, cache.sessionBindings["sess"], "the binding stays on the account")
	gid := int64(25)
	require.NotContains(t, svc.resilience.sessionAvoid(context.Background(), &gid, "sess"), account.ID)
	require.Empty(t, r.postOutput.failures[account.ID])
	require.Zero(t, repo.tempCalls, "no cooldown")
	require.Equal(t, before, postOutputMetric(account.ID, postOutputReadError))
}

// The same failure with the client still connected keeps the existing
// behaviour: the account is marked failed and the session moves away.
func TestPostOutputFailureCountsWhileClientConnected(t *testing.T) {
	rl, _, account, r := newPenaltyFixture(t, PlatformOpenAI)
	store := newMemResilienceStore()
	cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": account.ID}}
	svc := &GatewayService{cache: cache, resilience: NewResilienceService(nil, store)}

	ctx := postOutputRequestWithClient(svc.gatewaySessionRecorder(), context.Background())
	require.True(t, noteResiliencePostOutputFailure(ctx, rl, account, &http.Response{}, postOutputReadError))
	require.True(t, resilience.StateFrom(ctx).AccountFailed(account.ID))
	_, bound := cache.sessionBindings["sess"]
	require.False(t, bound, "the first failure after output rebinds the session")
	gid := int64(25)
	require.Contains(t, svc.resilience.sessionAvoid(context.Background(), &gid, "sess"), account.ID)
	require.Len(t, r.postOutput.failures[account.ID], 1)
}
