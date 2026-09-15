//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestIQSimplificationMigration(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// Temporary tables shadow the fully migrated schema, reproducing v0.0.12 state.
	_, err = tx.Exec(`CREATE TEMP TABLE accounts(id bigint,platform text,deleted_at timestamptz,iq_check jsonb,overload_until timestamptz,rate_limit_reset_at timestamptz,temp_unschedulable_until timestamptz);
 CREATE TEMP TABLE iq_check_quota_groups(id text);
 INSERT INTO accounts(id,platform,iq_check) SELECT id,'openai', jsonb_build_object('enabled',true,'interval_minutes',10,'status','degraded','reason','wrong_answer','last_valid_at',now()-interval '2 hours','last_run_at',now()-interval '1 hour','revision','stable','scheduling_mode','adaptive','smart_streak',6,'max_interval_minutes',60,'daily_request_limit',1,'budget_day','2026-09-15','budget_used',999,'quota_group','old','execution_state',CASE WHEN id IN (1,4) THEN 'paused' WHEN id=5 THEN 'idle' ELSE 'deferred' END,'execution_reason',CASE id WHEN 1 THEN 'http_403' WHEN 2 THEN 'daily_budget' WHEN 3 THEN 'group_cooldown' WHEN 4 THEN 'permission_denied' ELSE '' END,'next_run_at',CASE WHEN id IN (1,4) THEN NULL ELSE now()+interval '1 day' END) FROM generate_series(1,5) id;
 UPDATE accounts SET iq_check=iq_check||jsonb_build_object('not_before',now()+interval '2 hours') WHERE id=3;
 UPDATE accounts SET rate_limit_reset_at=now()+interval '3 hours' WHERE id=2;`)
	require.NoError(t, err)
	sql, err := migrations.FS.ReadFile("246_iq_check_simplification.sql")
	require.NoError(t, err)
	_, err = tx.Exec(string(sql))
	require.NoError(t, err)
	for id := 1; id <= 5; id++ {
		var raw []byte
		require.NoError(t, tx.QueryRow(`SELECT iq_check FROM accounts WHERE id=$1`, id).Scan(&raw))
		var data map[string]any
		require.NoError(t, json.Unmarshal(raw, &data))
		for _, key := range []string{"scheduling_mode", "max_interval_minutes", "daily_request_limit", "quota_group", "budget_day", "budget_used", "smart_streak"} {
			require.NotContains(t, data, key)
		}
		var s domain.IQCheck
		require.NoError(t, json.Unmarshal(raw, &s))
		require.Equal(t, 10, s.IntervalMinutes)
		require.Equal(t, "degraded", s.Status)
		require.Equal(t, "stable", s.Revision)
		require.NotNil(t, s.LastValidAt)
		if id == 4 {
			require.Equal(t, "paused", s.ExecutionState)
			require.Nil(t, s.NextRunAt)
		} else {
			require.Equal(t, "pending", s.ExecutionState)
			require.NotNil(t, s.NextRunAt)
		}
		if id == 2 {
			require.True(t, s.NextRunAt.After(time.Now().Add(170*time.Minute)))
		}
		if id == 3 {
			require.True(t, s.NextRunAt.After(time.Now().Add(110*time.Minute)))
		}
	}
	var exists bool
	require.NoError(t, tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname='iq_check_quota_groups' AND relnamespace=pg_my_temp_schema())`).Scan(&exists))
	require.False(t, exists)
	var raw []byte
	require.NoError(t, tx.QueryRow(`SELECT xy_iq_apply_settings('{"enabled":true,"interval_minutes":10,"status":"smart"}', '{"interval_minutes":20,"daily_request_limit":1,"quota_group":"old","status":"degraded"}',false,now())`).Scan(&raw))
	var data map[string]any
	require.NoError(t, json.Unmarshal(raw, &data))
	require.NotContains(t, data, "daily_request_limit")
	require.NotContains(t, data, "quota_group")
	require.Equal(t, "smart", data["status"])
	require.Equal(t, float64(20), data["interval_minutes"])
}

func TestIQForbiddenResumesAfterRestart(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	a := &service.Account{Name: "iq-forbidden-resume", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 1)}
	require.NoError(t, repo.Create(ctx, a))
	t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
	now := time.Now().UTC().Add(time.Second)
	claims, err := repo.ClaimIQChecks(ctx, now, 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	ok, err := repo.StartIQCheck(ctx, claims[0], now)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, repo.CompleteIQCheck(ctx, claims[0], iqcheck.Grade("29"), now.Add(time.Second)))
	current, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	now = *current.IQCheck.NextRunAt
	claims, err = repo.ClaimIQChecks(ctx, now, 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	ok, err = repo.StartIQCheck(ctx, claims[0], now)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, repo.CompleteIQCheck(ctx, claims[0], iqcheck.Unknown("http_403"), now.Add(time.Second)))
	current, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, current.IQCheck.BlocksScheduling())
	require.NotNil(t, current.IQCheck.NextRunAt)
	require.NotEqual(t, "paused", current.IQCheck.ExecutionState)
	peer := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	claims, err = peer.ClaimIQChecks(ctx, *current.IQCheck.NextRunAt, 1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	ok, err = peer.StartIQCheck(ctx, claims[0], *current.IQCheck.NextRunAt)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, peer.CompleteIQCheck(ctx, claims[0], iqcheck.Grade("21"), current.IQCheck.NextRunAt.Add(time.Second)))
	current, err = peer.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, current.IQCheck.BlocksScheduling())
	rows, err := peer.ListIQCheckRecords(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, "http_403", rows[1].Reason)
}

func TestIQLegacyGroupsNoLongerCoupleAccounts(t *testing.T) {
	ctx := context.Background()
	repo := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	for i := 0; i < 2; i++ {
		a := &service.Account{Name: "iq-independent", Platform: "openai", Type: "apikey", Status: "active", Schedulable: true, IQCheckSettings: iqTestSettingsPtr(true, 1)}
		require.NoError(t, repo.Create(ctx, a))
		t.Cleanup(func() { cleanupIQTestAccount(t, a.ID) })
		_, err := integrationDB.Exec(`UPDATE accounts SET iq_check=iq_check||'{"quota_group":"same-old-group","daily_request_limit":1,"budget_used":999999,"budget_day":"2099-01-01"}'::jsonb WHERE id=$1`, a.ID)
		require.NoError(t, err)
	}
	now := time.Now().UTC().Add(time.Second)
	claims, err := repo.ClaimIQChecks(ctx, now, 2)
	require.NoError(t, err)
	require.Len(t, claims, 2)
	for i, claim := range claims {
		ok, err := repo.StartIQCheck(ctx, claim, now)
		require.NoError(t, err)
		require.True(t, ok)
		result := iqcheck.Grade("21")
		if i == 0 {
			result = iqcheck.Unknown("quota_exhausted")
		}
		require.NoError(t, repo.CompleteIQCheck(ctx, claim, result, now.Add(time.Second)))
	}
	second, err := repo.GetByID(ctx, claims[1].AccountID)
	require.NoError(t, err)
	require.Equal(t, "smart", second.IQCheck.Status)
	require.NotNil(t, second.IQCheck.NextRunAt)
}
