//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func ttftRuleRepoFixture(t *testing.T) *schedQualityRepository {
	t.Helper()
	_, err := integrationDB.Exec(`DELETE FROM settings WHERE key=$1`, service.SettingKeySchedQualityTTFTRule)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.Exec(`DELETE FROM settings WHERE key=$1`, service.SettingKeySchedQualityTTFTRule)
		require.NoError(t, err)
	})
	return &schedQualityRepository{db: integrationDB}
}

func saveTTFTRuleInTx(ctx context.Context, r *schedQualityRepository, version int64, p q.RuleBParams) (result service.SchedTTFTRuleRecord, err error) {
	err = r.RunExclusive(ctx, func(tx service.SchedQualityTx) error { var e error; result, e = tx.SaveTTFTRule(version, p); return e })
	return
}

func TestSchedTTFTRuleSQLRoundTripCASAndRollback(t *testing.T) {
	r := ttftRuleRepoFixture(t)
	ctx := context.Background()
	defaults := q.DefaultRuleBParams()
	got, err := r.LoadTTFTRule(ctx)
	require.NoError(t, err)
	require.Equal(t, service.SchedTTFTRuleRecord{Params: defaults}, got)
	got, err = saveTTFTRuleInTx(ctx, r, 0, defaults)
	require.NoError(t, err)
	require.Zero(t, got.Version)
	next := defaults
	next.Confidence = .9
	saved, err := saveTTFTRuleInTx(ctx, r, 0, next)
	require.NoError(t, err)
	require.EqualValues(t, 1, saved.Version)
	got, err = r.LoadTTFTRule(ctx)
	require.NoError(t, err)
	require.Equal(t, saved, got)
	got, err = saveTTFTRuleInTx(ctx, r, 1, next)
	require.NoError(t, err)
	require.Equal(t, saved, got, "unchanged parameters do not advance the version")
	_, err = saveTTFTRuleInTx(ctx, r, 0, next)
	require.ErrorIs(t, err, service.ErrSchedPolicyConflict, "even a no-op validates the version")
	failure := errors.New("synthetic transaction failure")
	err = r.RunExclusive(ctx, func(tx service.SchedQualityTx) error {
		if _, e := tx.SaveTTFTRule(1, defaults); e != nil {
			return e
		}
		return failure
	})
	require.ErrorIs(t, err, failure)
	got, err = r.LoadTTFTRule(ctx)
	require.NoError(t, err)
	require.Equal(t, saved, got)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() { <-start; _, e := saveTTFTRuleInTx(ctx, r, 1, defaults); results <- e }()
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		e := <-results
		if e == nil {
			successes++
		} else {
			require.ErrorIs(t, e, service.ErrSchedPolicyConflict)
			conflicts++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	got, err = r.LoadTTFTRule(ctx)
	require.NoError(t, err)
	require.Equal(t, service.SchedTTFTRuleRecord{Version: 2, Params: defaults}, got)
}

func waitForSchedSQLBlocker(t *testing.T, ctx context.Context, holder *sql.Tx) {
	t.Helper()
	var pid int
	require.NoError(t, holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	require.Eventually(t, func() bool {
		var blocked bool
		err := holder.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked)
		require.NoError(t, err)
		return blocked
	}, 2*time.Second, 5*time.Millisecond, "observe actual PostgreSQL lock waiting")
}

func TestSchedTTFTRuleWaitsForSettingsRowLock(t *testing.T) {
	r := ttftRuleRepoFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := q.DefaultRuleBParams()
	p.Confidence = .9
	initial, err := saveTTFTRuleInTx(ctx, r, 0, p)
	require.NoError(t, err)
	holder, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer holder.Rollback()
	var raw string
	require.NoError(t, holder.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1 FOR UPDATE`, service.SettingKeySchedQualityTTFTRule).Scan(&raw))
	result := make(chan error, 1)
	// No-op updates still take FOR UPDATE before comparing the persisted version.
	go func() { _, e := saveTTFTRuleInTx(ctx, r, initial.Version, p); result <- e }()
	waitForSchedSQLBlocker(t, ctx, holder)
	select {
	case e := <-result:
		t.Fatalf("save returned before row lock was released: %v", e)
	default:
	}
	got, err := r.LoadTTFTRule(ctx)
	require.NoError(t, err)
	require.Equal(t, initial, got)
	require.NoError(t, holder.Commit())
	require.NoError(t, <-result)
}

func TestSchedRunExclusiveWaitsForCycleLock(t *testing.T) {
	r := ttftRuleRepoFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	holder, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer holder.Rollback()
	_, err = holder.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, schedQualityCycleLockID)
	require.NoError(t, err)
	entered := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- r.RunExclusive(ctx, func(tx service.SchedQualityTx) error {
			entered <- struct{}{}
			_, e := tx.SaveTTFTRule(0, q.DefaultRuleBParams())
			return e
		})
	}()
	waitForSchedSQLBlocker(t, ctx, holder)
	select {
	case <-entered:
		t.Fatal("exclusive callback ran before cycle lock was released")
	default:
	}
	require.NoError(t, holder.Commit())
	require.NoError(t, <-done)
	select {
	case <-entered:
	default:
		t.Fatal("exclusive callback was not run")
	}
}
