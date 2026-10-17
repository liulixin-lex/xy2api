//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// Upstream balance failures pause the account (design §3.2; simplified for
// 0.4.0 at the user's request, 2026-10-17): balance.pause_seconds through
// temp_unschedulable with a "balance:" reason, a hard penalty while it
// lasts. Real traffic retries the account when the pause ends; no balance
// record, probe or recovery state is kept.

const balance403Body = `{"error":{"type":"permission_error","message":"Insufficient account balance"}}`

func (r *resilienceAccountRepo) ClearModelRateLimits(context.Context, int64) error { return nil }

func requireBalancePause(t *testing.T, repo *resilienceAccountRepo, pause time.Duration) {
	t.Helper()
	require.Zero(t, repo.errorCalls, "the account stays active")
	require.True(t, strings.HasPrefix(repo.tempReason, "balance:exhausted: "), repo.tempReason)
	require.True(t, resilience.IsBalancePause(repo.tempReason))
	require.Equal(t, resilience.TierHard, resilience.TempUnschedTier(repo.tempReason))
	require.WithinDuration(t, time.Now().Add(pause), repo.tempUntil, 3*time.Second)
	require.Zero(t, repo.extraWrites, "no balance record is written")
	require.NotContains(t, repo.account.Extra, "resilience_balance")
}

func TestBalanceFailuresPauseTheAccount(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		platform string
		apply    func(svc *RateLimitService, r *ResilienceService, account *Account)
	}{
		{"HTTP 403 on an OpenAI relay", PlatformOpenAI, func(svc *RateLimitService, _ *ResilienceService, account *Account) {
			svc.HandleUpstreamError(ctx, account, http.StatusForbidden, http.Header{}, []byte(balance403Body))
		}},
		{"HTTP 403 on a Claude relay", PlatformAnthropic, func(svc *RateLimitService, _ *ResilienceService, account *Account) {
			svc.HandleUpstreamError(ctx, account, http.StatusForbidden, http.Header{},
				[]byte(`{"type":"error","error":{"type":"permission_error","message":"insufficient balance"}}`))
		}},
		{"HTTP 402", PlatformOpenAI, func(svc *RateLimitService, _ *ResilienceService, account *Account) {
			svc.HandleUpstreamError(ctx, account, http.StatusPaymentRequired, http.Header{}, []byte(`{"error":{"message":"payment required"}}`))
		}},
		{"HTTP 429 right after a success", PlatformAnthropic, func(svc *RateLimitService, r *ResilienceService, account *Account) {
			r.NoteAccountSuccess(ctx, account)
			svc.HandleUpstreamError(ctx, account, http.StatusTooManyRequests, http.Header{},
				[]byte(`{"error":{"type":"rate_limit_error","message":"Insufficient account balance"}}`))
		}},
		{"failure inside a stream", PlatformOpenAI, func(svc *RateLimitService, _ *ResilienceService, account *Account) {
			res := resilience.Classify(resilience.Outcome{Source: resilience.SourceStreamEvent, StatusCode: http.StatusOK, Code: "insufficient_quota"}, nil)
			svc.ApplyResilienceOutcome(ctx, account, res, "stream insufficient_quota")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, account, r := newPenaltyFixture(t, tc.platform)
			tc.apply(svc, r, account)
			requireBalancePause(t, repo, 300*time.Second)
		})
	}
}

func TestBalancePauseLengthIsConfigurable(t *testing.T) {
	svc, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	withResilienceSettings(t, func(s *resilience.Settings) { s.Balance.PauseSeconds = 45 })
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(balance403Body))
	requireBalancePause(t, repo, 45*time.Second)

	// A failure after the pause pauses again for the same length: no back-off.
	repo.tempUntil = time.Now().Add(-time.Second)
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(balance403Body))
	requireBalancePause(t, repo, 45*time.Second)
}

// While paused the account is never selected, not even as a last resort;
// once the pause ends it is selectable again.
func TestBalancePauseIsHardUntilItEnds(t *testing.T) {
	withResilienceSettings(t, nil)
	now := time.Now()
	until := now.Add(300 * time.Second)
	account := &Account{ID: 7301, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		TempUnschedulableUntil: &until, TempUnschedulableReason: resilience.BalancePauseReason("HTTP 403: Insufficient account balance")}

	require.False(t, account.IsSchedulable(), "normal selection skips it")
	tier, _, why := resiliencePenaltyOf(account, "gpt-5.1", now)
	require.Equal(t, resilience.TierHard, tier)
	require.Equal(t, "balance_exhausted", why)
	require.False(t, lastResortTierAllowed(tier, account.ID, nil), "no last resort")
	reason, recoverAt := shareAccountStateReason(account, now)
	require.Equal(t, sharesched.ReasonBalanceExhausted, reason)
	require.True(t, recoverAt.IsZero(), "share sessions move at once")

	ended := now.Add(-time.Second)
	account.TempUnschedulableUntil = &ended
	require.True(t, account.IsSchedulable())
	tier, _, _ = resiliencePenaltyOf(account, "gpt-5.1", now)
	require.Equal(t, resilience.TierNone, tier)
}

// A single-account group in a balance pause answers 503; its Retry-After
// follows the end of the pause (design §9.1).
func TestNoAccountRetryAfterFollowsTheBalancePause(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, left := range []time.Duration{12 * time.Second, 200 * time.Second} {
		until := time.Now().Add(left)
		repo := &groupAccountsRepo{byGroup: map[int64][]Account{7300: {{ID: 7302, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, TempUnschedulableUntil: &until,
			TempUnschedulableReason: resilience.BalancePauseReason("HTTP 403: Insufficient account balance")}}}}
		got := NewResilienceService(nil, nil).noAccountRetryAfter(context.Background(), repo, 7300, "gpt-5.1")
		require.InDelta(t, left.Seconds(), got.Seconds(), 2, "the pause ends in %s", left)
	}
}

// "Recover State" in the account list (DELETE .../temp-unschedulable)
// ends a balance pause at once.
func TestRestoringTheAccountEndsTheBalancePause(t *testing.T) {
	svc, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(balance403Body))
	require.True(t, resilience.IsBalancePause(repo.tempReason))

	require.NoError(t, svc.ClearTempUnschedulable(context.Background(), account.ID))
	fresh, err := repo.GetByID(context.Background(), account.ID)
	require.NoError(t, err)
	require.True(t, fresh.IsSchedulable())
	tier, _, _ := resiliencePenaltyOf(fresh, "gpt-5.1", time.Now())
	require.Equal(t, resilience.TierNone, tier)
}

// Switched off, balance failures take the 0.3.0 paths.
func TestBalanceSwitchOffKeeps030Handling(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Balance.Enabled = false })
	svc, repo, account, _ := newPenaltyFixtureKeepSettings(t, PlatformOpenAI)
	svc.HandleUpstreamError(context.Background(), account, http.StatusPaymentRequired, http.Header{}, []byte(`{"error":{"message":"payment required"}}`))
	require.Equal(t, 1, repo.errorCalls, "0.3.0: 402 sets the error state")
	require.False(t, resilience.IsBalancePause(repo.tempReason))
	require.Zero(t, repo.extraWrites)
}
