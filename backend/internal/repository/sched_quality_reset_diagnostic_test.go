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
	require.Equal(t, at.Add(q.TrialDuration), trial.TrialUntil)
	trial.Basis.RuleVersion = "legacy-rule"
	encoded, err := json.Marshal(trial.Basis)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO sched_line_states VALUES($1,$2,$3,$4,$5,$6,$7)`, trial.AccountID, trial.Model, trial.GroupID, trial.Line, trial.Identity, trial.Trial, string(encoded))
	require.NoError(t, err)
	service.LogSchedQualityDecision(ctx, q.Decision{At: at, Key: line.LineKey, Action: action, Basis: *trial.Basis})
	require.Equal(t, 1, strings.Count(output.String(), `"msg":"sched_quality.missing_deadline"`))
	output.Reset()
	_, _, err = (&schedQualityRepository{db: db}).RunReset(ctx, 1)
	require.NoError(t, err)
	// Keep historical evidence, but do not re-emit the old action's anomaly.
	require.Equal(t, 1, strings.Count(output.String(), `"msg":"sched_quality.state"`))
	require.Contains(t, output.String(), `"action":"reset"`)
	require.NotContains(t, output.String(), `"msg":"sched_quality.missing_deadline"`)
	require.NotContains(t, output.String(), `"msg":"sched_quality.history_missing"`)
	var recorded q.Basis
	var raw []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT basis FROM sched_decisions WHERE account_id=1 AND action='reset'`).Scan(&raw))
	require.NoError(t, json.Unmarshal(raw, &recorded))
	require.False(t, recorded.MissingDeadline)
	require.NotContains(t, string(raw), "missing_deadline")
	require.True(t, recorded.HistoryMissing)
	require.Equal(t, q.RuleVersion, recorded.RuleVersion)
	var version string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT rule_version FROM sched_decisions WHERE account_id=1 AND action='reset'").Scan(&version))
	require.Equal(t, q.RuleVersion, version)
}

func TestSchedQualityResetDropsPriorActionFields(t *testing.T) {
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
	expected := make(map[int64]q.Basis)
	for i, reason := range []string{"fail", "good", "deadline"} {
		basis := q.Basis{
			RuleVersion: "3.1", Line: q.Lines[i], Kind: "trial",
			ReleaseReason: reason, TrialDurationSeconds: 1200, MissingDeadline: true,
			HistoryMissing: true, Threshold: .8, SourceGroups: []int64{7, 8},
			SampleCounts: q.NewSampleCounts(40, 52),
		}
		switch basis.Line {
		case q.DimensionTTFO:
			basis.TTFT = &q.TTFTEvidence{N: 52, K: 20, S: 3, D: 2, LimitSeconds: 5}
		case q.DimensionError:
			basis.Error = &q.ErrorAssessment{Clear: 52, Failed: 7, Limit: .05}
		case q.DimensionCache:
			basis.Cache = &q.CacheAssessment{Turns: 40, Ratio: .95, MinRatio: .8, LowSessions10: new(0)}
		}
		raw, err := json.Marshal(basis)
		require.NoError(t, err)
		group := int64(i + 7)
		// Any restricted legacy row can carry old action metadata. Also seed a
		// normal row: reset deletes it without creating a restriction decision.
		for _, restricted := range []bool{true, false} {
			identity, trial, g := q.IdentityUnknown, false, group+10
			if restricted {
				identity, trial, g = q.IdentityRear, false, group
				if reason == "deadline" {
					identity, trial = q.IdentityUnknown, true
				}
			}
			_, err = db.ExecContext(ctx, "INSERT INTO sched_line_states VALUES($1,$2,$3,$4,$5,$6,$7)",
				1, "synthetic", g, basis.Line, identity, trial, string(raw))
			require.NoError(t, err)
		}
		basis.RuleVersion, basis.ReleaseReason, basis.TrialDurationSeconds, basis.MissingDeadline = q.RuleVersion, "", 0, false
		expected[group] = basis
	}
	_, groups, err := (&schedQualityRepository{db: db}).RunReset(ctx, 1)
	require.NoError(t, err)
	require.Len(t, groups, 6)
	var remaining int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM sched_line_states WHERE account_id=1").Scan(&remaining))
	require.Zero(t, remaining)
	rows, err := db.QueryContext(ctx, "SELECT group_id,rule_version,basis FROM sched_decisions WHERE account_id=1 AND action='reset' ORDER BY group_id")
	require.NoError(t, err)
	defer rows.Close()
	count := 0
	for rows.Next() {
		var group int64
		var version string
		var raw []byte
		require.NoError(t, rows.Scan(&group, &version, &raw))
		require.Equal(t, q.RuleVersion, version)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &fields))
		for _, key := range []string{"release_reason", "trial_duration_seconds", "missing_deadline"} {
			require.NotContains(t, fields, key)
		}
		var basis q.Basis
		require.NoError(t, json.Unmarshal(raw, &basis))
		require.Equal(t, expected[group], basis, "retain readings, counts, threshold, source groups and historical diagnostics")
		count++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 3, count)
	decoder := json.NewDecoder(&output)
	count = 0
	for decoder.More() {
		var record map[string]any
		require.NoError(t, decoder.Decode(&record))
		require.Equal(t, "sched_quality.state", record["msg"])
		require.Equal(t, "reset", record["action"])
		require.NotContains(t, record, "release_reason")
		require.NotContains(t, record, "trial_duration_seconds")
		count++
	}
	require.Equal(t, 3, count)
}
