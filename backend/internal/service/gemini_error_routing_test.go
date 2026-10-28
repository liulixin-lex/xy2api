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

func TestGeminiModelUnsupportedErrorUsesAccountModelCooldown(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	service := &GeminiMessagesCompatService{rateLimitService: &RateLimitService{accountRepo: repo}}
	account := &Account{ID: 92, Platform: PlatformGemini, Type: AccountTypeAPIKey}

	service.handleGeminiUpstreamError(context.Background(), account, http.StatusNotFound, nil,
		[]byte(`{"error":{"message":"unknown model gemini-not-real"}}`), "gemini-not-real")

	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gemini-not-real", repo.modelRateLimitCalls[0].scope)
	require.Equal(t, upstreamModelNotFoundReason, repo.modelRateLimitCalls[0].reason)
	require.WithinDuration(t, time.Now().Add(30*time.Minute), repo.modelRateLimitCalls[0].resetAt, 5*time.Second)
}

func TestGeminiBalance429And529UseBalancePause(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, 529} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			service, repo, account, _ := newPenaltyFixture(t, PlatformGemini)
			service.HandleUpstreamError(context.Background(), account, status, http.Header{},
				[]byte(`{"error":{"message":"insufficient account balance"}}`), "gemini-2.5-pro")
			require.True(t, resilience.IsBalancePause(repo.tempReason), repo.tempReason)
			require.Equal(t, 1, resilience.BalancePauseStep(repo.tempReason))
			require.Equal(t, 1, repo.tempCalls)
		})
	}
}

func TestDeactivatedWorkspace402UsesAuthHandling(t *testing.T) {
	service, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusPaymentRequired, nil,
		[]byte(`{"error":{"code":"deactivated_workspace","message":"workspace is inactive"}}`))
	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.errorCalls)
	require.False(t, resilience.IsBalancePause(repo.tempReason))
}
