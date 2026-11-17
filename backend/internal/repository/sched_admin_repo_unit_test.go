//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestSchedAdminRepositoryLast60NullableAndVersioned(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newAccountRepositoryWithSQL(nil, db, nil)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(schedAdminLatencySQL)).WithArgs(pq.Array([]int64{1, 2}), now.Add(-time.Hour), now).WillReturnRows(sqlmock.NewRows([]string{"group_id", "e50", "e90", "b50", "b90", "requests"}).AddRow(1, 120.0, 200.0, nil, nil, 4))
	mock.ExpectQuery(regexp.QuoteMeta(schedAdminMigrationSQL)).WithArgs(pq.Array([]int64{1, 2}), now.Add(-time.Hour), now, int16(schedquality.ClassMigration400)).WillReturnRows(sqlmock.NewRows([]string{"group_id", "n"}).AddRow(1, 0))
	out, err := repo.SchedAdminGroupLast60(context.Background(), []int64{1, 2}, now)
	require.NoError(t, err)
	require.EqualValues(t, 4, *out[1].Requests)
	require.EqualValues(t, 0, *out[1].Migration400)
	require.Nil(t, out[1].BodyFirstOutputP50Ms)
	raw, err := json.Marshal(out[2])
	require.NoError(t, err)
	require.JSONEq(t, `{"e2e_first_output_p50_ms":null,"e2e_first_output_p90_ms":null,"body_first_output_p50_ms":null,"body_first_output_p90_ms":null,"requests":null,"migration_400":null}`, string(raw))
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestSchedAdminRepositoryDecisionJSONLegacyAndUntil(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newAccountRepositoryWithSQL(nil, db, nil)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	until := at.Add(time.Minute)
	rows := sqlmock.NewRows([]string{"id", "at", "group_id", "model", "line", "action", "level", "until", "basis"})
	rows.AddRow(6, at, 2, "a", "ttfo", "trial", 1, until, `{"rule_version":"3.0","line":"ttfo","kind":"review","threshold":10,"ttft":{"limit_s":10}}`)
	rows.AddRow(5, at, 2, "a", "error", "renew", 2, until, `{"line":"error","error":{"limit":0.05}}`)
	rows.AddRow(4, at, 2, "a", "cache", "release", 1, until, `{"line":"cache","cache":{"min_ratio":0.8}}`)
	rows.AddRow(3, at, 2, "a", "", "enter", 1, until, `{"old":"not a v2 basis"}`)
	rows.AddRow(2, at, 2, "a", "ttfo", "trial_fail", 2, until, `{"line":"ttfo"}`)
	mock.ExpectQuery(regexp.QuoteMeta(schedAdminDecisionsSQL)).WithArgs(int64(1), int64(7), 50).WillReturnRows(rows)
	out, err := repo.SchedAdminDecisions(context.Background(), 1, 50, 7)
	require.NoError(t, err)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	var items []map[string]any
	require.NoError(t, json.Unmarshal(raw, &items))
	require.Equal(t, until.Format(time.RFC3339Nano), items[0]["until"])
	require.Equal(t, until.Format(time.RFC3339Nano), items[1]["until"])
	require.Nil(t, items[2]["until"])
	require.Nil(t, items[3]["until"])
	require.NotContains(t, items[3], "basis")
	require.Equal(t, until.Format(time.RFC3339Nano), items[4]["until"])
	basis := items[0]["basis"].(map[string]any)
	require.NotContains(t, basis, "cut")
	require.Equal(t, []any{}, basis["source_groups"])
	ev := basis["ttft"].(map[string]any)
	require.Nil(t, ev["p_rel"])
	require.Nil(t, ev["u"])
	require.Nil(t, ev["z"])
	require.Equal(t, []any{}, ev["causes"])
	require.NotContains(t, items[1]["basis"].(map[string]any)["error"], "verdict")
	require.NotContains(t, items[2]["basis"].(map[string]any)["cache"], "read_tokens")
	require.NoError(t, mock.ExpectationsWereMet())
}
