//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSchedulerOutboxCommittedQueueSurvivesLateSmallID(t *testing.T) {
	_ = testEntClient(t)
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox")
	require.NoError(t, err)
	first, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = first.Rollback() }()
	var smallID, largeID int64
	require.NoError(t, first.QueryRowContext(ctx, "INSERT INTO scheduler_outbox (event_type) VALUES ('full_rebuild') RETURNING id").Scan(&smallID))
	// The second transaction commits while the first still owns the smaller ID.
	require.NoError(t, integrationDB.QueryRowContext(ctx, "INSERT INTO scheduler_outbox (event_type) VALUES ('full_rebuild') RETURNING id").Scan(&largeID))
	require.Greater(t, largeID, smallID)
	repo := NewSchedulerOutboxRepository(integrationDB)
	events, err := repo.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, largeID, events[0].ID)
	// Commit between the read and acknowledgement, the most dangerous order:
	// acknowledging ID 2 must not delete now-visible but unhandled ID 1.
	require.NoError(t, first.Commit())
	require.NoError(t, repo.DeleteProcessed(ctx, []int64{largeID}))
	events, err = repo.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, smallID, events[0].ID)
	require.NoError(t, repo.DeleteProcessed(ctx, []int64{smallID}))
	events, err = repo.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Empty(t, events)
}
