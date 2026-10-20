package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestShareAccountAttemptsCapsByLargestGroups: the failed-attempt groups are
// ordered before the row limit, largest first and then by every grouping
// column, so a truncated read keeps the most attempts and is repeatable.
func TestShareAccountAttemptsCapsByLargestGroups(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SET LOCAL statement_timeout = '5s'`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`FROM usage_logs`).WillReturnRows(sqlmock.NewRows([]string{"account_id", "count"}).AddRow(1, 3))
	mock.ExpectQuery(`(?s)FROM ops_error_logs.*GROUP BY 1, 2, 3, 4, 5\s+ORDER BY 6 DESC, 1, 2, 3, 4, 5\s+LIMIT 50000$`).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "kind", "status", "category", "message", "count"}).
			AddRow(1, "failover", 502, "TRANSIENT", "upstream failed", 2))
	mock.ExpectRollback()

	now := time.Now()
	got, err := NewShareSchedulingRepository(db).AccountAttempts(context.Background(), []int64{1}, now.Add(-30*time.Minute), now)
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{1: 3}, got.Successes)
	require.Len(t, got.Failures, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}
