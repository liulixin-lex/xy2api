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

func TestReadTrialRoundFromCommittedRows(t *testing.T) {
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

	ctx := context.Background()
	execSQL := func(query string, args ...any) {
		t.Helper()
		_, err := db.ExecContext(ctx, query, args...)
		require.NoError(t, err)
	}
	execSQL(`CREATE TABLE accounts(id bigint primary key, extra jsonb, deleted_at timestamptz);
 CREATE TABLE groups(id bigint primary key, platform text, deleted_at timestamptz);
 CREATE TABLE account_groups(account_id bigint,group_id bigint);
 CREATE TABLE sched_line_states(account_id bigint, model text,group_id bigint,line text,identity smallint,trial boolean,trial_until timestamptz);
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
		state, reset, err := repo.ReadTrialRound(ctx, 1, "m", 7)
		require.NoError(t, err)
		require.Equal(t, want, state.Placement(true, time.Now()))
		if want == schedquality.PlaceTrial {
			require.True(t, time.Date(2026, 1, 10, 1, 0, 0, 0, time.UTC).Equal(reset))
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
