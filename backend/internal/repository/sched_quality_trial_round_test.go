//go:build unit

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newTrialPostgres(t *testing.T) *sql.DB {
	t.Helper()
	for _, bin := range []string{"initdb", "pg_ctl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip("local PostgreSQL executables unavailable")
		}
	}
	dir, err := os.MkdirTemp("", "trial-pg-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	data := filepath.Join(dir, "data")
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	listener, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	run("initdb", "-D", data, "--no-locale", "--encoding=UTF8", "--auth=trust")
	run("pg_ctl", "-D", data, "-l", filepath.Join(dir, "server.log"), "-o", fmt.Sprintf("-h '' -k %s -p %d", dir, port), "-w", "start")
	t.Cleanup(func() { run("pg_ctl", "-D", data, "-m", "immediate", "-w", "stop") })
	db, err := sql.Open("postgres", fmt.Sprintf("host=%s port=%d dbname=postgres sslmode=disable", dir, port))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	return db
}

func TestReadTrialRoundFromCommittedRows(t *testing.T) {
	db := newTrialPostgres(t)
	ctx := context.Background()
	execSQL := func(query string, args ...any) {
		t.Helper()
		_, err := db.ExecContext(ctx, query, args...)
		require.NoError(t, err)
	}
	execSQL(`CREATE TABLE accounts(id bigint primary key, extra jsonb, deleted_at timestamptz);
 CREATE TABLE groups(id bigint primary key, platform text, deleted_at timestamptz);
 CREATE TABLE account_groups(account_id bigint,group_id bigint);
 CREATE TABLE sched_line_states(account_id bigint, model text,group_id bigint,line text,identity smallint,trial boolean,trial_until timestamptz,last_assessment jsonb);
 CREATE TABLE account_quality_overrides(account_id bigint,group_id bigint,mode smallint,expires_at timestamptz);
 CREATE TABLE settings(key text primary key,value text);
 INSERT INTO accounts VALUES(1,'{"group_quality_reset_at":"2026-01-10T01:00:00Z"}',NULL);
 INSERT INTO groups VALUES(7,'openai',NULL),(8,'openai',NULL);
 INSERT INTO account_groups VALUES(1,7),(1,8);
 INSERT INTO sched_line_states VALUES(1,'m',7,'ttfo',0,true,'2026-01-10T02:00:00Z'),(1,'m',8,'error',2,false,NULL)`)
	execSQL("INSERT INTO settings VALUES ($1,$2)", service.SettingKeyGroupQualityGatePolicies, `{"groups":{"7":{"v4":{"enabled":true,"ttft_seconds":15,"max_error_rate":0.1,"min_cache_ratio":0.5,"takeover_idle_seconds":900}}}}`)
	repo := &schedQualityRepository{db: db}
	check := func(want schedquality.Placement) {
		t.Helper()
		state, err := repo.ReadTrialRound(ctx, 1, "m", 7)
		require.NoError(t, err)
		require.Equal(t, want, state.Placement(true, time.Now()))
		if want == schedquality.PlaceTrial {
			require.False(t, state.TTFO.TrialUntil.IsZero())
		}
	}
	check(schedquality.PlaceTrial)
	execSQL("INSERT INTO sched_line_states VALUES (1,'m',7,'cache',2,false,NULL)")
	check(schedquality.PlaceRear)
	execSQL("DELETE FROM sched_line_states WHERE line='cache'")
	execSQL("INSERT INTO account_quality_overrides VALUES (1,0,1,NULL)")
	check(schedquality.PlaceFront)
	execSQL("UPDATE account_quality_overrides SET expires_at=CURRENT_TIMESTAMP - interval '1 second'")
	check(schedquality.PlaceTrial)
	execSQL("INSERT INTO settings VALUES ($1,'false')", service.SettingKeySchedQualityEnabled)
	check(schedquality.PlaceFront)
	execSQL("DELETE FROM settings WHERE key=$1", service.SettingKeySchedQualityEnabled)
	execSQL("DELETE FROM account_groups WHERE group_id=7")
	check(schedquality.PlaceFront)
}

func TestRunResetReturnsGroupsDeletedUnderCycleLock(t *testing.T) {
	db := newTrialPostgres(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `CREATE TABLE accounts(id bigint primary key,extra jsonb);
 CREATE TABLE sched_line_states(account_id bigint,model text,group_id bigint,line text,identity smallint,trial boolean,basis jsonb,last_assessment jsonb);
 CREATE TABLE sched_decisions(at timestamptz,rule_version text,account_id bigint,model text,group_id bigint,line text,action text,dims text[],level int,until timestamptz,basis jsonb,attempt_ids text[]);
 INSERT INTO accounts VALUES(1,'{}');
 INSERT INTO sched_line_states VALUES (2,'other',10,'ttfo',0,false,'{}')`)
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", schedQualityCycleLockID)
	require.NoError(t, err)
	repo := &schedQualityRepository{db: db}
	type result struct {
		groups []service.SchedTrialGroup
		err    error
	}
	done := make(chan result, 1)
	go func() { _, groups, err := repo.RunReset(ctx, 1); done <- result{groups, err} }()
	require.Eventually(t, func() bool {
		var waiting bool
		return tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted)").Scan(&waiting) == nil && waiting
	}, time.Second, 10*time.Millisecond)
	_, err = tx.ExecContext(ctx, `INSERT INTO sched_line_states VALUES
 (1,'m',7,'ttfo',0,true,'{}'),(1,'m',7,'error',0,true,'{}'),(1,'n',8,'cache',0,false,'{}')`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	got := <-done
	require.NoError(t, got.err)
	require.ElementsMatch(t, []service.SchedTrialGroup{{GroupID: 7, Model: "m"}, {GroupID: 8, Model: "n"}}, got.groups)
	var remaining, decisions int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM sched_line_states WHERE account_id=1").Scan(&remaining))
	require.Zero(t, remaining)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM sched_decisions WHERE account_id=1 AND action='reset'").Scan(&decisions))
	require.Equal(t, 2, decisions)
}

func TestSchedQualityDisabledAccountRemainsMember(t *testing.T) {
	db := newTrialPostgres(t)
	ctx := t.Context()
	_, err := db.ExecContext(ctx, `CREATE TABLE groups(id bigint,platform text,deleted_at timestamptz);
 CREATE TABLE accounts(id bigint,status text,schedulable boolean,deleted_at timestamptz);
 CREATE TABLE account_groups(account_id bigint,group_id bigint);
 INSERT INTO groups VALUES(10,'openai',NULL);
 INSERT INTO accounts VALUES(1,'active',true,NULL);
 INSERT INTO account_groups VALUES(1,10)`)
	require.NoError(t, err)
	repo := &schedQualityRepository{db: db}
	for _, enabled := range []bool{false, true} {
		status := "disabled"
		if enabled {
			status = "active"
		}
		_, err = db.ExecContext(ctx, "UPDATE accounts SET status=$1,schedulable=$2 WHERE id=1", status, enabled)
		require.NoError(t, err)
		members, err := repo.LoadMembers(ctx)
		require.NoError(t, err)
		require.Equal(t, []int64{1}, members[10], "account eligibility must not pause or rebuild the quality state")
	}
}
