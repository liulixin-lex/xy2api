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

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

// The socket lives in a private, short directory to fit the Unix socket path limit.
func observePostgres(t *testing.T) *sql.DB {
	t.Helper()
	for _, name := range []string{"initdb", "pg_ctl"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("local PostgreSQL executables unavailable")
		}
	}
	dir, err := os.MkdirTemp("", "obs-pg-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	data := filepath.Join(dir, "data")
	run := func(bin string, args ...string) {
		t.Helper()
		output, err := exec.Command(bin, args...).CombinedOutput()
		require.NoError(t, err, string(output))
	}
	run("initdb", "-D", data, "--no-locale", "--encoding=UTF8", "--auth=trust")
	run("pg_ctl", "-D", data, "-l", filepath.Join(dir, "server.log"), "-o", fmt.Sprintf("-h '' -k '%s'", dir), "-w", "start")
	t.Cleanup(func() { run("pg_ctl", "-D", data, "-m", "immediate", "-w", "stop") })
	db, err := sql.Open("postgres", fmt.Sprintf("host='%s' dbname=postgres sslmode=disable", dir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestObserveEndToEndPostgres(t *testing.T) {
	ctx := context.Background()
	db := observePostgres(t)
	require.NoError(t, ApplyMigrations(ctx, db))
	run := func(query string, args ...any) {
		t.Helper()
		_, err := db.ExecContext(ctx, query, args...)
		require.NoError(t, err)
	}
	// Use one group and measure the head, two full hours and the tail.
	epoch := time.Now().UTC().Truncate(24 * time.Hour).Add(-48 * time.Hour)
	head, oldHour, measuredHour, tail := epoch.Add(30*time.Minute), epoch.Add(time.Hour), epoch.Add(2*time.Hour), epoch.Add(3*time.Hour)
	end := tail.Add(30 * time.Minute)
	const id int64 = 8701
	run("INSERT INTO groups(id,name,platform) VALUES ($1,'fixture','openai')", id)
	run("INSERT INTO users(id,email,password_hash) VALUES ($1,'fixture@example.test','fixture')", id)
	run("INSERT INTO accounts(id,name,platform,type) VALUES ($1,'fixture','openai','apikey')", id)
	run("INSERT INTO api_keys(id,user_id,key,name) VALUES ($1,$1,'synthetic-observe','fixture')", id)
	addUsage := func(at time.Time, first, e2e *int) {
		t.Helper()
		run(`INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,model,request_id,created_at,first_token_ms,end_to_end_first_output_ms)
			VALUES ($1,$1,$1,$1,'fixture',$2,$3,$4,$5)`, id, at.Format(time.RFC3339Nano), at, first, e2e)
	}
	addUsage(head, new(1), new(20))
	addUsage(oldHour, new(9), nil)
	addUsage(measuredHour, new(20), new(20))

	addUsage(tail, new(2), new(200))
	// The historical hour already has TTFT but no E2E measurements.
	run(`INSERT INTO ops_metrics_hourly(bucket_start,success_count,ttft_sample_count,ttft_p50_ms)
		VALUES ($1,1,1,9)`, oldHour)
	migration, err := migrations.FS.ReadFile("320_ops_metrics_e2e_first_output.sql")
	require.NoError(t, err)
	run(string(migration))
	run(string(migration))
	repo := &opsRepository{db: db}
	overview := func(from, to time.Time, mode service.OpsQueryMode) *service.OpsDashboardOverview {
		t.Helper()
		out, err := repo.GetDashboardOverview(ctx, &service.OpsDashboardFilter{StartTime: from, EndTime: to, QueryMode: mode})
		require.NoError(t, err)
		return out
	}
	legacy := overview(oldHour, measuredHour, service.OpsQueryModePreagg)
	require.Equal(t, new(9), legacy.TTFT.P50)
	require.Equal(t, service.OpsPercentiles{}, legacy.E2EFirstOutput)
	require.Zero(t, legacy.E2EFirstOutputSampleCount)

	// The second measurement arrives after both initial upserts. Reaggregation
	// must update the sample count and every percentile on conflict.
	require.NoError(t, repo.UpsertHourlyMetrics(ctx, measuredHour, tail))
	require.NoError(t, repo.UpsertDailyMetrics(ctx, epoch, epoch.Add(24*time.Hour)))
	addUsage(measuredHour.Add(time.Minute), new(60), new(60))
	raw := overview(measuredHour, tail, service.OpsQueryModeRaw)
	require.Equal(t, service.OpsPercentiles{P50: new(40), P90: new(56), P95: new(58), P99: new(60), Avg: new(40), Max: new(60)}, raw.E2EFirstOutput)
	require.Equal(t, raw.TTFT, raw.E2EFirstOutput)
	require.EqualValues(t, 2, raw.E2EFirstOutputSampleCount)
	require.NoError(t, repo.UpsertHourlyMetrics(ctx, measuredHour, tail))
	hour := overview(measuredHour, tail, service.OpsQueryModePreagg)
	require.Equal(t, raw.E2EFirstOutput, hour.E2EFirstOutput)
	require.Equal(t, hour.TTFT, hour.E2EFirstOutput)
	require.EqualValues(t, 2, hour.E2EFirstOutputSampleCount)

	mixed := overview(head, end, service.OpsQueryModePreagg)
	require.Equal(t, service.OpsPercentiles{P50: new(75), P90: new(83), P95: new(200), P99: new(200), Avg: new(75), Max: new(200)}, mixed.E2EFirstOutput)
	require.EqualValues(t, 4, mixed.E2EFirstOutputSampleCount)
	require.Equal(t, new(19), mixed.TTFT.P50) // preaggregated P50 rounds to 30; (1 + 3*30 + 2) / 5

	// Roll up the same weighted samples as the raw head and tail.
	require.NoError(t, repo.UpsertHourlyMetrics(ctx, epoch, oldHour))
	require.NoError(t, repo.UpsertHourlyMetrics(ctx, tail, end))
	require.NoError(t, repo.UpsertDailyMetrics(ctx, epoch, epoch.Add(24*time.Hour)))
	var count int64
	var daily service.OpsPercentiles
	require.NoError(t, db.QueryRow(`SELECT e2e_first_output_p50_ms,e2e_first_output_p90_ms,e2e_first_output_p95_ms,
		e2e_first_output_p99_ms,e2e_first_output_avg_ms,e2e_first_output_max_ms,e2e_first_output_sample_count
		FROM ops_metrics_daily WHERE platform IS NULL AND group_id IS NULL`).Scan(&daily.P50, &daily.P90, &daily.P95, &daily.P99, &daily.Avg, &daily.Max, &count))
	require.Equal(t, mixed.E2EFirstOutput, daily)
	require.EqualValues(t, 4, count)

	usage := newUsageLogRepositoryWithSQL(dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db))), db)
	run(`INSERT INTO gateway_unbilled_requests(request_id,created_at,user_id,api_key_id,group_id,platform,model,stream,result,first_output_ms,duration_ms)
		VALUES ('a7956456-7bbf-460f-9a39-a504c5477f1a',$1,$2,$2,$2,'openai','fixture',true,3,500,600)`, head, id)
	run("INSERT INTO settings(key,value) VALUES ('group_quality_gate_policies',$1) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value",
		fmt.Sprintf(`{"groups":{"%d":{"v4":{"enabled":true,"ttft_seconds":0.4}}}}`, id))
	page, _, err := usage.ListAdminRequests(ctx, pagination.PaginationParams{}, usagestats.UsageLogFilters{GroupID: id, Result: new(int16(3)), StartTime: &head, EndTime: &end})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Nil(t, page[0].Log.FirstTokenMs)
	require.EqualValues(t, 500, *page[0].Log.EndToEndFirstOutputMs)
	require.Equal(t, new(true), page[0].TTFOOver)
	stats, err := usage.AdminRequestStats(ctx, usagestats.UsageLogFilters{StartTime: &head, EndTime: &end, GroupID: id})
	require.NoError(t, err)
	require.EqualValues(t, 5, stats.E2EFirstOutputSamples)
	require.Equal(t, new(60.0), stats.E2EFirstOutputP50Ms)
	stats, err = usage.AdminRequestStats(ctx, usagestats.UsageLogFilters{StartTime: &oldHour, EndTime: &measuredHour, GroupID: id})
	require.NoError(t, err)
	require.Zero(t, stats.E2EFirstOutputSamples)
	require.Nil(t, stats.E2EFirstOutputP50Ms)

	// A separate hour shares this database and group. The two clocks have
	// different missing observations and opposite sort orders.
	start := epoch.Add(6 * time.Hour)
	stop := start.Add(time.Hour)
	addUsage(start, new(600), new(80))
	addUsage(start.Add(time.Minute), new(300), nil)
	addUsage(start.Add(2*time.Minute), new(450), nil)
	addUsage(start.Add(3*time.Minute), nil, new(20))
	t.Run("independent_hourly_clocks", func(t *testing.T) {
		require.NoError(t, repo.UpsertHourlyMetrics(ctx, start, stop))
		var ttftCount, e2eCount, ttftP50, e2eP50 int
		err := db.QueryRow(`SELECT ttft_sample_count,e2e_first_output_sample_count,ttft_p50_ms,e2e_first_output_p50_ms
			FROM ops_metrics_hourly WHERE bucket_start=$1 AND group_id=$2 AND platform='openai'`, start, id).
			Scan(&ttftCount, &e2eCount, &ttftP50, &e2eP50)
		require.NoError(t, err)
		require.Equal(t, 3, ttftCount)
		require.Equal(t, 2, e2eCount, "only the two measured end-to-end clocks contribute")
		require.Equal(t, 450, ttftP50)
		require.Equal(t, 50, e2eP50)
		result, err := repo.GetDashboardOverview(ctx, &service.OpsDashboardFilter{
			StartTime: start, EndTime: stop, GroupID: new(id), QueryMode: service.OpsQueryModePreagg,
		})
		require.NoError(t, err)
		require.EqualValues(t, 2, result.E2EFirstOutputSampleCount)
		require.Equal(t, new(450), result.TTFT.P50)
		require.Equal(t, new(50), result.E2EFirstOutput.P50)
	})
	t.Run("independent_detail_clocks", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO ops_error_logs(request_id,created_at,group_id,error_phase,error_type,status_code,time_to_first_token_ms,end_to_end_first_output_ms)
			VALUES ('fixture-error',$1,$2,'upstream','upstream_error',502,1,510)`, start, id)
		require.NoError(t, err)
		rows, total, err := repo.ListRequestDetails(ctx, &service.OpsRequestDetailFilter{
			StartTime: &start, EndTime: &stop, GroupID: new(id), Sort: "e2e_first_output_desc",
		})
		require.NoError(t, err)
		require.EqualValues(t, 5, total)
		require.Len(t, rows, 5)
		require.Equal(t, service.OpsRequestKindError, rows[0].Kind, "510 ms error must sort ahead of 80 ms success")
		require.Equal(t, "fixture-error", rows[0].RequestID)
		require.Equal(t, new(1), rows[0].FirstTokenMs)
		require.Equal(t, new(int64(510)), rows[0].EndToEndFirstOutputMs)
		require.Equal(t, service.OpsRequestKindSuccess, rows[1].Kind)
		require.Equal(t, new(600), rows[1].FirstTokenMs)
		require.Equal(t, new(int64(80)), rows[1].EndToEndFirstOutputMs)
		require.Equal(t, new(int64(20)), rows[2].EndToEndFirstOutputMs)
		require.Nil(t, rows[3].EndToEndFirstOutputMs)
		require.Nil(t, rows[4].EndToEndFirstOutputMs)
	})
}
