//go:build unit

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
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
	run("initdb", "-D", data, "--no-locale", "--encoding=UTF8", "--auth=trust")
	run("pg_ctl", "-D", data, "-l", filepath.Join(dir, "server.log"), "-o", "-h '' -k "+dir+" -p 55432", "-w", "start")
	t.Cleanup(func() { run("pg_ctl", "-D", data, "-m", "immediate", "-w", "stop") })
	db, err := sql.Open("postgres", fmt.Sprintf("host=%s port=55432 dbname=postgres sslmode=disable", dir))
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
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err = tx.ExecContext(ctx, string(body))
		var pgerr *pq.Error
		require.ErrorAs(t, err, &pgerr)
		require.Equal(t, pq.ErrorCode("55P03"), pgerr.Code)
		// A failed migration must release any earlier locks before rollback.
		_, err = db.ExecContext(ctx, writeSQL)
		require.NoError(t, err)
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
	apply("313_sched_attempt_v2.sql")
	apply("313_sched_attempt_v2.sql") // Replay must be harmless.
	var version int
	require.NoError(t, db.QueryRow("SELECT capture_version FROM account_attempt_samples WHERE attempt_id=$1", legacyID).Scan(&version))
	require.Equal(t, 1, version)
	_, err = db.Exec("CREATE TABLE groups(id INTEGER); CREATE TABLE account_groups(id INTEGER); CREATE TABLE usage_logs(id INTEGER); CREATE TABLE gateway_unbilled_requests(id INTEGER)")
	require.NoError(t, err)
	busyMigration("314_quality_gate_v2_fields.sql", "gateway_unbilled_requests", "INSERT INTO usage_logs VALUES (1)")
	apply("314_quality_gate_v2_fields.sql")
	apply("314_quality_gate_v2_fields.sql")
	var firstOutput *int
	require.NoError(t, db.QueryRow("SELECT first_output_ms FROM usage_logs WHERE id=1").Scan(&firstOutput))
	require.Nil(t, firstOutput, "historical requests have unknown request-level first output")

	ctx := context.Background()
	repo := &schedAttemptRepository{db: db}
	// A page containing only v1 data must not emit evidence or advance the cursor.
	rows, cursor, err := repo.LoadInsertedAfter(ctx, start.Add(-time.Second), 1)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.True(t, cursor.IsZero())
	finished, err := repo.LoadFinishedSince(ctx, start)
	require.NoError(t, err)
	require.Empty(t, finished)
	current := schedquality.AttemptSample{AttemptID: uuid.NewString(), StartedAt: start.Add(20 * time.Second), GroupID: 1, Model: "model", SessionKey: "session"}
	previous, err := repo.LoadPreviousAttempts(ctx, []schedquality.AttemptSample{current})
	require.NoError(t, err)
	require.Nil(t, previous[current.AttemptID])

	event, valid, answer, status, output := 10, 20, 30, 503, 42
	sample := schedquality.AttemptSample{
		AttemptID: uuid.NewString(), StartedAt: start.Add(3 * time.Second), FinishedAt: start.Add(4 * time.Second),
		AccountID: 1, GroupID: 1, Model: "model", Stream: true, SessionKey: "session",
		TTFOMs: &valid, FirstEventMs: &event, FirstAnswerMs: &answer, Outcome: schedquality.OutcomeAccountFault,
		Class: schedquality.ClassUpstream, HTTPStatus: &status, StatusOrigin: schedquality.OriginUpstream, OutputBytes: &output,
	}
	nullable := sample
	nullable.AttemptID, nullable.StartedAt, nullable.FinishedAt = uuid.NewString(), start.Add(5*time.Second), start.Add(6*time.Second)
	nullable.TTFOMs, nullable.FirstEventMs, nullable.FirstAnswerMs = nil, nil, nil
	nullable.Class, nullable.HTTPStatus, nullable.StatusOrigin, nullable.OutputBytes = schedquality.ClassNone, nil, schedquality.OriginNone, nil
	require.NoError(t, repo.InsertBatch(ctx, []schedquality.AttemptSample{sample, nullable}))
	require.NoError(t, db.QueryRow("SELECT capture_version FROM account_attempt_samples WHERE attempt_id=$1", sample.AttemptID).Scan(&version))
	require.Equal(t, 2, version)
	_, err = db.Exec("UPDATE account_attempt_samples SET inserted_at=$1 WHERE attempt_id=$2", start.Add(7*time.Second), sample.AttemptID)
	require.NoError(t, err)
	_, err = db.Exec("UPDATE account_attempt_samples SET inserted_at=$1 WHERE attempt_id=$2", start.Add(8*time.Second), nullable.AttemptID)
	require.NoError(t, err)
	// Put v1 immediately before current: it must not mask the eligible predecessor.
	_, err = db.Exec("UPDATE account_attempt_samples SET started_at=$1 WHERE attempt_id=$2", start.Add(19*time.Second), legacyID)
	require.NoError(t, err)

	rows, cursor, err = repo.LoadInsertedAfter(ctx, start.Add(-time.Second), 1)
	require.NoError(t, err)
	require.Len(t, rows, 1, "v1 must not consume the page limit and stall ingestion")
	require.Equal(t, sample.AttemptID, rows[0].AttemptID)
	require.True(t, cursor.Equal(start.Add(7*time.Second)))
	require.Equal(t, sample.FirstEventMs, rows[0].FirstEventMs)
	require.Equal(t, sample.TTFOMs, rows[0].TTFOMs)
	require.Equal(t, sample.FirstAnswerMs, rows[0].FirstAnswerMs)
	require.Equal(t, sample.Class, rows[0].Class)
	require.Equal(t, sample.HTTPStatus, rows[0].HTTPStatus)
	require.Equal(t, sample.StatusOrigin, rows[0].StatusOrigin)
	require.Equal(t, sample.OutputBytes, rows[0].OutputBytes)
	finished, err = repo.LoadFinishedSince(ctx, start)
	require.NoError(t, err)
	require.Len(t, finished, 2)
	require.Equal(t, nullable.AttemptID, finished[1].AttemptID)
	require.Nil(t, finished[1].FirstEventMs)
	require.Nil(t, finished[1].FirstAnswerMs)
	require.Nil(t, finished[1].HTTPStatus)
	require.Nil(t, finished[1].OutputBytes)
	require.Equal(t, schedquality.ClassNone, finished[1].Class)
	require.Equal(t, schedquality.OriginNone, finished[1].StatusOrigin)
	previous, err = repo.LoadPreviousAttempts(ctx, []schedquality.AttemptSample{current})
	require.NoError(t, err)
	require.Equal(t, nullable.AttemptID, previous[current.AttemptID].AttemptID)
}
