//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Tiered temp_unschedulable writes against real PostgreSQL (review R01/R02).

func TestAccountRepositoryTieredTempUnschedulable(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	acc := mustCreateAccount(t, client, &service.Account{Name: "tiered-penalty", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	soft := resilience.SoftReasonPrefixes()
	now := time.Now()
	read := func() (string, time.Time) {
		t.Helper()
		got, err := repo.GetByID(ctx, acc.ID)
		require.NoError(t, err)
		require.NotNil(t, got.TempUnschedulableUntil)
		return got.TempUnschedulableReason, *got.TempUnschedulableUntil
	}
	write := func(until time.Time, reason string) bool {
		t.Helper()
		changed, err := repo.SetTempUnschedulableTiered(ctx, acc.ID, until, reason, resilience.TempUnschedTier(reason) == resilience.TierHard, resilience.IsBalancePause(reason), soft)
		require.NoError(t, err)
		return changed
	}

	// A 10 minute soft 403 cooldown, then a 5 minute balance pause.
	require.True(t, write(now.Add(10*time.Minute), resilience.Reason403Cooldown+" (1/3): forbidden"))
	require.True(t, write(now.Add(5*time.Minute), resilience.BalancePauseReason("insufficient balance")))
	reason, until := read()
	require.True(t, resilience.IsBalancePause(reason), reason)
	require.WithinDuration(t, now.Add(10*time.Minute), until, time.Second, "the later end is kept")

	// No soft penalty replaces the active hard pause, however long.
	require.False(t, write(now.Add(time.Hour), resilience.ReasonPrefixTransient+"TRANSIENT 503"))
	reason, _ = read()
	require.True(t, resilience.IsBalancePause(reason), reason)

	// A longer hard pause replaces it; a shorter hard one does not.
	require.True(t, write(now.Add(30*time.Minute), "token refresh failed"))
	require.False(t, write(now.Add(5*time.Minute), resilience.BalancePauseReason("again")))
	reason, until = read()
	require.Equal(t, "token refresh failed", reason)

	// The conditional clear only clears the pause that was read.
	cleared, err := repo.ClearTempUnschedulableIf(ctx, acc.ID, "transient:stale", until)
	require.NoError(t, err)
	require.False(t, cleared)
	cleared, err = repo.ClearTempUnschedulableIf(ctx, acc.ID, reason, until)
	require.NoError(t, err)
	require.True(t, cleared)
	got, err := repo.GetByID(ctx, acc.ID)
	require.NoError(t, err)
	require.Nil(t, got.TempUnschedulableUntil)

	// A short hard pause of another kind replaces a long soft cooldown with
	// its own end, so a single-account group is not held for the rest of it.
	require.True(t, write(now.Add(10*time.Minute), resilience.Reason403Cooldown+" (1/3): forbidden"))
	require.True(t, write(now.Add(30*time.Second), "upstream transport error"))
	reason, until = read()
	require.Equal(t, "upstream transport error", reason)
	require.WithinDuration(t, now.Add(30*time.Second), until, time.Second)
	cleared, err = repo.ClearTempUnschedulableIf(ctx, acc.ID, reason, until)
	require.NoError(t, err)
	require.True(t, cleared)

	// After expiry any penalty writes again.
	require.True(t, write(now.Add(30*time.Second), resilience.ReasonPrefixTransient+"TRANSIENT 524"))
	require.True(t, write(now.Add(90*time.Second), resilience.ReasonPrefixTransient+"TRANSIENT 503"))
	require.False(t, write(now.Add(10*time.Second), resilience.ReasonPrefixTransient+"TRANSIENT 502"))
}

func TestAccountRepositoryBalancePauseConcurrentFailuresStayInOneTier(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	acc := mustCreateAccount(t, client, &service.Account{Name: "balance-tier-concurrent", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	basePause := 5 * time.Minute
	firstStartedAt := time.Now().UTC().Truncate(time.Microsecond)
	first, err := repo.SetBalancePauseTiered(ctx, acc.ID, basePause, firstStartedAt, firstStartedAt, "first")
	require.NoError(t, err)
	require.Equal(t, 1, first.Step)

	expiredUntil := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET temp_unschedulable_until = $1 WHERE id = $2`, expiredUntil, acc.ID)
	require.NoError(t, err)
	attemptStart := expiredUntil.Add(time.Millisecond)
	startedAt := time.Now().UTC().Truncate(time.Microsecond)

	start := make(chan struct{})
	results := make(chan service.BalancePauseWrite, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			write, err := repo.SetBalancePauseTiered(ctx, acc.ID, basePause, attemptStart, startedAt, "same round")
			if err != nil {
				t.Errorf("SetBalancePauseTiered: %v", err)
				return
			}
			results <- write
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	changed := 0
	for write := range results {
		if write.Changed {
			changed++
		}
	}
	require.Equal(t, 1, changed)
	got, err := repo.GetByID(ctx, acc.ID)
	require.NoError(t, err)
	require.Equal(t, 2, resilience.BalancePauseStep(got.TempUnschedulableReason))
	require.WithinDuration(t, startedAt.Add(15*time.Minute), *got.TempUnschedulableUntil, time.Millisecond)
}
