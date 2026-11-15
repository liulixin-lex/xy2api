//go:build integration

package repository

import (
	"context"
	"database/sql"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestSchedV2UpgradeFrom064PreservesRollbackData(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag), tcpostgres.WithDatabase("sched_v2_upgrade"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("fixture"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	// The v0.6.4 base (ad66cfbc9) has 345 SQL migrations through 298.
	previous := fstest.MapFS{}
	names, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)
	for _, name := range names {
		if name >= "300_" {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, name)
		require.NoError(t, err)
		previous[name] = &fstest.MapFile{Data: body}
	}
	require.Len(t, previous, 345)
	require.NoError(t, applyMigrationsFS(ctx, db, previous))
	var account, group int64
	require.NoError(t, db.QueryRow(`INSERT INTO accounts(name,platform,type,credentials,extra) VALUES('synthetic-upgrade','openai','apikey','{}','{}') RETURNING id`).Scan(&account))
	require.NoError(t, db.QueryRow(`INSERT INTO groups(name,platform) VALUES('synthetic-upgrade','openai') RETURNING id`).Scan(&group))
	for _, query := range []string{
		`INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,17)`,
		`INSERT INTO account_quality_states(account_id,group_id,model,state,level,reason,since) VALUES($1,$2,'synthetic-model',1,3,'test_speed',now())`,
		`INSERT INTO account_quality_overrides(account_id,group_id,mode) VALUES($1,$2,2)`,
		`INSERT INTO account_quality_tests(id,account_id,model,trigger,status,started_at) VALUES('00000000-0000-0000-0000-000000000064',$1,'synthetic-model',0,1,now())`,
	} {
		args := []any{account, group}
		if strings.HasPrefix(query, "INSERT INTO account_quality_tests") {
			args = args[:1]
		}
		_, err := db.Exec(query, args...)
		require.NoError(t, err)
	}
	raw := `{"version":4,"groups":{"1":{"v4":{"enabled":true,"ttft_seconds":12,"max_error_rate":0.01,"min_cache_ratio":0.8,"takeover_idle_seconds":0}},"2":{"v3":{"enabled":true,"max_ttft_p90_seconds":15,"max_error_rate":0.02,"min_cache_keep_rate":0.7}}}}`
	_, err = db.Exec(`INSERT INTO settings(key,value) VALUES('group_quality_gate_policies',$1),('sched_quality_test_enabled','false') ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, raw)
	require.NoError(t, err)
	require.NoError(t, ApplyMigrations(ctx, db))
	require.NoError(t, ApplyMigrations(ctx, db), "upgrade rerun is idempotent")
	var state, level, mode, priority int
	var reason string
	require.NoError(t, db.QueryRow(`SELECT state,level,reason FROM account_quality_states WHERE account_id=$1`, account).Scan(&state, &level, &reason))
	require.Equal(t, 1, state)
	require.Equal(t, 3, level)
	require.Equal(t, "test_speed", reason)
	require.NoError(t, db.QueryRow(`SELECT mode FROM account_quality_overrides WHERE account_id=$1`, account).Scan(&mode))
	require.Equal(t, 2, mode)
	require.NoError(t, db.QueryRow(`SELECT priority FROM account_groups WHERE account_id=$1`, account).Scan(&priority))
	require.Equal(t, 17, priority)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM account_quality_tests WHERE account_id=$1`, account).Scan(&count))
	require.Equal(t, 1, count)
	for _, table := range []string{"sched_states", "sched_decisions"} {
		require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
		require.Zero(t, count)
	}
	var key, policy string
	require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key='sched_quality_test_enabled'`).Scan(&key))
	require.Equal(t, "false", key)
	require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key='group_quality_gate_policies'`).Scan(&policy))
	records, version, err := decodeSchedQualityPolicies(policy)
	require.NoError(t, err)
	require.EqualValues(t, 5, version)
	require.Equal(t, .05, records[1].Policy.MaxErrorRate)
	require.Zero(t, records[1].Policy.TakeoverIdleSeconds)
	require.Equal(t, 900, records[2].Policy.TakeoverIdleSeconds)
	var legacyPresent bool
	require.NoError(t, db.QueryRow(`SELECT (value::jsonb->'groups'->'2') ? 'v3' FROM settings WHERE key='group_quality_gate_policies'`).Scan(&legacyPresent))
	require.True(t, legacyPresent)
}
