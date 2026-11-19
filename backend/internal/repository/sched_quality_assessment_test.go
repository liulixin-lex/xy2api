//go:build unit

package repository

import (
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestSchedAssessmentMigrationAndRestart(t *testing.T) {
	db := newTrialPostgres(t)
	ctx := t.Context()
	_, err := db.ExecContext(ctx, `CREATE TABLE accounts(id bigint PRIMARY KEY);
		CREATE TABLE groups(id bigint PRIMARY KEY); CREATE TABLE sched_decisions(action text);
		INSERT INTO accounts VALUES(1); INSERT INTO groups VALUES(2)`)
	require.NoError(t, err)
	migrate := func(name string) {
		t.Helper()
		m, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		_, err = tx.ExecContext(ctx, string(m))
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
	}
	migrate("315_sched_line_states.sql")
	at := time.Date(2026, 1, 2, 3, 0, 0, 123456000, time.UTC)
	_, err = db.ExecContext(ctx, `INSERT INTO sched_line_states(account_id,model,group_id,line,evaluated_at) VALUES(1,'m',2,'cache',$1)`, at)
	require.NoError(t, err)
	migrate("321_sched_line_last_assessment.sql")
	read := func() q.LineState {
		t.Helper()
		rows, err := loadSchedQualityStates(ctx, db)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		return rows[0]
	}
	save := func(state q.LineState, commit bool) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		store := schedQualityTx{ctx: ctx, tx: tx}
		require.NoError(t, store.Upsert([]q.LineState{state}))
		if commit {
			require.NoError(t, tx.Commit())
		}
	}
	eval := func(prev q.LineState, now time.Time, turns int) q.LineState {
		b := q.Basis{Line: q.DimensionCache, Cache: &q.CacheAssessment{Turns: turns, LowSessions: 1, MinRatio: .8, Ratio: .5}}
		next, _ := q.Step(prev, q.EventRegular, q.VerdictUnknown, b, now)
		return next
	}

	old := read()
	require.Nil(t, old.LastAssessment)
	require.Nil(t, service.SchedAdminLastEvalFromState(old))
	first := eval(old, at.Add(q.RegularInterval), 7)
	save(first, true)
	restarted := read()
	require.Equal(t, first.LastAssessment, restarted.LastAssessment)
	require.NotNil(t, service.SchedAdminLastEvalFromState(restarted))
	second := eval(restarted, at.Add(2*q.RegularInterval), 11)
	save(second, false)
	require.Equal(t, first.LastAssessment, read().LastAssessment)
	save(second, true)
	after := read()
	require.Equal(t, second.LastAssessment, after.LastAssessment)
	latest := service.SchedAdminLastEvalFromState(after)
	require.NotNil(t, latest)
	require.EqualValues(t, 2, latest.Count)
	require.Equal(t, first.EvaluatedAt.Format(time.RFC3339Nano), latest.Since)
	require.Equal(t, new(11.0), latest.Have)
	require.Equal(t, new(20), latest.Need)

	// Older binaries only mention known columns. The stale segment is still
	// queryable in SQL, but the API must wait for a new real assessment.
	legacyAt := after.EvaluatedAt.Add(time.Microsecond)
	clockAt := legacyAt.Add(95 * time.Second)
	_, err = db.ExecContext(ctx, `UPDATE sched_line_states SET evaluated_at=$1, admissions=1, last_rear=$2`, legacyAt, clockAt.Add(-q.LevelClearQuiet))
	require.NoError(t, err)
	stale := read()
	require.Equal(t, after.LastAssessment, stale.LastAssessment)
	require.Nil(t, service.SchedAdminLastEvalFromState(stale))
	cleared, action := q.Step(stale, q.EventClock, q.VerdictUnknown, q.Basis{}, clockAt)
	require.Equal(t, q.ActionClear, action)
	save(cleared, true)
	cleared = read()
	require.True(t, clockAt.Equal(cleared.EvaluatedAt))
	require.Nil(t, service.SchedAdminLastEvalFromState(cleared))
	fresh := eval(cleared, at.Add(4*q.RegularInterval), 12)
	save(fresh, true)
	require.EqualValues(t, 1, service.SchedAdminLastEvalFromState(read()).Count)

	// Mix a long action, several simultaneous causes, and fractional evidence,
	// then round-trip the entire eight-segment history (oldest to newest).
	b := q.Basis{Line: q.DimensionTTFO, PeerKnown: 9.123456789123456,
		TTFT: &q.TTFTEvidence{Causes: []q.RuleBUnknownReason{q.RuleBPeerKnownInsufficient, q.RuleBMassInsufficient, q.RuleBRelativeVeto}}}
	full := q.LineState{LineKey: q.LineKey{AccountID: 1, GroupID: 2, Model: "m", Line: q.DimensionTTFO}}
	for i := 0; i < 8; i++ {
		now := at.Add(time.Duration(i) * q.RegularInterval)
		kind, action := q.EventReview, q.ActionTrial
		if i%2 == 1 {
			kind, action = q.EventTrial, q.ActionTrialTimeout
		}
		full.LastAssessment = q.RecordAssessment(full, now, kind, q.VerdictUnknown, action, b)
		full.EvaluatedAt = now
	}
	save(full, true)
	rows, err := loadSchedQualityStates(ctx, db)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, full.LastAssessment, rows[1].LastAssessment)
	// Compare the initially published DTO with the committed PostgreSQL row,
	// including ties in both directions and a carry into the next second.
	for _, ns := range []time.Duration{123456789, 123456500, 123457500, 500, 1500, 999999500} {
		now := at.Truncate(time.Second).Add(ns)
		relaxed := eval(q.Relaxed(old.LineKey), now, 3)
		before := service.SchedAdminLastEvalFromState(relaxed)
		require.NotNil(t, before)
		save(relaxed, true)
		rows, err := loadSchedQualityStates(ctx, db)
		require.NoError(t, err)
		last := service.SchedAdminLastEvalFromState(rows[0])
		require.NotNil(t, last)
		require.Equal(t, rows[0].EvaluatedAt.UTC().Format(time.RFC3339Nano), last.At)
		require.Equal(t, before, last)
		require.Equal(t, last.At, last.Since)
		next := eval(rows[0], at.Add(q.RegularInterval), 8)
		continued := service.SchedAdminLastEvalFromState(next)
		require.NotNil(t, continued)
		require.EqualValues(t, 2, continued.Count)
		require.Equal(t, before.Since, continued.Since)
		save(next, true)
		rows, err = loadSchedQualityStates(ctx, db)
		require.NoError(t, err)
		require.Equal(t, continued, service.SchedAdminLastEvalFromState(rows[0]))
	}
	var bytes int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT pg_column_size(last_assessment) FROM sched_line_states WHERE line='ttfo'`).Scan(&bytes))
	require.LessOrEqual(t, bytes, 2048)
	t.Logf("eight mixed segments in PostgreSQL JSONB: %d bytes", bytes)
}
