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
	lease, acquired, err := repo.TryAcquireConsumerLock(ctx)
	require.NoError(t, err)
	require.True(t, acquired)
	defer lease.Release()
	events, err := lease.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, largeID, events[0].ID)
	// Commit between the read and acknowledgement, the most dangerous order:
	// acknowledging ID 2 must not delete now-visible but unhandled ID 1.
	require.NoError(t, first.Commit())
	require.NoError(t, lease.DeleteProcessed(ctx, []int64{largeID}))
	events, err = lease.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, smallID, events[0].ID)
	require.NoError(t, lease.DeleteProcessed(ctx, []int64{smallID}))
	events, err = lease.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Empty(t, events)
	require.NoError(t, lease.Commit())
}

func TestSchedulerOutboxTerminatedTransactionReplaysAndReleasesLock(t *testing.T) {
	_ = testEntClient(t)
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, "TRUNCATE scheduler_outbox")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "INSERT INTO scheduler_outbox (event_type) VALUES ('full_rebuild')")
	require.NoError(t, err)
	repo := NewSchedulerOutboxRepository(integrationDB)
	lease, acquired, err := repo.TryAcquireConsumerLock(ctx)
	require.NoError(t, err)
	require.True(t, acquired)
	defer lease.Release()
	tx := lease.(*schedulerOutboxConsumerLease).tx
	var idle, statement string
	require.NoError(t, tx.QueryRowContext(ctx, "SHOW idle_in_transaction_session_timeout").Scan(&idle))
	require.NoError(t, tx.QueryRowContext(ctx, "SHOW statement_timeout").Scan(&statement))
	require.Equal(t, "1min", idle)
	require.Equal(t, "1min", statement)
	events, err := lease.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NoError(t, lease.DeleteProcessed(ctx, []int64{events[0].ID}))
	var pid int
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
	var terminated bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&terminated))
	require.True(t, terminated)
	require.Error(t, lease.Commit())
	next, acquired, err := repo.TryAcquireConsumerLock(ctx)
	require.NoError(t, err)
	require.True(t, acquired)
	defer next.Release()
	replay, err := next.ListPendingAndReleaseDedup(ctx, 200)
	require.NoError(t, err)
	require.Len(t, replay, 1)
	require.Equal(t, events[0].ID, replay[0].ID, "uncommitted acknowledgement rolls back")
	require.NoError(t, next.DeleteProcessed(ctx, []int64{replay[0].ID}))
	require.NoError(t, next.Commit())
}
