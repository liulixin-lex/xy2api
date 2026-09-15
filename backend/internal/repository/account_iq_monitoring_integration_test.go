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

func TestIQMonitoringBudgetStartAndRecovery(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	limit := 1
	a := &service.Account{Name: "iq-budget", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
	a.IQCheckSettings.DailyRequestLimit = &limit
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
		go func() { defer wg.Done(); ok, e := repo.StartIQCheck(ctx, cs[0], now, nil); started <- ok; errs <- e }()
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
	require.Equal(t, 1, s.IQCheck.BudgetUsed)
	require.Equal(t, "deferred", s.IQCheck.ExecutionState)
	_, err = repo.ConfigureIQCheck(ctx, a.ID, iqTestSettings(false, 15))
	require.NoError(t, err)
	_, err = repo.ConfigureIQCheck(ctx, a.ID, iqTestSettings(true, 15))
	require.NoError(t, err)
	s, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 1, s.IQCheck.BudgetUsed)
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
func TestIQMonitoringSharedQuotaAndPause(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	group := "monitor-fixture"
	policies := map[string]service.IQQuotaPolicy{group: {DailyLimit: 2, MinIntervalSeconds: 60}}
	t.Cleanup(func() {
		_, e := integrationDB.Exec("DELETE FROM iq_check_quota_groups WHERE id=$1", group)
		require.NoError(t, e)
	})
	ids := []int64{}
	for i := 0; i < 2; i++ {
		a := &service.Account{Name: "iq-group", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 15)}
		a.IQCheckSettings.QuotaGroup = &group
		require.NoError(t, repo.Create(ctx, a))
		ids = append(ids, a.ID)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			cleanupIQTestAccount(t, id)
		}
	})
	now := time.Now().UTC().Truncate(time.Microsecond).Add(time.Second)
	cs, e := repo.ClaimIQChecks(ctx, now, 2)
	require.NoError(t, e)
	require.Len(t, cs, 2)
	ok, e := repo.StartIQCheck(ctx, cs[0], now, policies)
	require.NoError(t, e)
	require.True(t, ok)
	ok, e = repo.StartIQCheck(ctx, cs[1], now, policies)
	require.NoError(t, e)
	require.False(t, ok)
	after := now.Add(4 * time.Hour)
	r := iqcheck.Unknown("http_429")
	r.Diagnostic = &iqcheck.Diagnostic{RetryAfter: &after}
	require.NoError(t, repo.CompleteIQCheck(ctx, cs[0], r, now))
	cs, e = repo.ClaimIQChecks(ctx, now.Add(time.Minute), 2)
	require.NoError(t, e)
	require.Len(t, cs, 1)
	ok, e = repo.StartIQCheck(ctx, cs[0], now.Add(time.Minute), policies)
	require.NoError(t, e)
	require.False(t, ok)
	a, e := repo.GetByID(ctx, cs[0].AccountID)
	require.NoError(t, e)
	require.Equal(t, after, *a.IQCheck.NextRunAt)
	n := 300
	_, e = repo.BulkUpdate(ctx, []int64{ids[0]}, service.AccountBulkUpdate{IQCheck: &domain.IQCheckSettings{TimeoutSeconds: &n}})
	require.NoError(t, e)
	a, e = repo.GetByID(ctx, ids[0])
	require.NoError(t, e)
	require.Equal(t, 300, a.IQCheck.TimeoutSeconds)
	require.False(t, a.IQCheck.NextRunAt.Before(after))
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
	ok, err := repo.StartIQCheck(ctx, cs[0], now, nil)
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
	require.Equal(t, 1, a.IQCheck.BudgetUsed)
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
			started, err := repo.StartIQCheck(ctx, claims[0], now, nil)
			require.NoError(t, err)
			require.False(t, started)
			after, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Empty(t, after.IQCheck.LeaseToken, "an invalid unsent claim must release its reservation immediately")
			require.Nil(t, after.IQCheck.LeaseUntil)
			require.Equal(t, before.IQCheck.Revision, after.IQCheck.Revision)
			require.Equal(t, before.IQCheck.Status, after.IQCheck.Status)
			require.Equal(t, before.IQCheck.Reason, after.IQCheck.Reason)
			require.Equal(t, before.IQCheck.BudgetUsed, after.IQCheck.BudgetUsed)
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
				started, err := repo.StartIQCheck(ctx, claim, now, nil)
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
			started, err := repo.StartIQCheck(ctx, claim, now.Add(time.Second), nil)
			require.NoError(t, err)
			require.False(t, started)
			after, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, before.IQCheck, after.IQCheck)
			require.NotEmpty(t, after.IQCheck.LeaseToken)
		})
	}
}
