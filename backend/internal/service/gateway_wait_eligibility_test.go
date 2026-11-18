//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGatewayWaitLastResortStillRequiresCurrentGroupAndSoftState(t *testing.T) {
	groupID := int64(7)
	until := time.Now().Add(time.Minute)
	svc, _ := newLastResortGatewayFixture(t, []Account{{
		ID: 41, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true, Concurrency: 1, OverloadUntil: &until,
		AccountGroups: []AccountGroup{{GroupID: groupID}},
	}})
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{41: false}})
	ctx := context.Background()
	pick, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "", "claude-sonnet-4", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, pick.WaitPlan)
	serviceAccount, veto, _ := svc.GatewayAccountAfterSlot(ctx, pick)
	require.NotNil(t, serviceAccount)
	require.False(t, veto)
	require.Nil(t, serviceAccount.OverloadUntil)
	fresh := svc.accountRepo.(groupListingRepo).accountsByID[41]
	require.NotNil(t, fresh.OverloadUntil)
	fresh.AccountGroups = nil
	serviceAccount, _, _ = svc.GatewayAccountAfterSlot(ctx, pick)
	require.Nil(t, serviceAccount)
	fresh.AccountGroups = []AccountGroup{{GroupID: groupID}}
	fresh.Status = StatusDisabled
	serviceAccount, _, _ = svc.GatewayAccountAfterSlot(ctx, pick)
	require.Nil(t, serviceAccount)
}

type waitGroupReadError struct{ GroupRepository }

func (waitGroupReadError) GetByIDLite(context.Context, int64) (*Group, error) {
	return nil, errors.New("read failed")
}

func TestGatewayWaitRechecksCurrentPrivacyRule(t *testing.T) {
	gid := int64(7)
	a := Account{ID: 41, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: map[string]any{"mixed_scheduling": true}, AccountGroups: []AccountGroup{{GroupID: gid}}}
	svc, _ := newLastResortGatewayFixture(t, []Account{a})
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{41: false}})
	ctx := context.Background()
	pick, err := svc.SelectAccountWithLoadAwareness(ctx, &gid, "", "", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, pick.WaitPlan)
	repo := svc.groupRepo.(*mockGroupRepoForGateway)
	repo.groups[gid].RequirePrivacySet = true
	acc, _, reason := svc.GatewayAccountAfterSlot(ctx, pick)
	require.Nil(t, acc)
	require.Equal(t, "privacy_required", reason)
	repo.groups[gid].RequirePrivacySet = false
	acc, _, _ = svc.GatewayAccountAfterSlot(ctx, pick)
	require.NotNil(t, acc)
	svc.groupRepo = waitGroupReadError{repo}
	acc, _, _ = svc.GatewayAccountAfterSlot(ctx, pick)
	require.Same(t, pick.Account, acc)
	// A group-read error cannot make a confirmed account disable disappear.
	svc.accountRepo.(groupListingRepo).accountsByID[41].Status = StatusDisabled
	acc, _, _ = svc.GatewayAccountAfterSlot(ctx, pick)
	require.Nil(t, acc)
}

func TestGatewayWaitLastResortRechecksLiveRequestLimits(t *testing.T) {
	group := int64(7)
	svc, _ := newLastResortGatewayFixture(t, []Account{{
		ID: 41, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Extra: map[string]any{"base_rpm": 10, "window_cost_limit": 10.0}, AccountGroups: []AccountGroup{{GroupID: group}},
	}})
	svc.cfg.Gateway.Scheduling.LoadBatchEnabled = true
	limits := newResilienceGateLimits()
	limits.windowCost[41] = 1
	limits.rpm[41] = 1
	svc.rpmCache = limits
	svc.sessionLimitCache = limits
	svc.usageLogRepo = &struct{ UsageLogRepository }{}
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{41: false}})
	ctx := context.Background()
	require.NoError(t, svc.resilience.store.AddSessionAvoid(ctx, group, "sess", 41, time.Minute))
	pick, err := svc.SelectAccountWithLoadAwareness(ctx, &group, "sess", "claude-sonnet-4", nil, "", 0)
	require.NoError(t, err)
	require.NotNil(t, pick.WaitPlan)
	require.True(t, pick.WaitPlan.gatewayEligibility.lastResort)
	limits.rpm[41] = 100
	got, _, reason := svc.GatewayAccountAfterSlot(ctx, pick)
	require.Nil(t, got)
	require.Equal(t, "rpm_limit", reason)
	limits.rpm[41] = 1
	limits.windowCost[41] = 100
	got, _, reason = svc.GatewayAccountAfterSlot(ctx, pick)
	require.Nil(t, got)
	require.Equal(t, "window_cost", reason)
}
