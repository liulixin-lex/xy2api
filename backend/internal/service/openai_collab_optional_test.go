//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabOptionalEffectsStayUnwritten(t *testing.T) {
	ctx := resilience.WithCollabAttempt(context.Background(), resilience.CollabAttempt{Optional: true})
	repo := &rateLimitAccountRepoStub{}
	rl := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{accountRepo: repo, rateLimitService: rl}
	account := &Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	for _, status := range []int{401, 403, 429, 500, 503} {
		require.False(t, rl.HandleUpstreamError(ctx, account, status, http.Header{}, []byte(`{"error":{"message":"rate limit exceeded"}}`)))
		require.False(t, svc.handleOpenAIAccountUpstreamError(ctx, account, status, nil, nil))
	}
	require.Zero(t, repo.rateLimitedCalls)
	require.False(t, rl.ObserveOpenAIAPIKeyHealthFailure(ctx, account, errors.New("network failure")))
	require.Nil(t, svc.capabilityFailureLearner(ctx, account, []byte(`{"model":"synthetic-model"}`), "responses"))
	require.False(t, svc.learnOpenAITierRefusal(ctx, account, "synthetic-model", "priority", "responses", 400, []byte(`{"error":{"code":"unsupported_service_tier"}}`)))
	require.Nil(t, svc.newSchedAttemptCapture(ctx, account, nil, false, time.Now(), nil))
	require.NoError(t, svc.RecordUsage(ctx, nil))
	require.NoError(t, svc.BindStickySession(ctx, nil, "synthetic-session", account.ID))
	svc.noteOpenAITerminalRejectionForSession(ctx, account, 1, "synthetic-session", 400, nil, []byte(`{"error":{"code":"invalid_encrypted_content"}}`), errors.New("rejected"))
	svc.tempUnscheduleOpenAITransportError(ctx, account, "network failure")
	require.False(t, noteResiliencePostOutputFailureWithCategory(ctx, rl, account, nil, "read", resilience.CategoryTransient))
	plan, ok := planHTTPEncryptedContentRepair(ctx, nil, account, nil, []byte(collabBodyFixture), nil)
	require.False(t, ok)
	require.Empty(t, plan.Next)
	attempt := &openAIM3Attempt{}
	attempt.persist(ctx, []openAIBadEncryptedSlot{{}})
	next, err := attempt.derive(ctx, []byte(collabBodyFixture))
	require.NoError(t, err)
	require.Equal(t, collabBodyFixture, string(next))
	require.Nil(t, attempt.learn(ctx, nil, nil, "", 400, 400))
	attempt.succeeded(ctx)
	declaration := &openAIWebSearchReplay{accountID: 71, replayed: true, optional: true}
	declaration.succeeded()
	require.False(t, openAIWebSearchDeclared.needed(71, time.Now()))
	upstream, release := detachUpstreamContext(ctx)
	defer release()
	require.Same(t, ctx, upstream)
}

func TestCollabChildStateKeepsIdentityAndSeparatesBudget(t *testing.T) {
	parent := resilience.NewRequestState()
	child := resilience.NewCollabChildState(parent)
	require.Equal(t, parent.GatewayRequestID(), child.GatewayRequestID())
	require.Same(t, parent.CollabSettingsSnapshot(), child.CollabSettingsSnapshot())
	child.SetRecoveryOutcome("completed")
	require.Nil(t, parent.RecoveryOutcome())
}
