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
	"github.com/liulixin-lex/xy2api/internal/service"
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
	key := schedquality.LineKey{AccountID: 1, GroupID: 2, Model: "a", Line: schedquality.DimensionTTFO}
	basis := schedquality.Basis{RuleVersion: schedquality.RuleVersion, Line: key.Line, Kind: "review", Threshold: 10, TTFT: &schedquality.TTFTEvidence{LimitSeconds: 10}}
	entered, enterAction := schedquality.Step(schedquality.Relaxed(key), schedquality.EventRegular, schedquality.VerdictFail, basis, at)
	require.Equal(t, schedquality.ActionEnter, enterAction)
	renewed, renewAction := schedquality.Step(entered, schedquality.EventReview, schedquality.VerdictFail, basis, entered.Until)
	require.Equal(t, schedquality.ActionRenew, renewAction)
	trial, trialAction := schedquality.Step(entered, schedquality.EventReview, schedquality.VerdictUnknown, basis, entered.Until)
	require.Equal(t, schedquality.ActionTrial, trialAction)
	require.True(t, trial.Until.IsZero())
	require.False(t, trial.TrialUntil.IsZero())
	failed, failAction := schedquality.Step(trial, schedquality.EventTrial, schedquality.VerdictFail, basis, trial.EvaluatedAt.Add(time.Minute))
	require.Equal(t, schedquality.ActionTrialFail, failAction)
	require.True(t, failed.TrialUntil.IsZero())
	reviewed, reviewAction := schedquality.Step(entered, schedquality.EventReview, schedquality.VerdictPass, basis, entered.Until)
	require.Equal(t, schedquality.ActionTrial, reviewAction)
	require.Equal(t, trial.TrialUntil, reviewed.TrialUntil)
	// Historical direct-release rows remain readable although recovery 3.1 no
	// longer creates them. Keep this decoder fixture independent of new Step.
	legacyBasis := basis
	legacyBasis.RuleVersion = "3.0"
	released := schedquality.LineState{LineKey: key, Identity: schedquality.IdentityPass, EvaluatedAt: entered.Until, Basis: &legacyBasis}
	releaseAction := schedquality.ActionRelease
	passed, passAction := schedquality.Step(trial, schedquality.EventTrial, schedquality.VerdictPass, basis, trial.Epoch.Add(schedquality.TrialMinimum))
	require.Equal(t, schedquality.ActionTrialPass, passAction)
	timedOut, timeoutAction := schedquality.Step(trial, schedquality.EventTrial, schedquality.VerdictUnknown, basis, trial.TrialUntil)
	require.Equal(t, schedquality.ActionTrialTimeout, timeoutAction)
	// Supply the real Step deadlines as persisted by the engine's action-specific mapping.
	cases := []struct {
		state  schedquality.LineState
		action schedquality.Action
		until  time.Time
	}{
		{trial, trialAction, trial.TrialUntil}, {failed, failAction, failed.Until}, {entered, enterAction, entered.Until}, {renewed, renewAction, renewed.Until},
		{released, releaseAction, time.Time{}}, {passed, passAction, time.Time{}}, {timedOut, timeoutAction, time.Time{}},
	}
	rows := sqlmock.NewRows([]string{"id", "at", "group_id", "model", "line", "action", "level", "until", "basis"})
	for i, c := range cases {
		raw, err := json.Marshal(c.state.Basis)
		require.NoError(t, err)
		var until any
		if !c.until.IsZero() {
			until = c.until
		}
		rows.AddRow(10-i, c.state.EvaluatedAt, 2, "a", "ttfo", string(c.action), c.state.Level, until, raw)
	}
	rows.AddRow(1, at, 2, "a", "", "enter", 1, entered.Until, `{"old":"not a v2 basis"}`)
	mock.ExpectQuery(regexp.QuoteMeta(schedAdminDecisionsSQL)).WithArgs(int64(1), int64(11), 50).WillReturnRows(rows)
	out, err := repo.SchedAdminDecisions(context.Background(), 1, 50, 11)
	require.NoError(t, err)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	var items []map[string]any
	require.NoError(t, json.Unmarshal(raw, &items))
	for i, c := range cases {
		require.Equal(t, string(c.action), items[i]["action"])
		if c.until.IsZero() {
			require.Nil(t, items[i]["until"])
		} else {
			require.Equal(t, c.until.Format(time.RFC3339Nano), items[i]["until"])
		}
	}
	require.Equal(t, "3.0", items[4]["basis"].(map[string]any)["rule_version"])
	require.Equal(t, "good", items[5]["basis"].(map[string]any)["release_reason"])
	require.EqualValues(t, 900, items[5]["basis"].(map[string]any)["trial_duration_seconds"])
	require.Equal(t, "deadline", items[6]["basis"].(map[string]any)["release_reason"])
	require.EqualValues(t, 3600, items[6]["basis"].(map[string]any)["trial_duration_seconds"])
	require.Nil(t, items[len(cases)]["until"])
	require.NotContains(t, items[len(cases)], "basis")
	b := items[0]["basis"].(map[string]any)
	require.NotContains(t, b, "cut")
	require.Equal(t, []any{}, b["source_groups"])
	ev := b["ttft"].(map[string]any)
	for _, key := range []string{"p_rel", "u", "z"} {
		require.Nil(t, ev[key])
	}
	require.Equal(t, []any{}, ev["causes"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSchedAdminRepositoryDecisionsUsePersistedUntil(t *testing.T) {
	// Arbitrary persisted values prove the reader does not reconstruct deadlines from at.
	db, mock := newSQLMock(t)
	repo := newAccountRepositoryWithSQL(nil, db, nil)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	until := at.Add(17 * time.Second)
	rows := sqlmock.NewRows([]string{"id", "at", "group_id", "model", "line", "action", "level", "until", "basis"})
	actions := []string{"trial", "trial_fail", "enter", "renew", "release", "trial_pass", "trial_timeout", "reset", "relax", "clear"}
	for i, action := range actions {
		rows.AddRow(20-i, at, 2, "a", "ttfo", action, 1, until, `{"line":"ttfo"}`)
	}
	mock.ExpectQuery(regexp.QuoteMeta(schedAdminDecisionsSQL)).WithArgs(int64(1), int64(0), 50).WillReturnRows(rows)
	out, err := repo.SchedAdminDecisions(context.Background(), 1, 50, 0)
	require.NoError(t, err)
	for i := range actions {
		if i < 4 {
			require.Equal(t, until.Format(time.RFC3339Nano), *out[i].Until)
		} else {
			require.Nil(t, out[i].Until)
		}
	}
	require.NoError(t, mock.ExpectationsWereMet())
	// Non-TTFO bases retain only the fields in the public evidence contract.
	for _, basis := range []schedquality.Basis{{Error: &schedquality.ErrorAssessment{}}, {Cache: &schedquality.CacheAssessment{}}} {
		raw, err := json.Marshal(service.SchedAdminDecisionBasis(basis))
		require.NoError(t, err)
		require.NotContains(t, string(raw), `"verdict"`)
		require.NotContains(t, string(raw), `"read_tokens"`)
	}
}

func TestObserveRearEntriesDistinctAndWindow(t *testing.T) {
	db := observePostgres(t)
	_, err := db.Exec(`CREATE TABLE sched_decisions(at timestamptz, group_id bigint, account_id bigint, model text, action text)`)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	for _, r := range []struct {
		age            time.Duration
		group, account int
		model, action  string
	}{
		{24 * time.Hour, 1, 2, "a", "enter"}, {time.Hour, 1, 2, "a", "renew"}, {time.Minute, 1, 2, "b", "trial_fail"},
		{0, 1, 3, "a", "enter"}, {0, 1, 3, "b", "release"}, {24*time.Hour + time.Second, 1, 4, "a", "enter"},
		{-time.Second, 1, 5, "a", "enter"}, {time.Minute, 2, 2, "a", "enter"}, {time.Minute, 9, 3, "a", "enter"},
	} {
		_, err = db.Exec("INSERT INTO sched_decisions VALUES ($1,$2,$3,$4,$5)", now.Add(-r.age), r.group, r.account, r.model, r.action)
		require.NoError(t, err)
	}
	repo := newAccountRepositoryWithSQL(nil, db, nil)
	got, err := repo.SchedAdminRearEntries24h(context.Background(), []int64{1, 2, 3}, now)
	require.NoError(t, err)
	require.Equal(t, map[int64]service.SchedAdminRearEntries{1: {Decisions: 4, AccountModels: 3}, 2: {Decisions: 1, AccountModels: 1}}, got)
}
