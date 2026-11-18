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

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

// Use an isolated local PostgreSQL cluster, as the migration lock tests do.
// No shared acceptance database or Docker daemon is needed.
func TestSchedAttemptRepositoryV2Evidence(t *testing.T) {
	for _, bin := range []string{"initdb", "pg_ctl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip("local PostgreSQL executables unavailable")
		}
	}
	dir, err := os.MkdirTemp("", "sched-v2-")
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
	apply := func(name string) {
		t.Helper()
		body, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		tx, err := db.Begin()
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		_, err = tx.Exec(string(body))
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
	}
	busyMigration := func(name, table, writeSQL string) {
		t.Helper()
		body, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		holder, err := db.Begin()
		require.NoError(t, err)
		defer func() { _ = holder.Rollback() }()
		_, err = holder.Exec("LOCK TABLE " + table + " IN ACCESS SHARE MODE")
		require.NoError(t, err)
		tx, err := db.Begin()
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		started := time.Now()
		_, err = tx.ExecContext(ctx, string(body))
		var pgerr *pq.Error
		require.ErrorAs(t, err, &pgerr)
		require.Equal(t, pq.ErrorCode("55P03"), pgerr.Code)
		require.GreaterOrEqual(t, time.Since(started), 4*time.Second, "migration must wait for the lock")
		require.Less(t, time.Since(started), 10*time.Second, "lock_timeout must bound the wait")
		// A failed migration must release any earlier locks before rollback.
		_, err = db.ExecContext(ctx, writeSQL)
		require.NoError(t, err)
		require.NoError(t, tx.Rollback())
		require.NoError(t, holder.Rollback())
		apply(name) // Releasing the holder must make the same migration succeed.
	}
	apply("294_sched_quality.sql")
	apply("305_sv2_attempt_tokens.sql")
	// Insert before the migration so the version-1 default must cover old rows.
	start := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	legacyID := uuid.NewString()
	_, err = db.Exec(`INSERT INTO account_attempt_samples
		(attempt_id, started_at, finished_at, account_id, group_id, model, request_class, stream, warm, outcome, session_key, inserted_at)
		VALUES ($1,$2,$3,1,1,'model',1,true,false,1,'session',$4)`,
		legacyID, start, start.Add(time.Second), start.Add(2*time.Second))
	require.NoError(t, err)
	busyMigration("313_sched_attempt_v2.sql", "account_attempt_samples", "UPDATE account_attempt_samples SET detail=detail")
	apply("313_sched_attempt_v2.sql") // Replay after the successful retry must be harmless.
	busyMigration("319_sched_attempt_full_tier_timeout.sql", "account_attempt_samples", "UPDATE account_attempt_samples SET detail=detail")
	apply("319_sched_attempt_full_tier_timeout.sql")
	var fullTier bool
	require.NoError(t, db.QueryRow("SELECT full_tier_first_output_timeout FROM account_attempt_samples WHERE attempt_id=$1", legacyID).Scan(&fullTier))
	require.False(t, fullTier)
	var version int
	require.NoError(t, db.QueryRow("SELECT capture_version FROM account_attempt_samples WHERE attempt_id=$1", legacyID).Scan(&version))
	require.Equal(t, 1, version)
	_, err = db.Exec("CREATE TABLE groups(id INTEGER); CREATE TABLE account_groups(id INTEGER); CREATE TABLE usage_logs(id INTEGER); CREATE TABLE gateway_unbilled_requests(id INTEGER)")
	require.NoError(t, err)
	busyMigration("314_quality_gate_v2_fields.sql", "gateway_unbilled_requests", "INSERT INTO usage_logs VALUES (1)")
	apply("314_quality_gate_v2_fields.sql")
	var duplicateColumn bool
	require.NoError(t, db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.columns
        WHERE table_schema='public' AND table_name='usage_logs' AND column_name='first_output_ms')`).Scan(&duplicateColumn))
	require.False(t, duplicateColumn, "request-level timing reuses end_to_end_first_output_ms")

	assertSchedAttemptV2Evidence(t, db)
	t.Run("mixed-owner-timeouts", func(t *testing.T) { assertSchedAttemptOwnerTimeoutEvidence(t, db) })
}
