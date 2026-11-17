package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestSchedulerOutboxAcquireTimesOutWhenPoolIsExhausted(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	held, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer func() { _ = held.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		lease, acquired, acquireErr := (&schedulerOutboxRepository{db: db}).TryAcquireConsumerLock(ctx)
		if acquired {
			lease.Release()
		}
		done <- acquireErr
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded, "acquisition must have its own budget even when the batch has none")
	case <-time.After(10 * time.Second):
		cancel()
		<-done
		t.Fatal("pool acquisition ignored its timeout")
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSchedulerOutboxAcquireSetupCancellationDoesNotCancelLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	expectOutboxTransaction(mock, true)
	mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"alive"}).AddRow(1))
	mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
	lease, acquired, err := (&schedulerOutboxRepository{db: db}).TryAcquireConsumerLock(context.Background())
	require.NoError(t, err)
	require.True(t, acquired)
	// Setup's context has already been cancelled by its deferred cleanup.
	require.NoError(t, lease.KeepAlive(context.Background()))
	lease.Release()
	lease.Release()
	require.Equal(t, 0, db.Stats().InUse, "Release must return the reserved connection")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSchedulerOutboxReleaseDiscardsConnectionAfterRollbackFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	expectOutboxTransaction(mock, true)
	mock.ExpectExec("ROLLBACK").WillReturnError(errors.New("connection lost"))
	mock.ExpectClose()
	lease, acquired, err := (&schedulerOutboxRepository{db: db}).TryAcquireConsumerLock(context.Background())
	require.NoError(t, err)
	require.True(t, acquired)
	lease.Release()
	lease.Release()
	require.Equal(t, 0, db.Stats().OpenConnections, "failed rollback must discard the physical connection")
	require.NoError(t, mock.ExpectationsWereMet())
	_ = db.Close()
}

func TestSchedulerOutboxAcquireInitializationHasDeadline(t *testing.T) {
	for _, phase := range []string{"begin_and_settings", "lock"} {
		t.Run(phase, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			setup := mock.ExpectExec("BEGIN; SET LOCAL idle_in_transaction_session_timeout = '60s'; SET LOCAL statement_timeout = '60s'")
			if phase == "begin_and_settings" {
				setup.WillDelayFor(time.Minute)
			}
			setup.WillReturnResult(sqlmock.NewResult(0, 0))
			if phase == "lock" {
				mock.ExpectQuery("SELECT pg_try_advisory_xact_lock").WillDelayFor(time.Minute).
					WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
			}
			mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))
			lease, acquired, err := (&schedulerOutboxRepository{db: db}).TryAcquireConsumerLock(context.Background())
			require.ErrorIs(t, err, sqlmock.ErrCancelled, "SQL setup must observe the acquisition deadline")
			require.False(t, acquired)
			require.Nil(t, lease)
			require.Zero(t, db.Stats().InUse)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
