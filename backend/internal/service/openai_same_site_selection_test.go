//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAISameSiteHoldOverridesSoftPenalty(t *testing.T) {
	for _, tc := range []struct {
		name, penalty     string
		missing, disabled bool
	}{
		{name: "hold only"}, {name: "short account limit", penalty: "rate"},
		{name: "model limit early return", penalty: "model"},
		{name: "transient soft penalty", penalty: "transient"},
		{name: "longer soft penalty has another reason", penalty: "longer"},
		{name: "missing owner with short limit", penalty: "rate", missing: true},
		{name: "missing owner with model limit", penalty: "model", missing: true},
		{name: "disabled hold alone retains healthy owner", disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newOpenAIRuntimeBlockEnv(t, 1, 2)
			const session = "same-site-owner"
			owner, other := env.account(1), env.account(2)
			owner.Credentials = map[string]any{"base_url": "https://site-a.example/v1"}
			other.Credentials = map[string]any{"base_url": "https://site-b.example/v1"}
			now := time.Now()
			until := now.Add(time.Minute)
			switch tc.penalty {
			case "rate":
				owner.RateLimitResetAt = &until
			case "model":
				owner.Extra = map[string]any{"model_rate_limits": map[string]any{"gpt-5.1": map[string]any{"rate_limit_reset_at": until.Format(time.RFC3339)}}}
			case "transient", "longer":
				if tc.penalty == "longer" {
					until = now.Add(8 * time.Minute)
				}
				owner.TempUnschedulableUntil = &until
				owner.TempUnschedulableReason = "transient:TRANSIENT HTTP 502"
			}
			peer := *owner
			peer.ID = 999
			env.svc.resilience.noteOpenAISameSiteStreamFailure(&peer, now.Add(-time.Second))
			env.svc.resilience.noteOpenAISameSiteStreamFailure(owner, now)
			tier, _, why := env.svc.resilienceOpenAIPenaltyOf(owner, "gpt-5.1", now)
			require.Equal(t, resilience.TierSoft, tier, "global penalty remains eligible for last resort")
			if tc.penalty == "longer" {
				require.NotEqual(t, "same_site_hold", why)
			}
			var snapshot *openAISnapshotCacheStub
			if tc.missing {
				snapshot = &openAISnapshotCacheStub{snapshotAccounts: []*Account{other}, accountsByID: map[int64]*Account{1: owner, 2: other}}
				env.svc.schedulerSnapshot = &SchedulerSnapshotService{cache: snapshot}
			}
			if tc.disabled {
				settings := resilience.Current().Clone()
				settings.SameSiteHold.Enabled = false
				resilience.Publish(settings)
			}
			env.cache.bind("openai:"+session, 1, time.Hour-30*time.Second)
			ctx := context.Background()
			result, _ := env.selectAccount(t, ctx, "", session, nil)
			wantServing := int64(2)
			if tc.disabled && tc.penalty == "" {
				wantServing = 1
			}
			require.Equal(t, wantServing, result.Account.ID)
			group := selectionTestGroupID
			require.NoError(t, env.svc.BindStickySessionAfterProfitAdmission(ContextWithSelectionProfitGate(ctx, result), &group, session, result.Account.ID))
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			wantOwner := int64(2)
			if tc.disabled {
				wantOwner = 1
			}
			require.Equal(t, wantOwner, env.cache.sessionBindings["openai:"+session])
			owner.RateLimitResetAt = nil
			owner.TempUnschedulableUntil = nil
			owner.Extra = nil
			tracker := &env.svc.resilience.sameSiteHold
			tracker.mu.Lock()
			entry := tracker.accounts[owner.ID]
			entry.until = now.Add(-time.Second)
			tracker.accounts[owner.ID] = entry
			tracker.mu.Unlock()
			if snapshot != nil {
				snapshot.snapshotAccounts = []*Account{owner, other}
			}
			next, _ := env.selectAccount(t, ctx, "", session, nil)
			require.Equal(t, wantOwner, next.Account.ID, "expiry must not return a rebound session to its old owner")
			if next.ReleaseFunc != nil {
				next.ReleaseFunc()
			}
		})
	}
	for _, overlap := range []bool{false, true} {
		t.Run("single held account remains last resort/soft="+fmtBool(overlap), func(t *testing.T) {
			env := newOpenAIRuntimeBlockEnv(t, lrAccountA)
			a := env.account(lrAccountA)
			if overlap {
				until := time.Now().Add(time.Minute)
				a.RateLimitResetAt = &until
			}
			peer := *a
			peer.ID = 999
			now := time.Now()
			env.svc.resilience.noteOpenAISameSiteStreamFailure(&peer, now.Add(-time.Second))
			env.svc.resilience.noteOpenAISameSiteStreamFailure(a, now)
			result := selectOpenAILastResort(t, env)
			require.Equal(t, a.ID, result.Account.ID)
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
		})
	}
}

func TestOpenAISelectionEqualKeysUseAccountID(t *testing.T) {
	a, b := selectionFixtureAccount(1, 0), selectionFixtureAccount(2, 0)
	a.RateMultiplier, b.RateMultiplier = floatPtr(1), floatPtr(1)
	svc := &OpenAIGatewayService{}
	svc.accountRepo = schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{a, b}}}
	group := int64(7)
	selected, _, _ := svc.selectBestAccount(context.Background(), &group, PlatformOpenAI, []Account{a, b}, "gpt-test", nil, false, "", true)
	require.NotNil(t, selected)
	require.Equal(t, a.ID, selected.ID)
}
