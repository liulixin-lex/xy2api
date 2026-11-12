//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedTestRepository(t *testing.T) {
	ctx := context.Background()
	repo := NewSchedTestRepository(integrationDB)
	now := time.Now().UTC()
	account := int64(81370001)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_quality_tests WHERE account_id=$1`, account)
	})
	newRecord := func(at time.Time, running bool) service.SchedTestRecord {
		return service.SchedTestRecord{AccountID: account, Model: "test-model", Running: running, TestResult: schedquality.TestResult{ID: uuid.NewString(), StartedAt: at, Done: !running}}
	}
	old := newRecord(now.Add(-schedquality.TestFreshFor-time.Minute), false)
	fresh := newRecord(now.Add(-schedquality.TestFreshFor+time.Minute), false)
	running := newRecord(now, true)
	for _, v := range []service.SchedTestRecord{old, fresh, running} {
		if err := repo.Insert(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Insert(ctx, newRecord(now, true)); !errors.Is(err, service.ErrSchedTestRunning) {
		t.Fatalf("expected running conflict: %v", err)
	}
	rows, err := repo.Since(ctx, now.Add(-schedquality.TestFreshFor))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != fresh.ID || rows[1].ID != running.ID || !rows[1].Running {
		t.Fatalf("unexpected fresh rows: %+v", rows)
	}
	runner := service.NewSchedTestRunnerService(nil, nil, nil, nil, repo, func() time.Time { return now }, nil, nil)
	defer runner.Stop()
	byPair, err := runner.Fresh(ctx)
	require.NoError(t, err)
	require.Equal(t, rows, byPair[service.SchedAccountModel{AccountID: account, Model: running.Model}])

	running.Running = false
	running.Done = true
	running.FinishedAt = now.Add(time.Minute)
	previous, prompt, cached, ttfo := 15000, 16200, 14000, 1234
	running.Turns = []schedquality.TestTurn{{
		Tier: schedquality.TierSmall, Turn: 2, Class: schedquality.ClassLight, Warm: true,
		Outcome: schedquality.OutcomeOK, PromptTokens: &prompt, PrevPromptTokens: &previous,
		CacheReadTokens: &cached, TTFOMs: &ttfo,
	}}
	running.InputTokens, running.CachedTokens, running.OutputTokens = 16200, 14000, 250
	require.NoError(t, repo.Finish(ctx, running))
	latest, err := repo.Latest(ctx, account, running.Model)
	require.NoError(t, err)
	require.Equal(t, running.ID, latest.ID)
	require.True(t, latest.Done)
	require.False(t, latest.Running)
	require.Equal(t, running.Turns, latest.Turns)
	require.Equal(t, running.InputTokens, latest.InputTokens)
	require.Equal(t, running.CachedTokens, latest.CachedTokens)
	require.Equal(t, running.OutputTokens, latest.OutputTokens)
	recent, err := runner.Recent(ctx, account, 2)
	require.NoError(t, err)
	require.Len(t, recent, 2)
	require.Equal(t, running.ID, recent[0].ID)
	require.Equal(t, fresh.ID, recent[1].ID)
}

func TestSchedTestRepositoryReapStale(t *testing.T) {
	ctx := context.Background()
	repo := NewSchedTestRepository(integrationDB)
	now := time.Now().UTC().Truncate(time.Microsecond)
	account := int64(81370002)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_quality_tests WHERE account_id=$1`, account)
	})
	for _, tc := range []struct {
		model   string
		age     time.Duration
		running bool
	}{
		{"stale", schedquality.TestStaleAfter + time.Microsecond, true},
		{"boundary", schedquality.TestStaleAfter, true},
		{"finished", 8 * time.Minute, false},
	} {
		require.NoError(t, repo.Insert(ctx, service.SchedTestRecord{
			AccountID: account, Model: tc.model, Running: tc.running,
			TestResult: schedquality.TestResult{ID: uuid.NewString(), StartedAt: now.Add(-tc.age), Done: !tc.running},
		}))
	}
	runner := service.NewSchedTestRunnerService(nil, nil, nil, nil, repo, func() time.Time { return now }, nil, nil)
	defer runner.Stop()
	require.NoError(t, runner.ReapStale(ctx))
	stale, err := repo.Latest(ctx, account, "stale")
	require.NoError(t, err)
	require.False(t, stale.Running)
	require.False(t, stale.Done)
	require.Equal(t, "timeout", stale.AbortReason)
	require.True(t, now.Equal(stale.FinishedAt))
	boundary, err := repo.Latest(ctx, account, "boundary")
	require.NoError(t, err)
	require.True(t, boundary.Running)
	finished, err := repo.Latest(ctx, account, "finished")
	require.NoError(t, err)
	require.True(t, finished.Done)
	require.Empty(t, finished.AbortReason)
}

func TestSchedTestRepositoryAtomicAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	repo := NewSchedTestRepository(integrationDB)
	account := int64(81370003)
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM account_quality_tests WHERE account_id IN ($1,$2)`, account, account+1)
	})
	newRecord := func(model string, at time.Time) service.SchedTestRecord {
		return service.SchedTestRecord{AccountID: account, Model: model, Running: true, TestResult: schedquality.TestResult{ID: uuid.NewString(), StartedAt: at}}
	}
	t.Run("waiter_rechecks_after_first_test_finishes", func(t *testing.T) {
		const model = "empty-history"
		last, err := repo.Latest(ctx, account, model)
		require.NoError(t, err)
		require.Nil(t, last)
		tx, err := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		lockID := advisoryLockHash(fmt.Sprintf("sched_test:%d:%s", account, model))
		_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lockID)
		require.NoError(t, err)
		result := make(chan error, 1)
		go func() { result <- repo.Insert(ctx, newRecord(model, now)) }()
		// Observe an actual database lock waiter rather than guessing with a sleep.
		var waitErr error
		require.Eventually(t, func() bool {
			var waiting bool
			waitErr = integrationDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND classid=$1 AND objid=$2 AND objsubid=1)`, int64(uint64(lockID)>>32), int64(uint64(lockID)&0xffffffff)).Scan(&waiting)
			return waitErr == nil && waiting
		}, 5*time.Second, 5*time.Millisecond)
		require.NoError(t, waitErr)
		// Other models and accounts must not queue behind this pair's admission lock.
		require.NoError(t, repo.Insert(ctx, newRecord("other-model", now)))
		other := newRecord(model, now)
		other.AccountID++
		require.NoError(t, repo.Insert(ctx, other))
		winner := newRecord(model, now)
		_, err = tx.ExecContext(ctx, `INSERT INTO account_quality_tests (id,account_id,model,trigger,status,started_at,finished_at) VALUES ($1,$2,$3,1,1,$4,$4)`, winner.ID, account, model, now)
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
		require.ErrorIs(t, <-result, service.ErrSchedTestTooSoon)
		last, err = repo.Latest(ctx, account, model)
		require.NoError(t, err)
		require.Equal(t, winner.ID, last.ID)
		require.True(t, last.Done)
		var rows int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_quality_tests WHERE account_id=$1 AND model=$2`, account, model).Scan(&rows))
		require.Equal(t, 1, rows)
		require.ErrorIs(t, repo.Insert(ctx, newRecord(model, now.Add(schedquality.TestRetryEvery-time.Microsecond))), service.ErrSchedTestTooSoon)
		require.NoError(t, repo.Insert(ctx, newRecord(model, now.Add(schedquality.TestRetryEvery))))
		require.ErrorIs(t, repo.Insert(ctx, newRecord(model, now.Add(2*schedquality.TestRetryEvery))), service.ErrSchedTestRunning)
	})
	t.Run("concurrent_first_tests", func(t *testing.T) {
		const contenders = 12
		start := make(chan struct{})
		results := make(chan error, contenders)
		for range contenders {
			go func() {
				<-start
				results <- NewSchedTestRepository(integrationDB).Insert(ctx, newRecord("concurrent", now))
			}()
		}
		close(start)
		started := 0
		for range contenders {
			if err := <-results; err == nil {
				started++
			} else {
				require.ErrorIs(t, err, service.ErrSchedTestRunning)
			}
		}
		require.Equal(t, 1, started)
		var rows int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_quality_tests WHERE account_id=$1 AND model='concurrent'`, account).Scan(&rows))
		require.Equal(t, 1, rows)
	})
}
