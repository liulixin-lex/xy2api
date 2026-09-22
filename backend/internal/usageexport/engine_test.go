package usageexport

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"
)

func testEngine(t *testing.T, snapshot Snapshot) *Engine {
	t.Helper()
	dsn := os.Getenv("USAGE_EXPORT_TEST_DSN")
	if dsn == "" {
		t.Skip("USAGE_EXPORT_TEST_DSN is required for isolated PostgreSQL tests")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(12)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`DROP TABLE IF EXISTS usage_export_tickets,usage_export_keys,usage_export_tasks,usage_export_rate,export_fixture,users CASCADE;CREATE TABLE users(id bigint PRIMARY KEY);INSERT INTO users VALUES(1),(2),(3);CREATE TABLE export_fixture(id bigint PRIMARY KEY,value text)`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/252_usage_export_tasks.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	cfg := Defaults()
	cfg.Enabled = true
	cfg.Directory = t.TempDir()
	cfg.TaskDiskBytes = 64 << 20
	cfg.MaxFileBytes = 32 << 20
	cfg.FreeBytes = 0
	cfg.StorageBytes = 1 << 30
	e, err := New(db, cfg, snapshot, nil)
	require.NoError(t, err)
	return e
}
func options() Options {
	o := Options{Format: "csv", Timezone: "UTC", Language: "en", Version: 1, SortBy: "id", SortOrder: "asc"}
	o.Filters.UserID = 1
	return o
}
func TestExportPostgresLifecycle(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, func(ctx context.Context, tx *sql.Tx, o Options, emit func([]string) error) error {
		return emit([]string{"2026-09-22T00:00:00Z", "=untrusted", "model"})
	})
	task, err := e.Create(ctx, 1, "user", "request-0001", options())
	require.NoError(t, err)
	same, err := e.Create(ctx, 1, "user", "request-0001", options())
	require.NoError(t, err)
	require.Equal(t, task.ID, same.ID)
	alias, err := e.Create(ctx, 1, "user", "request-0002", options())
	require.NoError(t, err)
	require.Equal(t, task.ID, alias.ID)
	changed := options()
	changed.Language = "zh"
	_, err = e.Create(ctx, 1, "user", "request-0001", changed)
	require.ErrorContains(t, err, "IDEMPOTENCY_CONFLICT")
	_, err = e.Get(ctx, 2, "user", task.ID)
	require.ErrorContains(t, err, "NOT_FOUND")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); e.runSlot(ctx, 0) }()
	go func() { defer wg.Done(); e.runSlot(ctx, 1) }()
	wg.Wait()
	task, err = e.Get(ctx, 1, "user", task.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", task.Status)
	require.EqualValues(t, 1, task.Generation)
	require.EqualValues(t, 1, *task.Total)
	require.NotNil(t, task.SnapshotAt)
	require.Len(t, task.SHA256, 64)
	a, err := e.Store.Open(ctx, task.StorageKey)
	require.NoError(t, err)
	first, err := io.ReadAll(a)
	_ = a.Close()
	require.NoError(t, err)
	require.Contains(t, string(first), "'=untrusted")
	a, err = e.Store.Open(ctx, task.StorageKey)
	require.NoError(t, err)
	second, _ := io.ReadAll(a)
	_ = a.Close()
	require.Equal(t, first, second)
	token, err := e.Ticket(ctx, 1, "user", task.ID, TicketIdentity{Owner: 1, Session: "session"})
	require.NoError(t, err)
	_, who, err := e.Redeem(ctx, "user", task.ID, token)
	require.NoError(t, err)
	require.Equal(t, "session", who.Session)
	_, _, err = e.Redeem(ctx, "user", task.ID, token)
	require.ErrorContains(t, err, "INVALID_TICKET")
	require.NoError(t, e.Delete(ctx, 1, "user", task.ID))
	_, err = e.Ticket(ctx, 1, "user", task.ID, TicketIdentity{Owner: 1})
	require.ErrorContains(t, err, "EXPIRED")
	_, err = e.DB.Exec(`UPDATE usage_export_tasks SET heartbeat_at=now()-interval '2 minutes'`)
	require.NoError(t, err)
	require.NoError(t, e.Cleanup(ctx))
	_, err = e.Store.Open(ctx, task.StorageKey)
	require.Error(t, err)
}
func TestExportSnapshotAndCancellation(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	resume := make(chan struct{})
	e := testEngine(t, func(ctx context.Context, tx *sql.Tx, o Options, emit func([]string) error) error {
		var first string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM export_fixture WHERE id=1`).Scan(&first); err != nil {
			return err
		}
		close(started)
		<-resume
		rows, err := tx.QueryContext(ctx, `SELECT value FROM export_fixture ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var value string
			if err = rows.Scan(&value); err != nil {
				return err
			}
			if err = emit([]string{value}); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	_, err := e.DB.Exec(`INSERT INTO export_fixture VALUES(1,'original'),(2,'retained')`)
	require.NoError(t, err)
	task, err := e.Create(ctx, 1, "user", "snapshot-1", options())
	require.NoError(t, err)
	done := make(chan struct{})
	go func() { e.runSlot(ctx, 0); close(done) }()
	<-started
	_, err = e.DB.Exec(`UPDATE export_fixture SET value='changed' WHERE id=1;DELETE FROM export_fixture WHERE id=2;INSERT INTO export_fixture VALUES(3,'new')`)
	require.NoError(t, err)
	close(resume)
	<-done
	task, err = e.Get(ctx, 1, "user", task.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", task.Status)
	file, err := e.Store.Open(ctx, task.StorageKey)
	require.NoError(t, err)
	b, _ := io.ReadAll(file)
	_ = file.Close()
	require.Contains(t, string(b), "original")
	require.Contains(t, string(b), "retained")
	require.NotContains(t, string(b), "changed")
	require.NotContains(t, string(b), "new")
	e.Snapshot = func(ctx context.Context, tx *sql.Tx, o Options, emit func([]string) error) error {
		<-ctx.Done()
		return ctx.Err()
	}
	task, err = e.Create(ctx, 1, "user", "cancel-001", options())
	require.NoError(t, err)
	done = make(chan struct{})
	go func() { e.runSlot(ctx, 0); close(done) }()
	require.Eventually(t, func() bool { t, _ := e.Get(ctx, 1, "user", task.ID); return t.Status == "running" }, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, e.Cancel(ctx, 1, "user", task.ID))
	require.NoError(t, e.Cancel(ctx, 1, "user", task.ID))
	<-done
	task, _ = e.Get(ctx, 1, "user", task.ID)
	require.Equal(t, "canceled", task.Status)
}
func TestExportRowLimitAndRecovery(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, func(ctx context.Context, tx *sql.Tx, o Options, emit func([]string) error) error {
		for i := 0; i < 2; i++ {
			if err := emit([]string{"row"}); err != nil {
				return err
			}
		}
		return nil
	})
	e.Config.MaxRows = 1
	task, err := e.Create(ctx, 1, "user", "limit-001", options())
	require.NoError(t, err)
	e.runSlot(ctx, 0)
	task, _ = e.Get(ctx, 1, "user", task.ID)
	require.Equal(t, "failed", task.Status)
	require.Equal(t, "EXPORT_ROW_LIMIT", task.ErrorCode)
	_, err = e.Ticket(ctx, 1, "user", task.ID, TicketIdentity{Owner: 1})
	require.Error(t, err)
	task, err = e.Create(ctx, 1, "user", "stale-001", options())
	require.NoError(t, err)
	_, err = e.DB.Exec(`UPDATE usage_export_tasks SET status='running',heartbeat_at=now()-interval '2 minutes' WHERE id=$1`, task.ID)
	require.NoError(t, err)
	require.NoError(t, e.Cleanup(ctx))
	task, _ = e.Get(ctx, 1, "user", task.ID)
	require.Equal(t, "failed", task.Status)
	require.Equal(t, "EXPORT_INTERRUPTED", task.ErrorCode)
}
func TestExportFormats(t *testing.T) {
	dir := t.TempDir()
	spool := filepath.Join(dir, "rows")
	f, err := os.Create(spool)
	require.NoError(t, err)
	enc := json.NewEncoder(f)
	require.NoError(t, enc.Encode([]string{"time", " =SUM(1,2)", "中文,\"\nmodel", "effort"}))
	require.NoError(t, f.Close())
	csvPath := filepath.Join(dir, "out.csv")
	require.NoError(t, Render(context.Background(), spool, csvPath, options(), 1<<20, dir))
	b, err := os.ReadFile(csvPath)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(b), "\ufeff"))
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(b), "\ufeff")))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	require.NoError(t, err)
	require.Equal(t, "' =SUM(1,2)", rows[1][1])
	require.Contains(t, rows[1][2], "中文")
	xlsxPath := filepath.Join(dir, "out.xlsx")
	o := options()
	o.Format = "xlsx"
	require.NoError(t, Render(context.Background(), spool, xlsxPath, o, 1<<20, dir))
	book, err := excelize.OpenFile(xlsxPath)
	require.NoError(t, err)
	defer func() { _ = book.Close() }()
	v, err := book.GetCellValue("Usage", "B2")
	require.NoError(t, err)
	require.Equal(t, " =SUM(1,2)", v)
	formula, err := book.GetCellFormula("Usage", "B2")
	require.NoError(t, err)
	require.Empty(t, formula)
	require.Error(t, Render(context.Background(), spool, csvPath, options(), 5, dir))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, Render(ctx, spool, csvPath, options(), 1<<20, dir))
}
