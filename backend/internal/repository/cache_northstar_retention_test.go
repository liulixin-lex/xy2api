//go:build unit

package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

var (
	northstarRetentionRollupCutoff = time.Date(2026, 4, 28, 4, 30, 0, 0, time.UTC)
	northstarRetentionTraceCutoff  = time.Date(2026, 7, 27, 4, 30, 0, 0, time.UTC)
)

func expectNorthstarRetentionProbe(mock sqlmock.Sqlmock, hourly, hours, events, trace bool) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM cache_northstar_hourly WHERE bucket_start<$1),
 EXISTS(SELECT 1 FROM cache_northstar_hours WHERE bucket_start<$1),
 EXISTS(SELECT 1 FROM cache_observability_events WHERE at<$1),
 EXISTS(SELECT 1 FROM cache_trace_hourly WHERE bucket_start<$2)`)).
		WithArgs(northstarRetentionRollupCutoff, northstarRetentionTraceCutoff).
		WillReturnRows(sqlmock.NewRows([]string{"hourly", "hours", "events", "trace"}).AddRow(hourly, hours, events, trace))
}

func northstarRetentionDelete(table, column string) string {
	return regexp.QuoteMeta("DELETE FROM " + table + " WHERE ctid = ANY(ARRAY(SELECT ctid FROM " + table + " WHERE " + column + "<$1 LIMIT 5000))")
}

func TestCacheNorthstarRetentionProbeOnlyWithoutExpiredRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	expectNorthstarRetentionProbe(mock, false, false, false, false)
	// Any DELETE would be an unexpected statement.
	require.NoError(t, NewCacheNorthstarRepository(db).CleanupCacheNorthstar(context.Background(), northstarRetentionRollupCutoff, northstarRetentionTraceCutoff))
	require.NoError(t, mock.ExpectationsWereMet())
	mock.ExpectClose()
}

func TestCacheNorthstarRetentionBatchesOnlyExpiredTables(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	expectNorthstarRetentionProbe(mock, true, false, true, true)
	hourly := northstarRetentionDelete("cache_northstar_hourly", "bucket_start")
	mock.ExpectExec(hourly).WithArgs(northstarRetentionRollupCutoff).WillReturnResult(sqlmock.NewResult(0, 5000))
	mock.ExpectExec(hourly).WithArgs(northstarRetentionRollupCutoff).WillReturnResult(sqlmock.NewResult(0, 12))
	mock.ExpectExec(northstarRetentionDelete("cache_observability_events", "at")).WithArgs(northstarRetentionRollupCutoff).WillReturnResult(sqlmock.NewResult(0, 3))
	trace := northstarRetentionDelete("cache_trace_hourly", "bucket_start")
	mock.ExpectExec(trace).WithArgs(northstarRetentionTraceCutoff).WillReturnResult(sqlmock.NewResult(0, 5000))
	mock.ExpectExec(trace).WithArgs(northstarRetentionTraceCutoff).WillReturnResult(sqlmock.NewResult(0, 5000))
	mock.ExpectExec(trace).WithArgs(northstarRetentionTraceCutoff).WillReturnResult(sqlmock.NewResult(0, 0))
	require.NoError(t, NewCacheNorthstarRepository(db).CleanupCacheNorthstar(context.Background(), northstarRetentionRollupCutoff, northstarRetentionTraceCutoff))
	require.NoError(t, mock.ExpectationsWereMet())
	mock.ExpectClose()
}

func TestCacheNorthstarRetentionStopsAtFirstError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	repo := NewCacheNorthstarRepository(db)
	probe := errors.New("synthetic probe failure")
	mock.ExpectQuery("SELECT EXISTS").WillReturnError(probe)
	require.ErrorIs(t, repo.CleanupCacheNorthstar(context.Background(), northstarRetentionRollupCutoff, northstarRetentionTraceCutoff), probe)

	expectNorthstarRetentionProbe(mock, false, true, false, true)
	failure := errors.New("synthetic delete failure")
	mock.ExpectExec(northstarRetentionDelete("cache_northstar_hours", "bucket_start")).WithArgs(northstarRetentionRollupCutoff).WillReturnError(failure)
	require.ErrorIs(t, repo.CleanupCacheNorthstar(context.Background(), northstarRetentionRollupCutoff, northstarRetentionTraceCutoff), failure)
	require.NoError(t, mock.ExpectationsWereMet())
	mock.ExpectClose()
}
