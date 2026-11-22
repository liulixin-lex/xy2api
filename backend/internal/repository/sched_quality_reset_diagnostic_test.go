//go:build unit

package repository

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedQualityResetDoesNotRepublishDiagnostics(t *testing.T) {
	db := newTrialPostgres(t)
	ctx := t.Context()
	_, err := db.ExecContext(ctx, `CREATE TABLE accounts(id bigint primary key,extra jsonb);
 CREATE TABLE sched_line_states(account_id bigint,model text,group_id bigint,line text,identity smallint,trial boolean,basis jsonb);
 CREATE TABLE sched_decisions(at timestamptz,rule_version text,account_id bigint,model text,group_id bigint,line text,action text,dims text[],level int,until timestamptz,basis jsonb,attempt_ids text[]);
 INSERT INTO accounts VALUES(1,'{}');`)
	require.NoError(t, err)
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	line := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 7, Model: "synthetic", Line: q.DimensionError}, Identity: q.IdentityRear, EvaluatedAt: at.Add(-time.Second)}
	trial, action := q.Step(line, q.EventReview, q.VerdictUnknown, q.Basis{HistoryMissing: true, Kind: "review"}, at)
	require.Equal(t, q.ActionTrial, action)
	require.True(t, trial.Basis.MissingDeadline)
	require.True(t, trial.Trial)
	require.Equal(t, at.Add(120*time.Minute), trial.TrialUntil)
	encoded, err := json.Marshal(trial.Basis)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO sched_line_states VALUES($1,$2,$3,$4,$5,$6,$7)`, trial.AccountID, trial.Model, trial.GroupID, trial.Line, trial.Identity, trial.Trial, string(encoded))
	require.NoError(t, err)
	service.LogSchedQualityDecision(ctx, q.Decision{At: at, Key: line.LineKey, Action: action, Basis: *trial.Basis})
	require.Equal(t, 1, strings.Count(output.String(), `"msg":"sched_quality.missing_deadline"`))
	output.Reset()
	_, _, err = (&schedQualityRepository{db: db}).RunReset(ctx, 1)
	require.NoError(t, err)
	// Reset still records its decision with the original evidence. Neither
	// persisted flag represents a new diagnostic from this transaction.
	require.Equal(t, 1, strings.Count(output.String(), `"msg":"sched_quality.state"`))
	require.Contains(t, output.String(), `"action":"reset"`)
	require.NotContains(t, output.String(), `"msg":"sched_quality.missing_deadline"`)
	require.NotContains(t, output.String(), `"msg":"sched_quality.history_missing"`)
	var recorded q.Basis
	var raw []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT basis FROM sched_decisions WHERE account_id=1 AND action='reset'`).Scan(&raw))
	require.NoError(t, json.Unmarshal(raw, &recorded))
	require.True(t, recorded.MissingDeadline)
	require.True(t, recorded.HistoryMissing)
}
