//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
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

// Count logical repository calls, including the handler's terminal check.
type admissionCountingAccountRepo struct {
	schedulerTestOpenAIAccountRepo
	reads int
}

func (r *admissionCountingAccountRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.reads++
	return r.schedulerTestOpenAIAccountRepo.GetByID(ctx, id)
}
func TestSelectionAdmissionReadCounts(t *testing.T) {
	for _, path := range []string{"immediate", "queued", "ws_followup"} {
		t.Run(path, func(t *testing.T) {
			group := &Group{ID: 21, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true}
			account := profitControlWSAccount(1, 0.2, time.Now())
			account.GroupIDs = []int64{21}
			accounts := &admissionCountingAccountRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{account}}}
			groups := &eligibilityCountingGroupRepo{fallbackGroupRepo: fallbackGroupRepo{groups: map[int64]*Group{21: group}}}
			cfg := newSchedulerTestOpenAIWSV2Config()
			cfg.Gateway.Scheduling.LoadBatchEnabled = false
			svc := &OpenAIGatewayService{accountRepo: accounts, cfg: cfg,
				schedulerSnapshot:  &SchedulerSnapshotService{groupRepo: groups, cache: &openAISnapshotCacheStub{snapshotAccounts: []*Account{&account}, accountsByID: map[int64]*Account{1: &account}}},
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{1: path != "queued"}})}
			requestCtx := context.WithValue(context.Background(), ctxkey.Group, group)
			selection, err := svc.SelectAccountWithLoadAwareness(requestCtx, &group.ID, "", "gpt-test", nil)
			require.NoError(t, err)
			require.Equal(t, 1, accounts.reads, "initial authoritative account read")
			require.Equal(t, 1, groups.reads, "initial privacy read")
			ctx := ContextWithSelectionProfitGate(requestCtx, selection)
			_, vetoed, reason := svc.SelectionAdmissionForResult(ctx, selection, path == "queued")
			require.False(t, vetoed, reason)
			if path == "ws_followup" {
				accounts.reads, groups.reads = 0, 0
				_, vetoed, reason = svc.SelectionAdmissionVetoLatest(ctx, selection.Account)
				require.False(t, vetoed, reason)
			}
			want := 1
			if path == "queued" {
				want = 2
			}
			require.Equal(t, want, accounts.reads)
			require.Equal(t, want, groups.reads)
			t.Logf("%s: accounts=%d groups=%d", path, accounts.reads, groups.reads)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}
func TestSelectionAdmissionUnknownRequestSkipsOnlyGroup(t *testing.T) {
	account := profitControlWSAccount(1, 0.2, time.Now())
	account.GroupIDs = []int64{7}
	svc := &OpenAIGatewayService{}
	_, vetoed, reason := svc.SelectionAdmissionVeto(context.Background(), &account)
	require.False(t, vetoed, reason)
	account.Schedulable = false
	_, vetoed, _ = svc.SelectionAdmissionVeto(context.Background(), &account)
	require.True(t, vetoed)
}

func TestSelectionAdmissionRechecksGrokFreeQuota(t *testing.T) {
	const id int64 = 991701
	account := Account{ID: id, Platform: PlatformGrok, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"subscription_tier": "free"}}
	svc := &OpenAIGatewayService{cfg: grokFreeQuotaTestConfig(), usageLogRepo: &grokFreeQuotaUsageRepoStub{}, accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{account}}}
	ctx := context.WithValue(context.Background(), openAISelectionRequestKey{}, OpenAIAccountScheduleRequest{Platform: PlatformGrok})
	openaiGrokFreeQuotaGateCache.Store(id, grokFreeQuotaGateCacheEntry{tokens: 0, checkedAt: time.Now(), known: true})
	defer openaiGrokFreeQuotaGateCache.Delete(id)
	require.NotNil(t, svc.recheckSelectionAccount(ctx, OpenAIAccountScheduleRequest{Platform: PlatformGrok}, &account))
	openaiGrokFreeQuotaGateCache.Store(id, grokFreeQuotaGateCacheEntry{tokens: 500000, checkedAt: time.Now(), known: true})
	_, vetoed, reason := svc.SelectionAdmissionVetoLatest(ctx, &account)
	require.True(t, vetoed)
	require.Equal(t, "quota_auto_pause", reason)
}
