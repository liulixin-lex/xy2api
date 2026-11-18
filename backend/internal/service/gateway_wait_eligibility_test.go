//go:build unit

package service

import (
	"context"
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
