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

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestOpsStatusOriginMigration(t *testing.T) {
	for _, bin := range []string{"initdb", "pg_ctl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip("local PostgreSQL executables unavailable")
		}
	}
	dir, err := os.MkdirTemp("", "errclass-pg-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	data := filepath.Join(dir, "data")
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("initdb", "-D", data, "--no-locale", "--encoding=UTF8", "--auth=trust")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	run("pg_ctl", "-D", data, "-l", filepath.Join(dir, "server.log"), "-o", fmt.Sprintf("-h '' -k %s -p %d", dir, port), "-w", "start")
	t.Cleanup(func() { run("pg_ctl", "-D", data, "-m", "immediate", "-w", "stop") })
	db, err := sql.Open("postgres", fmt.Sprintf("host=%s port=%d dbname=postgres sslmode=disable", dir, port))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE ops_error_logs (id BIGSERIAL PRIMARY KEY, upstream_status_code INTEGER, status_code INTEGER); INSERT INTO ops_error_logs(upstream_status_code,status_code) VALUES (502,503)`)
	require.NoError(t, err)
	body, err := migrations.FS.ReadFile("316_ops_status_origin.sql")
	require.NoError(t, err)
	holder, err := db.Begin()
	require.NoError(t, err)
	_, err = holder.Exec("LOCK TABLE ops_error_logs IN ACCESS SHARE MODE")
	require.NoError(t, err)
	blocked, err := db.Begin()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	waitStarted := time.Now()
	_, err = blocked.ExecContext(ctx, string(body))
	require.GreaterOrEqual(t, time.Since(waitStarted), 4*time.Second)
	var pgerr *pq.Error
	require.ErrorAs(t, err, &pgerr)
	require.Equal(t, pq.ErrorCode("55P03"), pgerr.Code, "lock_timeout must bound the hot-table wait")
	require.NoError(t, blocked.Rollback())
	_, err = db.ExecContext(ctx, "UPDATE ops_error_logs SET status_code=status_code")
	require.NoError(t, err, "failed migration must not queue behind the reader and block writers")
	require.NoError(t, holder.Rollback())
	for range 2 {
		tx, err := db.Begin()
		require.NoError(t, err)
		_, err = tx.Exec(string(body))
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
	}
	var origin, client sql.NullInt64
	require.NoError(t, db.QueryRow("SELECT status_origin,client_status FROM ops_error_logs WHERE id=1").Scan(&origin, &client))
	require.False(t, origin.Valid)
	require.False(t, client.Valid)
	_, err = db.Exec("INSERT INTO ops_error_logs(upstream_status_code,status_code,status_origin,client_status) VALUES (502,503,1,200), (504,504,2,200)")
	require.NoError(t, err)
	var upstream, internal, wire, source int
	require.NoError(t, db.QueryRow("SELECT upstream_status_code,status_code,client_status,status_origin FROM ops_error_logs WHERE id=2").Scan(&upstream, &internal, &wire, &source))
	require.Equal(t, []int{502, 503, 200, 1}, []int{upstream, internal, wire, source})
}
