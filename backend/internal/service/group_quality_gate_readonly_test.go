package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGroupQualityPureSnapshotBoundedAndIsolated(t *testing.T) {
	now := time.Now().UTC()
	metric := 12.5
	svc := NewGroupQualityGateService(nil, nil, nil)
	svc.snapshot.Store(&groupQualitySnapshot{GeneratedAt: now, Policies: GroupQualityGatePolicies{Version: 4, Groups: map[string]GroupQualityGatePolicy{"8": {V3: &GroupQualityGateV3Policy{Enabled: true}}}}, Members: map[int64]map[int64]groupQualityMemberSnapshot{8: {2: {AccountID: 2, ExpectedP90Ms: &metric}, 3: {AccountID: 3, ExpectedP90Ms: &metric}}, 9: {4: {AccountID: 4, ExpectedP90Ms: &metric}}}})
	first := svc.PureSnapshot(0, 0, 0, 0, 1, true)
	require.NotNil(t, first)
	require.Len(t, first.Items, 1)
	require.True(t, first.Truncated)
	require.EqualValues(t, 8, first.Items[0].GroupID)
	require.EqualValues(t, 2, first.Items[0].Account.AccountID)
	*first.Items[0].Account.TTFT.Value = 99
	first.Items[0].Policy.Enabled = false
	again := svc.PureSnapshot(8, 2, 0, 0, 100, true)
	require.Equal(t, 12.5, *again.Items[0].Account.TTFT.Value)
	require.True(t, again.Items[0].Policy.Enabled)
	next := svc.PureSnapshot(0, 0, 8, 2, 100, false)
	require.Len(t, next.Items, 2)
	require.EqualValues(t, 3, next.Items[0].Account.AccountID)
	require.Nil(t, next.Items[0].Policy)
	require.Nil(t, svc.PureSnapshot(0, 0, 0, 0, 101, true))
	svc.snapshot.Store(&groupQualitySnapshot{GeneratedAt: now.Add(-3 * time.Minute), Members: map[int64]map[int64]groupQualityMemberSnapshot{}})
	require.True(t, svc.PureSnapshot(0, 0, 0, 0, 20, false).Stale)
}
