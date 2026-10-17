//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// A relay 429 "insufficient balance" on the only account of a Grok group
// installed a model rate limit from the relay's reset headers and locked the
// group for about 28 minutes in production (2026-10-16, 761 failures).

const grokBalance429Body = `{"error":{"message":"insufficient balance","type":"rate_limit_error"}}`

func grokBalanceFixture(account *Account) (*OpenAIGatewayService, *grokQuotaAccountRepo) {
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{account.ID: account},
	}}
	rl := &RateLimitService{accountRepo: repo}
	rl.SetResilienceService(NewResilienceService(nil, newMemResilienceStore()))
	return &OpenAIGatewayService{accountRepo: repo, rateLimitService: rl}, repo
}

func grokRelayResetHeaders(reset time.Time) http.Header {
	return http.Header{
		"X-Ratelimit-Limit-Requests":     []string{"10"},
		"X-Ratelimit-Remaining-Requests": []string{"0"},
		"X-Ratelimit-Reset-Requests":     []string{fmt.Sprintf("%d", reset.Unix())},
	}
}

func TestGrokAPIKeyBalance429PausesInsteadOfARateLimit(t *testing.T) {
	withResilienceSettings(t, nil)
	account := &Account{ID: 162, Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	svc, repo := grokBalanceFixture(account)
	headers := grokRelayResetHeaders(time.Now().Add(28 * time.Minute))
	ctx := withGrokTeamRateLimitModel(context.Background(), "grok-4.6")

	svc.handleGrokAccountUpstreamError(ctx, account, http.StatusTooManyRequests, headers, []byte(grokBalance429Body))

	require.Zero(t, repo.rateLimitedCalls, "no quota-window rate limit from the relay headers")
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Equal(t, 1, repo.tempUnschedCalls)
	require.True(t, resilience.IsBalancePause(repo.lastTempUnschedReason), repo.lastTempUnschedReason)
	// The pause is balance.pause_seconds, not the relay's 28-minute reset.
	require.WithinDuration(t, time.Now().Add(300*time.Second), repo.lastTempUnschedUntil, 3*time.Second)
	require.NotContains(t, account.Extra, "resilience_balance")
}

func TestGrokAPIKeyBalance429AfterASuccessStillPauses(t *testing.T) {
	withResilienceSettings(t, nil)
	account := &Account{ID: 163, Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	svc, repo := grokBalanceFixture(account)
	svc.rateLimitService.resilience.NoteAccountSuccess(context.Background(), account)

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, grokRelayResetHeaders(time.Now().Add(28*time.Minute)), []byte(grokBalance429Body))

	require.Zero(t, repo.rateLimitedCalls)
	require.True(t, resilience.IsBalancePause(repo.lastTempUnschedReason), repo.lastTempUnschedReason)
	require.Equal(t, resilience.TierHard, resilience.TempUnschedTier(repo.lastTempUnschedReason),
		"a recent success no longer makes it a weak, soft signal")
}

func TestGrokRateLimitPathsUnchangedOutsideTheBalanceCase(t *testing.T) {
	cases := []struct {
		name    string
		account *Account
		body    string
		off     bool
	}{
		{"plain 429 on an API key", &Account{ID: 164, Platform: PlatformGrok, Type: AccountTypeAPIKey}, `{"error":{"message":"rate limit exceeded"}}`, false},
		{"OAuth account", &Account{ID: 165, Platform: PlatformGrok, Type: AccountTypeOAuth}, grokBalance429Body, false},
		{"resilience off", &Account{ID: 166, Platform: PlatformGrok, Type: AccountTypeAPIKey}, grokBalance429Body, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = !tc.off })
			svc, repo := grokBalanceFixture(tc.account)

			svc.handleGrokAccountUpstreamError(context.Background(), tc.account, http.StatusTooManyRequests, http.Header{"Retry-After": []string{"45"}}, []byte(tc.body))

			require.Equal(t, 1, repo.rateLimitedCalls)
			require.False(t, resilience.IsBalancePause(repo.lastTempUnschedReason))
		})
	}
}

func TestGrokPoolModeBalance429KeepsGrokHandling(t *testing.T) {
	withResilienceSettings(t, nil)
	account := &Account{
		ID:          167,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"pool_mode": true},
	}
	require.True(t, account.IsPoolMode())
	svc, repo := grokBalanceFixture(account)

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, nil, []byte(grokBalance429Body))

	require.Zero(t, repo.tempUnschedCalls)
}
