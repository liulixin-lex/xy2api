//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestResilienceExplainShowsPenaltiesSessionLastResortAndFallback(t *testing.T) {
	// tasks 3.5: Explain shows tiers, penalty reasons, the session's
	// failures and avoid set, the last-resort pick and the fallback decision.
	r, store, accounts, _ := newFallbackFixture(t)
	ctx := context.Background()
	pausedUntil := time.Now().Add(5 * time.Minute)
	hard := Account{ID: 4, Name: "dry", Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, Priority: 1,
		TempUnschedulableUntil: &pausedUntil, TempUnschedulableReason: resilience.BalancePauseReason("HTTP 403: Insufficient account balance")}
	soft := softAccount(1)
	soft.Name, soft.Priority = "busy", 2
	accounts.byGroup[10] = []Account{soft, hard}
	_, _ = store.IncrSessionFailure(ctx, 10, "sess", 1, time.Minute)
	_ = store.AddSessionAvoid(ctx, 10, "sess", 1, time.Minute)

	out, err := r.Explain(ctx, 10, "sess", "gpt-6.1-sol")
	require.NoError(t, err)
	require.Equal(t, 0, out.HealthyCount)
	require.Len(t, out.Accounts, 2)
	require.Equal(t, ResilienceAccountExplanation{AccountID: 4, Name: "dry", Platform: PlatformOpenAI, Priority: 1,
		Tier: "hard", Reason: "balance_exhausted"}, out.Accounts[0])
	busy := out.Accounts[1]
	require.Equal(t, "soft", busy.Tier)
	require.Equal(t, "temp_unschedulable", busy.Reason)
	require.NotNil(t, busy.RecoverAt)
	require.Equal(t, int64(1), busy.SessionFailures)
	require.True(t, busy.Avoided)
	require.Equal(t, &ResilienceSessionExplanation{Failures: 1, Avoided: []int64{1}}, out.Session)
	require.NotNil(t, out.LastResort, "soft account is the last resort; the hard one never")
	require.Equal(t, int64(1), out.LastResort.AccountID)
	require.NotNil(t, out.FallbackGroup)
	require.Equal(t, int64(20), out.FallbackGroup.GroupID)
	require.True(t, out.FallbackGroup.Usable)
	require.True(t, out.FallbackGroup.FallbackHealthy)
	require.True(t, out.FallbackGroup.WouldMove)

	// A healthy account: no last resort, no move.
	accounts.byGroup[10] = []Account{healthyAccount(5)}
	r.lastResortGroups.entries = nil
	out, err = r.Explain(ctx, 10, "", "")
	require.NoError(t, err)
	require.Equal(t, 1, out.HealthyCount)
	require.Nil(t, out.LastResort)
	require.Nil(t, out.Session)
	require.False(t, out.FallbackGroup.WouldMove)

	var nilSvc *ResilienceService
	_, err = nilSvc.Explain(ctx, 10, "", "")
	require.ErrorIs(t, err, ErrResilienceExplainUnavailable)
}

func TestResilienceExplainShowsModelRestriction(t *testing.T) {
	// Production: a group allowed a model its channel pricing did not list,
	// so every request failed with 503 while all accounts were healthy.
	// Explain names the restriction instead of the accounts.
	r, _, _, key := newFallbackFixture(t)
	ctx := context.Background()
	key.Group.ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.5", "gpt-6-sol"}}
	r.pricingRestricted = func(_ context.Context, groupID int64, model string) bool {
		return groupID == 10 && model == "gpt-5.5"
	}

	cases := map[string]string{"gpt-5.5": "channel_pricing", "gpt-5.4": "not_allowed", "gpt-6-sol": "", "": ""}
	for model, want := range cases {
		out, err := r.Explain(ctx, 10, "", model)
		require.NoError(t, err)
		require.Equal(t, want, out.ModelRestriction, model)
	}
}
