package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupQualityReplayRawLimitsStopBeforeLoadingRows(t *testing.T) {
	for _, tc := range []struct {
		name      string
		errorRows bool
	}{
		{name: "usage rows bound freshness and lag input"},
		{name: "error events bound expansion input", errorRows: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			repo := &groupQualityGateRepository{db: db}
			from := time.Date(2026, 11, 4, 5, 30, 0, 0, time.UTC)
			to := from.Add(7 * time.Hour)
			mock.ExpectBegin()
			mock.ExpectExec("SET LOCAL statement_timeout = '20s'").WillReturnResult(sqlmock.NewResult(0, 0))
			usageRows := int64(service.GroupQualityReplayMaxObservationRows + 1)
			if tc.errorRows {
				usageRows = 0
			}
			mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM usage_logs.*LIMIT`).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(usageRows))
			if tc.errorRows {
				mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*ops_error_logs.*jsonb_array_elements.*LIMIT`).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(service.GroupQualityReplayMaxObservationRows + 1)))
			}
			mock.ExpectRollback()

			_, err = repo.LoadObservationsForReplay(context.Background(), []int64{1}, from, to)
			require.ErrorIs(t, err, errGroupQualityReplayRowLimit)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
