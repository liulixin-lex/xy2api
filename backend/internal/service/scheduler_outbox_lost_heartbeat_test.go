package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSchedulerOutboxStopsHeartbeatsAfterFirstLeaseLoss(t *testing.T) {
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1}, {ID: 2}, {ID: 3}},
		rows:   []int64{1, 2, 3}, leaseErr: errors.New("lease expired"),
	}
	NewSchedulerSnapshotService(&outboxCleanupCache{}, repo, nil, nil, nil).pollOutbox()
	require.Equal(t, 1, repo.heartbeats, "later events and final acknowledgement must not retry a lost lease")
	require.Empty(t, repo.events)
	require.Equal(t, [][]int64{{1, 2, 3}}, repo.deleteCalls, "lease loss must not suppress acknowledgement")
	require.Equal(t, 1, repo.releaseCount)
}
