//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

func TestGrokAPIKeyBalance429GoesToBalanceStateNotRateLimit(t *testing.T) {
	withResilienceSettings(t, nil)
	account := &Account{ID: 162, Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	svc, repo := grokBalanceFixture(account)
	headers := grokRelayResetHeaders(time.Now().Add(28 * time.Minute))
	ctx := withGrokTeamRateLimitModel(context.Background(), "grok-4.6")

	svc.handleGrokAccountUpstreamError(ctx, account, http.StatusTooManyRequests, headers, []byte(grokBalance429Body))

	require.Zero(t, repo.rateLimitedCalls, "no quota-window rate limit from the relay headers")
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Equal(t, resilience.BalanceExhausted, resilience.BalanceStateOf(account.Extra))
	require.Equal(t, 1, repo.tempUnschedCalls)
	require.True(t, strings.HasPrefix(repo.lastTempUnschedReason, resilience.ReasonPrefixBalanceExhausted))
	// Probes recheck from 60 s on; the exclusion is bounded by the first
	// recheck plus its grace, not by the relay's 28-minute reset.
	require.True(t, repo.lastTempUnschedUntil.Before(time.Now().Add(15*time.Minute)))
}

func TestGrokAPIKeyBalance429AfterRecentSuccessIsWeak(t *testing.T) {
	withResilienceSettings(t, nil)
	account := &Account{ID: 163, Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	svc, repo := grokBalanceFixture(account)
	svc.rateLimitService.resilience.successes.last.Store(account.ID, time.Now())

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, grokRelayResetHeaders(time.Now().Add(28*time.Minute)), []byte(grokBalance429Body))

	require.Zero(t, repo.rateLimitedCalls)
	require.Equal(t, resilience.BalanceSuspect, resilience.BalanceStateOf(account.Extra))
	require.True(t, strings.HasPrefix(repo.lastTempUnschedReason, resilience.ReasonPrefixBalanceSuspect))
	require.Equal(t, resilience.TierSoft, resilience.TempUnschedTier(repo.lastTempUnschedReason),
		"a suspect account stays available as a last resort")
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
			require.Equal(t, resilience.BalanceOK, resilience.BalanceStateOf(tc.account.Extra))
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

	require.Equal(t, resilience.BalanceOK, resilience.BalanceStateOf(account.Extra))
	require.Zero(t, repo.tempUnschedCalls)
}
