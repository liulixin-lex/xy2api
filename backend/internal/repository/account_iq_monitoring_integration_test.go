//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIQMonitoringBulkResetBusyDeferrals(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: "iq-bulk-busy-reset", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	_, err := integrationDB.Exec(`UPDATE accounts SET iq_check=iq_check||'{"busy_deferrals":3,"execution_state":"deferred","execution_reason":"account_busy"}'::jsonb WHERE id=$1`, a.ID)
	require.NoError(t, err)
	interval := 20
	_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{IQCheck: &domain.IQCheckSettings{IntervalMinutes: &interval}})
	require.NoError(t, err)
	after, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 3, after.IQCheck.BusyDeferrals, "a scheduling edit preserves existing wait credit")
	model := "new-fixture-model"
	_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{IQCheck: &domain.IQCheckSettings{Model: &model}})
	require.NoError(t, err)
	after, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Zero(t, after.IQCheck.BusyDeferrals, "a new assessment profile must reset busy deferrals")
}

func TestIQMonitoringFinalAuditGates(t *testing.T) {
	for _, scenario := range []string{"quota_changed", "health_cooldown", "defer_preserves_cooldown"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
			a := &service.Account{Name: "iq-final-gates", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
			require.NoError(t, repo.Create(ctx, a))
			t.Cleanup(func() {
				cleanupIQTestAccount(t, a.ID)
			})
			now := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
			claims, err := repo.ClaimIQChecks(ctx, now, 1)
			require.NoError(t, err)
			require.Len(t, claims, 1)
			long := now.Add(time.Hour)
			switch scenario {
			case "quota_changed":
				_, err = integrationDB.Exec(`UPDATE accounts SET extra=COALESCE(extra,'{}'::jsonb)||'{"quota_limit":1,"quota_used":1}'::jsonb WHERE id=$1`, a.ID)
			case "health_cooldown":
				_, err = integrationDB.Exec(`UPDATE accounts SET rate_limit_reset_at=$2 WHERE id=$1`, a.ID, long)
				require.NoError(t, err)
			case "defer_preserves_cooldown":
				_, err = integrationDB.Exec(`UPDATE accounts SET iq_check=iq_check||jsonb_build_object('not_before',$2::timestamptz) WHERE id=$1`, a.ID, long)
			}
			require.NoError(t, err)
			if scenario == "defer_preserves_cooldown" {
				require.NoError(t, repo.DeferIQCheck(ctx, claims[0], "account_busy", now.Add(20*time.Second)))
			} else {
				started, err := repo.StartIQCheck(ctx, claims[0], now)
				require.NoError(t, err)
				require.False(t, started, "a fresh business quota gate must prevent starting")
			}
			after, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			if scenario == "quota_changed" {
				require.Equal(t, "account_quota", after.IQCheck.ExecutionReason)
			} else {
				require.Equal(t, long, *after.IQCheck.NextRunAt, "a shorter delay must not erase the latest cooldown")
			}
			require.Empty(t, after.IQCheck.LeaseToken)
			records, err := repo.ListIQCheckRecords(ctx, a.ID)
			require.NoError(t, err)
			require.Empty(t, records)
		})
	}
}

func TestIQMonitoringHealthChangeBeforeStart(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: "iq-health-race", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	now := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
	claims, err := repo.ClaimIQChecks(ctx, now, 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	short, long := now.Add(2*time.Minute), now.Add(10*time.Minute)
	// Business traffic may place the account in cooldown after the worker reads it.
	_, err = integrationDB.Exec(`UPDATE accounts SET overload_until=$2,rate_limit_reset_at=$3,temp_unschedulable_until=$2 WHERE id=$1`, a.ID, short, long)
	require.NoError(t, err)
	started, err := repo.StartIQCheck(ctx, claims[0], now)
	require.NoError(t, err)
	require.False(t, started, "a fresh cooldown must prevent transport")
	after, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "account_cooldown", after.IQCheck.ExecutionReason)
	require.Equal(t, long, *after.IQCheck.NextRunAt)
	require.Empty(t, after.IQCheck.LeaseToken)
	rows, err := repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
	// The account becomes eligible naturally once every cooldown has elapsed.
	claims, err = repo.ClaimIQChecks(ctx, long, 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	started, err = repo.StartIQCheck(ctx, claims[0], long)
	require.NoError(t, err)
	require.True(t, started)
}

func TestIQMonitoringStartAndRecovery(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: "iq-start-recovery", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	now := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
	cs, err := repo.ClaimIQChecks(ctx, now, 2)
	require.NoError(t, err)
	require.Len(t, cs, 1)
	rows, err := repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, repo.DeferIQCheck(ctx, cs[0], "account_busy", now.Add(time.Minute)))
	rows, err = repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
	now = now.Add(time.Minute)
	cs, err = repo.ClaimIQChecks(ctx, now, 2)
	require.NoError(t, err)
	require.Len(t, cs, 1)
	var wg sync.WaitGroup
	started := make(chan bool, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); ok, e := repo.StartIQCheck(ctx, cs[0], now); started <- ok; errs <- e }()
	}
	wg.Wait()
	close(started)
	close(errs)
	count := 0
	for ok := range started {
		if ok {
			count++
		}
	}
	for e := range errs {
		require.NoError(t, e)
	}
	require.Equal(t, 1, count)
	require.NoError(t, repo.CompleteIQCheck(ctx, cs[0], iqcheck.Grade("29"), now.Add(time.Second)))
	require.NoError(t, repo.QueueIQCheck(ctx, a.ID))
	s, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, s.IQCheck.BlocksScheduling())
	require.Equal(t, "deferred", s.IQCheck.ExecutionState)
	_, err = repo.ConfigureIQCheck(ctx, a.ID, iqTestSettings(false, 15))
	require.NoError(t, err)
	_, err = repo.ConfigureIQCheck(ctx, a.ID, iqTestSettings(true, 15))
	require.NoError(t, err)
	s, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	// Pending lease expiration preserves an old assessment; only a started attempt becomes unknown.
	next := *s.IQCheck.NextRunAt
	cs, err = repo.ClaimIQChecks(ctx, next, 2)
	require.NoError(t, err)
	require.Len(t, cs, 1)
	_, err = integrationDB.Exec(`UPDATE accounts SET iq_check=iq_check||'{"status":"degraded"}'::jsonb WHERE id=$1`, a.ID)
	require.NoError(t, err)
	_, err = repo.ClaimIQChecks(ctx, next.Add(181*time.Second), 0)
	require.NoError(t, err)
	s, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, s.IQCheck.BlocksScheduling())
	rows, err = repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}
func TestIQMonitoringStaleResultKeepsCooldown(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: "iq-stale-cooldown", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	now := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
	cs, err := repo.ClaimIQChecks(ctx, now, 2)
	require.NoError(t, err)
	require.Len(t, cs, 1)
	ok, err := repo.StartIQCheck(ctx, cs[0], now)
	require.NoError(t, err)
	require.True(t, ok)
	model := "changed"
	_, err = repo.ConfigureIQCheck(ctx, a.ID, domain.IQCheckSettings{Model: &model})
	require.NoError(t, err)
	after := now.Add(24 * time.Hour)
	result := iqcheck.Unknown("http_429")
	result.Diagnostic = &iqcheck.Diagnostic{RetryAfter: &after}
	require.NoError(t, repo.CompleteIQCheck(ctx, cs[0], result, now.Add(time.Second)))
	a, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", a.IQCheck.Status)
	require.Equal(t, "configuration_changed", a.IQCheck.Reason)
	require.Equal(t, after, *a.IQCheck.NotBefore)
	require.Equal(t, after, *a.IQCheck.NextRunAt)
}

func TestIQMonitoringReleasesInvalidPendingClaim(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		name := "changed_timeout"
		if disabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
			a := &service.Account{Name: "iq-invalid-pending", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
			require.NoError(t, repo.Create(ctx, a))
			t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
			now := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
			claims, err := repo.ClaimIQChecks(ctx, now, 1)
			require.NoError(t, err)
			require.Len(t, claims, 1)
			old, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.NotNil(t, old.IQCheck.LeaseUntil)
			timeout := 60
			settings := domain.IQCheckSettings{TimeoutSeconds: &timeout}
			if disabled {
				enabled := false
				settings.Enabled = &enabled
			}
			_, err = repo.ConfigureIQCheck(ctx, a.ID, settings)
			require.NoError(t, err)
			before, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.NotEqual(t, claims[0].Revision, before.IQCheck.Revision)
			started, err := repo.StartIQCheck(ctx, claims[0], now)
			require.NoError(t, err)
			require.False(t, started)
			after, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Empty(t, after.IQCheck.LeaseToken, "an invalid unsent claim must release its reservation immediately")
			require.Nil(t, after.IQCheck.LeaseUntil)
			require.Equal(t, before.IQCheck.Revision, after.IQCheck.Revision)
			require.Equal(t, before.IQCheck.Status, after.IQCheck.Status)
			require.Equal(t, before.IQCheck.Reason, after.IQCheck.Reason)
			records, err := repo.ListIQCheckRecords(ctx, a.ID)
			require.NoError(t, err)
			require.Empty(t, records)
			next := now.Add(time.Second)
			require.True(t, next.Before(*old.IQCheck.LeaseUntil))
			claims, err = repo.ClaimIQChecks(ctx, next, 1)
			require.NoError(t, err)
			if disabled {
				require.Empty(t, claims)
				require.Nil(t, after.IQCheck.NextRunAt)
			} else {
				require.Len(t, claims, 1)
				require.Equal(t, after.IQCheck.Revision, claims[0].Revision)
				require.Equal(t, timeout, claims[0].TimeoutSeconds)
			}
		})
	}
}

func TestIQMonitoringRejectedStartPreservesActiveReservation(t *testing.T) {
	for _, alreadyStarted := range []bool{false, true} {
		name := "different_token"
		if alreadyStarted {
			name = "already_started_with_changed_revision"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
			a := &service.Account{Name: "iq-valid-reservation", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
			require.NoError(t, repo.Create(ctx, a))
			t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
			now := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
			claims, err := repo.ClaimIQChecks(ctx, now, 1)
			require.NoError(t, err)
			require.Len(t, claims, 1)
			claim := claims[0]
			if alreadyStarted {
				started, err := repo.StartIQCheck(ctx, claim, now)
				require.NoError(t, err)
				require.True(t, started)
				timeout := 60
				_, err = repo.ConfigureIQCheck(ctx, a.ID, domain.IQCheckSettings{TimeoutSeconds: &timeout})
				require.NoError(t, err)
			} else {
				claim.Token = "not-the-current-reservation"
				claim.Revision = "stale-revision"
			}
			before, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			started, err := repo.StartIQCheck(ctx, claim, now.Add(time.Second))
			require.NoError(t, err)
			require.False(t, started)
			after, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, before.IQCheck, after.IQCheck)
			require.NotEmpty(t, after.IQCheck.LeaseToken)
			require.NoError(t, repo.DeferIQCheck(ctx, claim, "account_busy", now.Add(time.Minute)))
			after, err = repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, before.IQCheck, after.IQCheck)
		})
	}
}

func TestIQMonitoringDeferRequeuesChangedPendingClaim(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: "iq-defer-changed", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	now := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
	claims, err := repo.ClaimIQChecks(ctx, now, 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	model := "new-model"
	_, err = repo.ConfigureIQCheck(ctx, a.ID, domain.IQCheckSettings{Model: &model})
	require.NoError(t, err)
	require.NoError(t, repo.DeferIQCheck(ctx, claims[0], "account_busy", now.Add(time.Minute)))
	after, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Empty(t, after.IQCheck.LeaseToken)
	require.NotNil(t, after.IQCheck.NextRunAt)
	require.Equal(t, "pending", after.IQCheck.ExecutionState)
	rows, err := repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
	claims, err = repo.ClaimIQChecks(ctx, now.Add(time.Second), 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, model, claims[0].Profile.Model)
}

func TestIQMonitoringExpiredClaimCannotStart(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: "iq-expired-start", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	now := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
	claims, err := repo.ClaimIQChecks(ctx, now, 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	started, err := repo.StartIQCheck(ctx, claims[0], now.Add(10*time.Minute))
	require.NoError(t, err)
	require.False(t, started)
	after, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Empty(t, after.IQCheck.LeaseToken)
	rows, err := repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestIQMonitoringBusyDeferralsPersistAndReset(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: "iq-busy-progress", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	now := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
	for i := 1; i <= 3; i++ {
		claims, err := repo.ClaimIQChecks(ctx, now, 1)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		next := now.Add(20 * time.Second)
		require.NoError(t, repo.DeferIQCheck(ctx, claims[0], "account_busy", next))
		require.NoError(t, repo.DeferIQCheck(ctx, claims[0], "account_busy", next))
		after, err := repo.GetByID(ctx, a.ID)
		require.NoError(t, err)
		require.Equal(t, i, after.IQCheck.BusyDeferrals)
		require.Equal(t, next, *after.IQCheck.NextRunAt)
		now = next
	}
	rows, err := repo.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Empty(t, rows)
	claims, err := repo.ClaimIQChecks(ctx, now, 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	started, err := repo.StartIQCheck(ctx, claims[0], now)
	require.NoError(t, err)
	require.True(t, started)
	after, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Zero(t, after.IQCheck.BusyDeferrals)
}
