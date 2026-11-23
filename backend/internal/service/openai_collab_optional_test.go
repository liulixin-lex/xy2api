//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
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

func TestCollabOptionalQuotaSnapshotDoesNotWriteAccount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &rateLimitAccountRepoStub{}
		svc := &OpenAIGatewayService{accountRepo: repo}
		ctx := resilience.WithCollabAttempt(context.Background(), resilience.CollabAttempt{Optional: true})
		used := float64(100)
		svc.updateCodexUsageSnapshot(ctx, 7, &OpenAICodexUsageSnapshot{PrimaryUsedPercent: &used})
		synctest.Wait()
		require.Zero(t, repo.updateExtraCalls)
	})
}

func TestCollabOptionalDisconnectDrainsAfterDelivery(t *testing.T) {
	original := resilience.Current().Clone()
	defer resilience.Publish(original)
	settings := original.Clone()
	settings.Disconnect.Enabled = true
	settings.Disconnect.PartialDrainMaxSeconds = 2
	resilience.Publish(settings)
	for _, delivered := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			synctest.Test(t, func(t *testing.T) {
				client, cancel := context.WithCancel(context.Background())
				owner := resilience.NewRequestState()
				owner.InitDelivery(client, time.Now(), func() int64 {
					if delivered {
						return 10
					}
					return 0
				})
				ctx := resilience.WithCollabAttempt(resilience.WithRequestState(client, resilience.NewCollabChildState(owner)), resilience.CollabAttempt{Optional: true})
				var upstream context.Context
				var release context.CancelFunc
				if stream {
					upstream, release = detachStreamUpstreamContext(ctx, true)
				} else {
					upstream, release = detachUpstreamContext(ctx)
				}
				defer release()
				cancel()
				synctest.Wait()
				if !delivered {
					require.Error(t, upstream.Err())
					return
				}
				require.NoError(t, upstream.Err())
				time.Sleep(3 * time.Second)
				synctest.Wait()
				require.Error(t, upstream.Err())
			})
		}
	}
}

func TestCollabOptionalContextSurvivesBuildRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(resilience.WithCollabAttempt(context.Background(), resilience.CollabAttempt{Optional: true}))
	defer cancel()
	upstream, release := detachUpstreamContext(ctx)
	release()
	require.NoError(t, upstream.Err(), "request construction releases its temporary context before dispatch")
	cancel()
	select {
	case <-upstream.Done():
	case <-time.After(time.Second):
		t.Fatal("preparation cancellation lost")
	}
}

func TestCollabOptionalSelectionReleasesTrial(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	leases := newSelectionTrialLeaseFake()
	selectionSetTrial(f, 1, leases)
	ctx := resilience.WithCollabAttempt(resilience.WithRequestState(context.Background(), resilience.NewCollabChildState(resilience.NewRequestState())), resilience.CollabAttempt{Optional: true})
	result, err := f.svc.SelectCollabAccount(ctx, ptrInt64Selection(7), "m", OpenAIEndpointCapabilityResponses, 1, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Nil(t, result.WaitPlan)
	require.NotEmpty(t, leases.held)
	require.Empty(t, f.cache.sessionBindings)
	release := CombineSelectionRelease(result, nil)
	release()
	release()
	require.Empty(t, leases.held)
	require.Empty(t, f.cache.sessionBindings)
}
