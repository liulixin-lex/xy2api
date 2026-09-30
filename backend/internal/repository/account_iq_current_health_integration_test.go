//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	dbaccount "github.com/liulixin-lex/xy2api/ent/account"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestIQSchedulingGateSQLMatchesMemory(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: "iq-sql-parity", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	for _, enabled := range []bool{false, true} {
		for _, status := range []string{"", "unknown", "smart", "degraded"} {
			for _, lastValid := range []string{"", "smart", "degraded"} {
				for _, lastRun := range []string{"", "unknown", "smart", "degraded"} {
					t.Run(fmt.Sprintf("enabled=%t/status=%s/valid=%s/run=%s", enabled, status, lastValid, lastRun), func(t *testing.T) {
						state := domain.IQCheck{Enabled: enabled, Status: status, LastValidStatus: lastValid, LastRunStatus: lastRun}
						_, err := testEntClient(t).Account.UpdateOneID(a.ID).SetIqCheck(state).Save(ctx)
						require.NoError(t, err)
						eligible, err := testEntClient(t).Account.Query().Where(dbaccount.IDEQ(a.ID), iqSchedulablePredicate()).Exist(ctx)
						require.NoError(t, err)
						require.Equal(t, !state.BlocksScheduling(), eligible, "database candidate gate and in-memory gate must agree")
					})
				}
			}
		}
	}
}

func TestIQCurrentHealthMigration(t *testing.T) {
	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`CREATE TEMP TABLE accounts(id bigint,iq_check jsonb,deleted_at timestamptz) ON COMMIT DROP;
CREATE TEMP TABLE account_iq_check_attempts(attempt_no int CHECK(attempt_no BETWEEN 1 AND 2)) ON COMMIT DROP;
INSERT INTO accounts VALUES
(1,'{"enabled":true,"status":"smart","reason":"correct_answer","last_run_status":"unknown","last_run_reason":"response_too_large"}',NULL),
(2,'{"enabled":true,"status":"degraded","reason":"wrong_answer","last_run_status":"unknown","last_run_reason":"quota_exhausted","execution_state":"paused","execution_reason":"quota_exhausted"}',NULL),
(3,'{"enabled":true,"status":"smart","reason":"correct_answer","last_run_status":"smart"}',NULL);`)
	require.NoError(t, err)
	source, err := migrations.FS.ReadFile("256_iq_current_health.sql")
	require.NoError(t, err)
	_, err = tx.Exec(string(source))
	require.NoError(t, err)
	for id := 1; id <= 3; id++ {
		var raw []byte
		require.NoError(t, tx.QueryRow(`SELECT iq_check FROM accounts WHERE id=$1`, id).Scan(&raw))
		var state domain.IQCheck
		require.NoError(t, json.Unmarshal(raw, &state))
		if id == 3 {
			require.Equal(t, "smart", state.Status)
			require.False(t, state.BlocksScheduling())
		} else {
			require.Equal(t, "unknown", state.Status)
			require.Equal(t, id == 2, state.BlocksScheduling(), "only the previous valid degraded verdict retains an IQ pause")
		}
		if id == 2 {
			require.Equal(t, "degraded", state.LastValidStatus)
			require.Equal(t, "deferred", state.ExecutionState)
			require.NotNil(t, state.NextRunAt)
			require.True(t, state.NextRunAt.After(time.Now().Add(14*time.Minute)))
		} else {
			require.Equal(t, "smart", state.LastValidStatus)
		}
	}
	_, err = tx.Exec(`INSERT INTO account_iq_check_attempts VALUES (3)`)
	require.NoError(t, err)
	var reset []byte
	require.NoError(t, tx.QueryRow(`SELECT xy_iq_apply_settings(iq_check,'{"model":"changed-fixture"}',false,now()) FROM accounts WHERE id=1`).Scan(&reset))
	var state domain.IQCheck
	require.NoError(t, json.Unmarshal(reset, &state))
	require.Empty(t, state.LastValidStatus)
	require.Empty(t, state.LastRunStatus)
	require.False(t, state.BlocksScheduling())
	_, err = tx.Exec(`INSERT INTO account_iq_check_attempts VALUES (4)`)
	require.Error(t, err, "the database must reject a fourth attempt")
}

func TestIQCurrentHealthAvoidanceAndRecovery(t *testing.T) {
	ctx := context.Background()
	cache := &schedulerCacheRecorder{}
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, cache)
	probe := &service.Account{Name: "iq-current-health", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 1)}
	alternative := &service.Account{Name: "iq-healthy-alternative", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true}
	for _, a := range []*service.Account{probe, alternative} {
		require.NoError(t, repo.Create(ctx, a))
		t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	}
	start := func(at time.Time) service.IQCheckClaim {
		cs, err := repo.ClaimIQChecks(ctx, at, 10)
		require.NoError(t, err)
		require.Len(t, cs, 1)
		require.Equal(t, probe.ID, cs[0].AccountID)
		ok, err := repo.StartIQCheck(ctx, cs[0], at)
		require.NoError(t, err)
		require.True(t, ok)
		return cs[0]
	}
	now := time.Now().UTC().Add(time.Second)
	claim := start(now)
	require.NoError(t, repo.CompleteIQCheck(ctx, claim, iqcheck.Grade("21"), now.Add(time.Second)))
	now = now.Add(2 * time.Minute)
	claim = start(now)
	require.NoError(t, repo.CompleteIQCheck(ctx, claim, iqcheck.Unknown("quota_exhausted"), now.Add(time.Second)))
	fresh, err := repo.GetByID(ctx, probe.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", fresh.IQCheck.Status)
	require.Equal(t, "smart", fresh.IQCheck.LastValidStatus)
	require.True(t, fresh.Schedulable, "IQ isolation must not overwrite the manual switch")
	require.True(t, fresh.IsSchedulable())
	require.True(t, cache.accounts[probe.ID].IsSchedulable())
	require.False(t, buildSchedulerMetadataAccount(*fresh).IQCheck.BlocksScheduling())
	require.Equal(t, "deferred", fresh.IQCheck.ExecutionState)
	require.Nil(t, fresh.IQCheck.RetryAt, "balance failures must not spend immediate retry requests")
	require.NotNil(t, fresh.IQCheck.NextRunAt)
	recoveryAt := *fresh.IQCheck.NextRunAt
	require.True(t, !recoveryAt.Before(now.Add(15*time.Minute)))
	candidates, err := repo.ListSchedulable(ctx)
	require.NoError(t, err)
	foundAlternative, foundProbe := false, false
	for _, a := range candidates {
		foundProbe = foundProbe || a.ID == probe.ID
		foundAlternative = foundAlternative || a.ID == alternative.ID
	}
	require.True(t, foundProbe, "an IQ transport error must not exclude the account from routing")
	require.True(t, foundAlternative, "healthy alternative remains in the routing pool")
	items, _, err := repo.ListWithFilters(service.WithIQStatusFilter(ctx, "unknown"), pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "", "", 0, "")
	require.NoError(t, err)
	foundUnknown := false
	for _, a := range items {
		foundUnknown = foundUnknown || a.ID == probe.ID
	}
	require.True(t, foundUnknown)
	claim = start(recoveryAt)
	require.NoError(t, repo.SetSchedulable(ctx, probe.ID, false))
	require.NoError(t, repo.CompleteIQCheck(ctx, claim, iqcheck.Grade("21"), recoveryAt.Add(time.Second)))
	fresh, err = repo.GetByID(ctx, probe.ID)
	require.NoError(t, err)
	require.False(t, fresh.IQCheck.BlocksScheduling())
	require.False(t, fresh.IsSchedulable(), "recovery cannot override a manual pause")
	require.NoError(t, repo.SetSchedulable(ctx, probe.ID, true))
	fresh, err = repo.GetByID(ctx, probe.ID)
	require.NoError(t, err)
	require.True(t, fresh.IsSchedulable())
	// The account switch remains the administrator's authority, regardless of IQ recovery.
	require.True(t, fresh.Schedulable)
	records, err := repo.ListIQCheckRecords(ctx, probe.ID)
	require.NoError(t, err)
	require.Len(t, records[1].Attempts, 1)

	claim = start(*fresh.IQCheck.NextRunAt)
	require.NoError(t, repo.CompleteIQCheck(ctx, claim, iqcheck.Grade("29"), claim.StartedAt.Add(time.Second)))
	fresh, err = repo.GetByID(ctx, probe.ID)
	require.NoError(t, err)
	require.True(t, fresh.Schedulable)
	require.False(t, fresh.IsSchedulable(), "a valid degraded verdict pauses new requests")
	require.False(t, cache.accounts[probe.ID].IsSchedulable())
	candidates, err = repo.ListSchedulable(ctx)
	require.NoError(t, err)
	for _, a := range candidates {
		require.NotEqual(t, probe.ID, a.ID)
	}

	claim = start(*fresh.IQCheck.NextRunAt)
	require.NoError(t, repo.CompleteIQCheck(ctx, claim, iqcheck.Unknown("http_503"), claim.StartedAt.Add(time.Second)))
	fresh, err = repo.GetByID(ctx, probe.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", fresh.IQCheck.Status)
	require.Equal(t, "degraded", fresh.IQCheck.LastValidStatus)
	require.False(t, fresh.IsSchedulable(), "an IQ error cannot clear the previous degraded pause")
	candidates, err = repo.ListSchedulable(ctx)
	require.NoError(t, err)
	for _, a := range candidates {
		require.NotEqual(t, probe.ID, a.ID)
	}

	claim = start(*fresh.IQCheck.NextRunAt)
	require.NoError(t, repo.CompleteIQCheck(ctx, claim, iqcheck.Grade("21"), claim.StartedAt.Add(time.Second)))
	fresh, err = repo.GetByID(ctx, probe.ID)
	require.NoError(t, err)
	require.Equal(t, "smart", fresh.IQCheck.LastValidStatus)
	require.True(t, fresh.IsSchedulable(), "only a valid smart verdict clears the IQ pause")
	candidates, err = repo.ListSchedulable(ctx)
	require.NoError(t, err)
	foundProbe = false
	for _, a := range candidates {
		foundProbe = foundProbe || a.ID == probe.ID
	}
	require.True(t, foundProbe)
}
