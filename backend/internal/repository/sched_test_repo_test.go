//go:build integration

package repository

import (
	"context"
	"errors"
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
	old := newRecord(now.Add(-6*time.Hour-time.Minute), false)
	fresh := newRecord(now.Add(-6*time.Hour+time.Minute), false)
	running := newRecord(now, true)
	for _, v := range []service.SchedTestRecord{old, fresh, running} {
		if err := repo.Insert(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Insert(ctx, newRecord(now, true)); !errors.Is(err, service.ErrSchedTestRunning) {
		t.Fatalf("expected running conflict: %v", err)
	}
	rows, err := repo.Since(ctx, now.Add(-6*time.Hour))
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
		{"stale", 7*time.Minute + time.Microsecond, true},
		{"boundary", 7 * time.Minute, true},
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
