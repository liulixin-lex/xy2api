//go:build unit

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedFinishedPageHasIndependentDeadlineAndStrictCursor(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := &schedAttemptRepository{db: db}
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	through := since.Add(time.Hour)
	cursor := service.SchedAttemptCursor{At: since.Add(time.Minute), AttemptID: "00000000-0000-0000-0000-000000000001"}
	columns := strings.FieldsFunc(schedAttemptColumns, func(r rune) bool { return r == ',' || r == '\n' || r == '\t' || r == ' ' })
	for i := 0; i < 2; i++ {
		mock.ExpectBegin()
		mock.ExpectExec("SET LOCAL statement_timeout").WillReturnResult(sqlmock.NewResult(0, 1))
		query := mock.ExpectQuery(`(?s)capture_version >= 2.*finished_at >= \$1 AND finished_at <= \$2.*\(finished_at,attempt_id\) >.*ORDER BY finished_at,attempt_id LIMIT \$5`).WithArgs(since, through, cursor.At, cursor.AttemptID, 1)
		if i == 0 {
			query.WillReturnError(context.DeadlineExceeded)
			mock.ExpectRollback()
		} else {
			query.WillReturnRows(sqlmock.NewRows(columns))
			mock.ExpectCommit()
		}
		rows, e := repo.LoadFinishedPage(context.Background(), since, through, cursor, 1)
		if i == 0 {
			require.ErrorIs(t, e, context.DeadlineExceeded)
		} else {
			require.NoError(t, e)
			require.Empty(t, rows)
		}
	}
	require.NoError(t, mock.ExpectationsWereMet())
}
