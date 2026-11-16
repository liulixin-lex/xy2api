//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSelectionAdmissionAfterWaitRechecksAccount(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Account)
	}{
		{"disabled", func(a *Account) { a.Schedulable = false }},
		{"status", func(a *Account) { a.Status = "error" }},
		{"group_removed", func(a *Account) { a.GroupIDs = nil; a.AccountGroups = nil }},
		{"rate_limited", func(a *Account) { until := time.Now().Add(time.Hour); a.RateLimitResetAt = &until }},
		{"model_removed", func(a *Account) {
			a.Credentials = map[string]any{"model_mapping": map[string]any{"other-model": "other-model"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gid := int64(7)
			account := profitControlWSAccount(1, 0.2, time.Now())
			account.GroupIDs = []int64{gid}
			repo := schedulerTestOpenAIAccountRepo{accounts: []Account{account}}
			svc := &OpenAIGatewayService{
				accountRepo:        repo,
				cfg:                newSchedulerTestOpenAIWSV2Config(),
				rateLimitService:   newOpenAISelectionRateLimitService(),
				concurrencyService: NewConcurrencyService(&qualityCapacityCache{allBusy: true}),
			}
			selected, err := svc.SelectAccountWithLoadAwareness(context.Background(), &gid, "", "gpt-test", nil)
			require.NoError(t, err)
			require.NotNil(t, selected.WaitPlan)
			require.False(t, selected.Acquired)
			snapshot := *selected.Account
			selected.Account = &snapshot // the queued selection stays stale while the repository changes
			// A channel models the held slot. The writer commits its change
			// before allowing the waiting request to perform terminal admission.
			slotGranted := make(chan struct{})
			admitted := make(chan bool)
			go func() {
				<-slotGranted
				ctx := ContextWithSelectionProfitGate(context.Background(), selected)
				_, vetoed, _ := svc.SelectionAdmissionVetoLatest(ctx, selected.Account)
				admitted <- !vetoed
			}()
			tc.change(&repo.accounts[0])
			close(slotGranted)
			require.False(t, <-admitted, "account changed while queued must be rejected after taking the slot")
		})
	}
}

func TestSelectionAdmissionPreservesRescueContext(t *testing.T) {
	gid := int64(7)
	until := time.Now().Add(30 * time.Second)
	account := profitControlWSAccount(1, 0.2, time.Now())
	account.GroupIDs = []int64{gid}
	account.TempUnschedulableUntil = &until
	account.TempUnschedulableReason = "transient:TRANSIENT synthetic"
	repo := schedulerTestOpenAIAccountRepo{accounts: []Account{account}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	req := OpenAIAccountScheduleRequest{GroupID: &gid, Platform: PlatformOpenAI, RequestedModel: "gpt-test"}
	ctx := context.WithValue(context.Background(), openAISelectionRequestKey{}, req)
	ctx = withOpenAIProxyStreamQuarantineBypass(withResilienceLastResortGate(ctx))
	selection := attachSelectionProfitGate(ctx, &AccountSelectionResult{Account: withoutSoftPenalties(account, time.Now())})
	admission := ContextWithSelectionProfitGate(context.Background(), selection)
	require.True(t, resilienceLastResortGate(admission))
	require.True(t, openAIProxyStreamQuarantineBypassed(admission))
	_, vetoed, reason := svc.SelectionAdmissionVetoLatest(admission, selection.Account)
	require.False(t, vetoed, reason)
	repo.accounts[0].Schedulable = false
	_, vetoed, _ = svc.SelectionAdmissionVetoLatest(admission, selection.Account)
	require.True(t, vetoed, "rescue must still reject a newly disabled account")
}

func TestSelectionAdmissionUsesCurrentWebSocketTurnModel(t *testing.T) {
	account := profitControlWSAccount(1, 0.2, time.Now())
	account.Credentials = map[string]any{"model_mapping": map[string]any{"first-model": "first-model"}}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{account}}}
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "first-model"}
	ctx := context.WithValue(context.Background(), openAISelectionRequestKey{}, req)
	_, vetoed, reason := svc.SelectionAdmissionVetoLatest(ctx, &account)
	require.False(t, vetoed, reason)
	next := WithOpenAISelectionRequestModel(ctx, "next-model")
	_, vetoed, _ = svc.SelectionAdmissionVetoLatest(next, &account)
	require.True(t, vetoed, "a persistent connection must validate this turn's model")
	require.Equal(t, "first-model", ctx.Value(openAISelectionRequestKey{}).(OpenAIAccountScheduleRequest).RequestedModel)
}
