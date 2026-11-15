//go:build unit

package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Exercise the same transaction boundary as the migration runner on local PG.
func TestGatewayUsageFactsMigrationBusyTableDoesNotBlockBilling(t *testing.T) {
	for _, bin := range []string{"initdb", "pg_ctl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip("local PostgreSQL executables unavailable")
		}
	}
	dir, err := os.MkdirTemp("", "xy-m11-migration-")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	data, sock := filepath.Join(dir, "data"), filepath.Join(dir, "sock")
	require.NoError(t, os.Mkdir(sock, 0700))
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("initdb", "-D", data, "--no-locale", "--encoding=UTF8", "--auth=trust")
	run("pg_ctl", "-D", data, "-l", filepath.Join(dir, "server.log"), "-o", "-h '' -k "+sock+" -p 55431", "-w", "start")
	defer run("pg_ctl", "-D", data, "-m", "immediate", "-w", "stop")
	db, err := sql.Open("postgres", fmt.Sprintf("host=%s port=55431 dbname=postgres sslmode=disable", sock))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec("CREATE TABLE usage_logs(id int); CREATE TABLE gateway_unbilled_requests(id int); CREATE TABLE ops_error_logs(id int)")
	require.NoError(t, err)
	holder, err := db.Begin()
	require.NoError(t, err)
	defer holder.Rollback()
	_, err = holder.Exec("LOCK TABLE ops_error_logs IN ACCESS SHARE MODE")
	require.NoError(t, err)
	migration, err := FS.ReadFile("312_gateway_usage_facts.sql")
	require.NoError(t, err)
	tx, err := db.Begin()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = tx.ExecContext(ctx, string(migration))
	var pgerr *pq.Error
	require.ErrorAs(t, err, &pgerr)
	require.Equal(t, pq.ErrorCode("55P03"), pgerr.Code)
	// PostgreSQL releases locks on transaction abort, before the runner rolls back.
	_, err = db.ExecContext(ctx, "INSERT INTO usage_logs VALUES(1)")
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.NoError(t, holder.Rollback())
	tx, err = db.Begin()
	require.NoError(t, err)
	_, err = tx.Exec(string(migration))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	var columns, nullable int
	err = db.QueryRow("SELECT count(*),count(*) FILTER(WHERE is_nullable='YES' AND column_default IS NULL) FROM information_schema.columns WHERE table_schema='public' AND column_name<>'id'").Scan(&columns, &nullable)
	require.NoError(t, err)
	require.Equal(t, 16, columns)
	require.Equal(t, 16, nullable)
}
