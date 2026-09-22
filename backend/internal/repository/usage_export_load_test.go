package repository

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/usageexport"
	"github.com/stretchr/testify/require"
	"net/url"
	"os"
	"sort"
	"testing"
	"time"
)

// Opt-in SQL capacity gate; repeat on deployment-equivalent hardware before rollout.
// This measures database queries, not HTTP response latency.
func TestUsageExportLoadGate(t *testing.T) {
	if os.Getenv("USAGE_EXPORT_LOAD") != "1" {
		t.Skip("opt-in concurrent SQL load gate")
	}
	_, conn, schema := exportQueryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	_, err := conn.ExecContext(ctx, `TRUNCATE usage_logs;INSERT INTO users VALUES(2,'second@example.test');INSERT INTO usage_logs(id,created_at,requested_model,model,api_key_id,user_id,account_id,group_id,actual_cost,total_cost,rate_multiplier) SELECT n,'2026-09-22'::timestamptz+(n*interval '1 millisecond'),'client','backend',1,1+(n%2),1,1,0.12345678,0.2,1 FROM generate_series(1,1000000) n;CREATE INDEX export_load_order ON usage_logs(user_id,created_at DESC,id DESC);ANALYZE usage_logs`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/252_usage_export_tasks.sql")
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	uri, err := url.Parse(os.Getenv("USAGE_EXPORT_TEST_DSN"))
	require.NoError(t, err)
	q := uri.Query()
	q.Set("search_path", schema)
	uri.RawQuery = q.Encode()
	pool, err := sql.Open("postgres", uri.String())
	require.NoError(t, err)
	defer func() { _ = pool.Close() }()
	pool.SetMaxOpenConns(20)
	cfg := usageexport.Defaults()
	cfg.Enabled = true
	cfg.Directory = t.TempDir()
	cfg.TaskDiskBytes = 512 << 20
	cfg.MaxFileBytes = 128 << 20
	cfg.FreeBytes = 0
	cfg.StorageBytes = 4 << 30
	engine, err := usageexport.New(pool, cfg, usageExportSnapshot, nil)
	require.NoError(t, err)
	engine.Start()
	defer engine.Stop()
	sample := func() time.Duration {
		samples := make([]time.Duration, 0, 100)
		for i := 0; i < 110; i++ {
			start := time.Now()
			var count int64
			require.NoError(t, conn.QueryRowContext(ctx, `SELECT count(*) FROM usage_logs WHERE user_id=1`).Scan(&count))
			rows, er := conn.QueryContext(ctx, `SELECT id,model,created_at,actual_cost FROM usage_logs WHERE user_id=1 ORDER BY created_at DESC,id DESC LIMIT 100`)
			require.NoError(t, er)
			for rows.Next() {
				var id int64
				var model string
				var stamp time.Time
				var cost float64
				require.NoError(t, rows.Scan(&id, &model, &stamp, &cost))
			}
			require.NoError(t, rows.Err())
			_ = rows.Close()
			if i >= 10 {
				samples = append(samples, time.Since(start))
			}
			time.Sleep(10 * time.Millisecond)
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		return samples[94]
	}
	baseline := sample()
	t.Logf("LOAD baseline_sql_p95_us=%d fixture_rows=1000000", baseline.Microseconds())
	for _, parallel := range []int{1, 2} {
		tasks := []*usageexport.Task{}
		for owner := 1; owner <= parallel; owner++ {
			o := usageexport.Options{Format: "csv", Timezone: "UTC", Language: "en", Version: 1, SortBy: "created_at", SortOrder: "desc"}
			o.Filters.UserID = int64(owner)
			task, er := engine.Create(ctx, int64(owner), "user", fmt.Sprintf("load-%d-%d", parallel, owner), o)
			require.NoError(t, er)
			tasks = append(tasks, task)
		}
		for _, task := range tasks {
			for {
				current, er := engine.Get(ctx, task.Owner, "user", task.ID)
				require.NoError(t, er)
				if current.Status != "queued" {
					require.Equal(t, "running", current.Status)
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		observed := sample()
		ratio := float64(observed) / float64(baseline)
		t.Logf("LOAD parallel=%d sql_p95_us=%d baseline_ratio=%.4f", parallel, observed.Microseconds(), ratio)
		for _, task := range tasks {
			for {
				current, er := engine.Get(ctx, task.Owner, "user", task.ID)
				require.NoError(t, er)
				if current.Status == "succeeded" {
					require.EqualValues(t, 500000, *current.Total)
					require.NoError(t, engine.Delete(ctx, task.Owner, "user", task.ID))
					break
				}
				require.NotContains(t, []string{"failed", "canceled"}, current.Status, current.ErrorCode)
				time.Sleep(200 * time.Millisecond)
			}
		}
		require.NoError(t, engine.Cleanup(ctx))
		if ratio > 1.2 {
			t.Errorf("rollout gate failed: parallel=%d SQL P95 ratio %.4f > 1.20", parallel, ratio)
		}
	}
}
