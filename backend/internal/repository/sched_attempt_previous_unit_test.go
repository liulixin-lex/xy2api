//go:build unit

package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

type schedAttemptLookupBatchArg struct {
	count       int
	first, last string
}

func (a schedAttemptLookupBatchArg) Match(value driver.Value) bool {
	data, ok := value.(string)
	if !ok {
		return false
	}
	var rows []struct {
		ID string `json:"attempt_id"`
	}
	return json.Unmarshal([]byte(data), &rows) == nil && len(rows) == a.count &&
		rows[0].ID == a.first && rows[len(rows)-1].ID == a.last
}

func TestSchedPreviousAttemptsBatchBoundary(t *testing.T) {
	for _, failSecond := range []bool{false, true} {
		t.Run(fmt.Sprintf("second_batch_failure=%v", failSecond), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			repo := &schedAttemptRepository{db: db}
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			current := make([]schedquality.AttemptSample, 2001)
			for i := range current {
				current[i] = schedquality.AttemptSample{
					AttemptID: fmt.Sprintf("current-%d", i), StartedAt: start,
					GroupID: 1, Model: "model", SessionKey: "session",
				}
			}
			current = append(current, schedquality.AttemptSample{AttemptID: "no-session"})
			columns := append([]string{"current_attempt_id"}, strings.FieldsFunc(schedAttemptColumns, func(r rune) bool {
				return r == ',' || r == '\n' || r == '\t' || r == ' '
			})...)
			result := func(currentID, previousID string) *sqlmock.Rows {
				return sqlmock.NewRows(columns).AddRow(currentID,
					previousID, start.Add(-time.Second), start, int64(1), int64(1), "model",
					false, nil, nil, 1, "", nil, nil, "session", "", start,
					nil, nil, nil, nil, 0, nil, 0, nil)
			}
			query := `(?s)JOIN LATERAL.*s\.capture_version >= 2`
			mock.ExpectQuery(query).
				WithArgs(schedAttemptLookupBatchArg{2000, "current-0", "current-1999"}, sqlmock.AnyArg()).
				WillReturnRows(result("current-0", "previous-first")).RowsWillBeClosed()
			second := mock.ExpectQuery(query).
				WithArgs(schedAttemptLookupBatchArg{1, "current-2000", "current-2000"}, sqlmock.AnyArg())
			wantErr := errors.New("second batch failed")
			if failSecond {
				second.WillReturnError(wantErr)
			} else {
				second.WillReturnRows(result("current-2000", "previous-last")).RowsWillBeClosed()
			}
			previous, err := repo.LoadPreviousAttempts(context.Background(), current)
			if failSecond {
				require.ErrorIs(t, err, wantErr)
				require.Nil(t, previous, "a partial predecessor map must not escape")
			} else {
				require.NoError(t, err)
				require.Len(t, previous, len(current))
				require.Equal(t, "previous-first", previous["current-0"].AttemptID)
				require.Equal(t, "previous-last", previous["current-2000"].AttemptID)
				require.Nil(t, previous["current-1"])
				require.Nil(t, previous["no-session"])
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
