//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// northstarPostgres isolates committed rollups and usage fixtures from the
// package's shared database, including its heap pages and planner statistics.
// The repository owns its transactions, so an outer rollback cannot isolate
// these tests. A separate server also avoids VACUUM/ANALYZE on shared tables.
func northstarPostgres(t *testing.T) (*sql.DB, *dbent.Client) {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag),
		tcpostgres.WithDatabase("xy2api_northstar_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		require.NoError(t, pg.Terminate(cleanupCtx))
	})
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, ApplyMigrations(ctx, db))
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return db, client
}

func northstarCSV(t *testing.T, name string) []map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "northstar", name))
	require.NoError(t, err)
	records, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	require.NoError(t, err)
	out := make([]map[string]string, 0, len(records)-1)
	for _, record := range records[1:] {
		row := map[string]string{}
		for i, col := range records[0] {
			row[col] = record[i]
		}
		out = append(out, row)
	}
	return out
}

func TestCacheNorthstarPostgresReconciliation(t *testing.T) {
	ctx := context.Background()
	db, client := northstarPostgres(t)
	manifest, err := os.ReadFile("testdata/northstar/SHA256.json")
	require.NoError(t, err)
	hashes := map[string]string{}
	require.NoError(t, json.Unmarshal(manifest, &hashes))
	for name, hash := range hashes {
		raw, e := os.ReadFile(filepath.Join("testdata", "northstar", name))
		require.NoError(t, e)
		require.Equal(t, hash, fmt.Sprintf("%x", sha256.Sum256(raw)))
	}
	user := mustCreateUser(t, client, &service.User{})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: fmt.Sprintf("northstar-%d", user.ID), Name: "synthetic"})
	accounts := map[string]int64{}
	groups := map[string]int64{}
	originalGroups := map[int64]string{0: "0"}
	for _, platform := range []string{"openai", "anthropic", "gemini"} {
		a := mustCreateAccount(t, client, &service.Account{Name: "northstar-" + platform, Platform: platform, Type: "apikey", Credentials: map[string]any{"base_url": "https://credential:synthetic@relay.example/v1"}})
		accounts[platform] = a.ID
	}
	for _, row := range northstarCSV(t, "usage.csv") {
		var group any
		if row["group_id"] != "" {
			label := row["platform"] + ":" + row["group_id"]
			if groups[label] == 0 {
				g := mustCreateGroup(t, client, &service.Group{Name: "northstar-" + label, Platform: row["platform"]})
				groups[label] = g.ID
				originalGroups[g.ID] = row["group_id"]
			}
			group = groups[label]
		}
		nullable := func(s string) any {
			if s == "" {
				return nil
			}
			return s
		}
		_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,model,upstream_model,created_at,
 input_tokens,cache_read_tokens,cache_creation_tokens,image_count,video_count,session_id,
 input_cost,cache_read_cost,cache_creation_cost,output_cost,total_cost,actual_cost,rate_multiplier,account_rate_multiplier,account_stats_cost,user_agent,inbound_endpoint)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,'codex_cli_rs/fixture','/v1/responses')`,
			user.ID, key.ID, accounts[row["platform"]], group, row["model"], nullable(row["upstream_model"]), row["created_at"],
			row["input_tokens"], row["cache_read_tokens"], row["cache_creation_tokens"], row["image_count"], row["video_count"], nullable(row["session_key_hmac"]),
			row["input_cost"], row["cache_read_cost"], row["cache_creation_cost"], row["output_cost"], row["total_cost"], row["actual_cost"], row["rate_multiplier"], nullable(row["account_rate_multiplier"]), nullable(row["account_stats_cost"]))
		require.NoError(t, err)
	}
	p := service.CacheNorthstarPolicy{PolicyVersion: "synthetic-reconciliation", ExcludeMedia: true, UnknownThreshold: "aux_only",
		ModelThresholds:  []service.CacheNorthstarThresholdRule{{Match: "exact", Model: "gpt-5.6", Threshold: 1024}, {Match: "prefix", Model: "claude-", Threshold: 2048}, {Match: "exact", Model: "gemini-2.5", Threshold: 4096}},
		ExcludedGroupIDs: []int64{groups["openai:99"]}}

	start := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	repo := NewCacheNorthstarRepository(db)
	// Independent hand calculations for the two cross-hour predecessor pairs:
	// 1->3: max(min(4096,260255)-198838,0)=0;
	// 5->7: max(min(121893,117553)-100542,0)=17011.
	// Querying the 11:00 hour first also proves no earlier rollup/state is needed.
	for _, tc := range []struct {
		hour, at time.Time
		want     int64
	}{
		{start.Add(time.Hour), start.Add(70 * time.Minute), 17011},
		{start, start.Add(10 * time.Minute), 0},
	} {
		var opportunity int64
		query := cacheNorthstarSourceSQL + "SELECT o_recorded_tokens FROM northstar_values WHERE id=(SELECT id FROM usage_logs WHERE api_key_id=$3 AND created_at=$4)"
		require.NoError(t, db.QueryRowContext(ctx, query, tc.hour, tc.hour.Add(time.Hour), key.ID, tc.at).Scan(&opportunity))
		require.Equal(t, tc.want, opportunity)
	}
	require.NoError(t, repo.AggregateCacheNorthstar(ctx, start, end, p, end, false))
	first, err := repo.ReadCacheNorthstar(ctx, p.PolicyVersion, start, end, "all")
	require.NoError(t, err)
	require.Len(t, first.Hours, 2)
	// Compare the common six-dimensional projection against A's independent
	// reference. Expanded account/client/protocol grouping is tested separately.
	expected := map[string]map[string]int64{}
	names := []string{"requests", "requests_hit", "p_tokens", "read_tokens", "read_maybe_rewritten", "o_recorded_tokens", "input_tokens_uncached", "creation_tokens", "upstream_covered_p_tokens", "component_mismatch_requests", "input_cost_micros", "read_cost_micros", "creation_cost_micros", "read_x_charged_mult_micro", "o_x_charged_mult_micro", "read_x_upstream_mult_micro", "o_x_upstream_mult_micro"}
	for _, row := range northstarCSV(t, "rollup.csv") {
		key := strings.Join([]string{row["bucket_start"], row["platform"], row["group_id"], row["model_key"], row["threshold_band"], row["has_media"]}, "|")
		vals := map[string]int64{}
		for _, name := range names {
			n, e := strconv.ParseInt(row[name], 10, 64)
			require.NoError(t, e)
			vals[name] = n
		}
		expected[key] = vals
	}
	actual := map[string]map[string]int64{}
	for _, row := range first.Rows {
		media := "f"
		if row.HasMedia {
			media = "t"
		}
		k := strings.Join([]string{row.BucketStart.Format(time.RFC3339), row.Platform, originalGroups[row.GroupID], row.Model, strconv.Itoa(row.ThresholdBand), media}, "|")
		require.Equal(t, "relay.example", row.UpstreamHost)
		require.Equal(t, "codex_cli_rs", row.ClientFamily)
		require.Equal(t, "responses_http", row.Protocol)
		vals := []int64{row.Requests, row.RequestsHit, row.PromptTokens, row.ReadTokens, row.ReadMaybeRewritten, row.OpportunityTokens, row.InputTokens, row.CreationTokens, row.UpstreamCoveredPromptTokens, row.ComponentMismatchRequests, row.InputCostMicros, row.ReadCostMicros, row.CreationCostMicros, row.ReadChargedMicro, row.OpportunityChargedMicro, row.ReadUpstreamMicro, row.OpportunityUpstreamMicro}
		if actual[k] == nil {
			actual[k] = map[string]int64{}
		}
		for i, name := range names {
			actual[k][name] += vals[i]
		}
	}
	require.Equal(t, expected, actual)
	require.NoError(t, repo.AggregateCacheNorthstar(ctx, start, end, p, end, false))
	replay, err := repo.ReadCacheNorthstar(ctx, p.PolicyVersion, start, end, "all")
	require.NoError(t, err)
	require.Equal(t, first, replay)

	// A late completed request must appear on D-1 replay without changing any
	// existing dashboard row, watermark or usage row.
	_, err = db.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,model,input_tokens,cache_read_tokens,created_at,session_id)
 SELECT user_id,api_key_id,account_id,group_id,model,2048,2048,$2,session_id
 FROM usage_logs WHERE api_key_id=$1 ORDER BY created_at,id LIMIT 1`, key.ID, start.Add(50*time.Minute))
	require.NoError(t, err)
	snapshot := func() map[string]string {
		values := map[string]string{}
		for _, table := range []string{"usage_dashboard_hourly", "usage_dashboard_daily", "usage_dashboard_hourly_users", "usage_dashboard_daily_users", "usage_dashboard_aggregation_watermark", "usage_logs"} {
			query := "SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text,'[]') FROM " + table + " t"
			args := []any{}
			if table == "usage_logs" {
				query += " WHERE api_key_id=$1"
				args = append(args, key.ID)
			}
			var value string
			require.NoError(t, db.QueryRowContext(ctx, query, args...).Scan(&value))
			values[table] = value
		}
		return values
	}
	unchanged := snapshot()
	day := start.Truncate(24 * time.Hour)
	require.NoError(t, repo.AggregateCacheNorthstar(ctx, day, day.Add(24*time.Hour), p, end.Add(24*time.Hour), true))
	late, err := repo.ReadCacheNorthstar(ctx, p.PolicyVersion, start, end, "all")
	require.NoError(t, err)
	var beforeTotals, afterTotals service.CacheNorthstarTotals
	for _, row := range first.Rows {
		beforeTotals.Add(row.CacheNorthstarTotals)
	}
	for _, row := range late.Rows {
		afterTotals.Add(row.CacheNorthstarTotals)
	}
	require.Equal(t, beforeTotals.Requests+1, afterTotals.Requests)
	require.Equal(t, beforeTotals.PromptTokens+4096, afterTotals.PromptTokens)
	require.Equal(t, unchanged, snapshot())

	// UTC conversion, filtered hour exclusion counts, and a null-group row which
	// is outside the right-open window are exercised by the same frozen fixture.
	openai, err := repo.ReadCacheNorthstar(ctx, p.PolicyVersion, start, end, "openai")
	require.NoError(t, err)
	require.EqualValues(t, 1, openai.Hours[1].MediaRequests)
	require.EqualValues(t, 1, openai.Hours[1].ZeroPromptRequests)
	require.EqualValues(t, 1, openai.Hours[1].ProbeGroupRequests)
}

func TestCacheNorthstarPostgresAtomicReplay(t *testing.T) {
	ctx := context.Background()
	db, _ := northstarPostgres(t)
	repo := NewCacheNorthstarRepository(db)
	p := service.CacheNorthstarPolicy{PolicyVersion: "atomic-replay"}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	require.NoError(t, repo.AggregateCacheNorthstar(ctx, end.Add(-time.Hour), end, p, end, false))
	require.NoError(t, repo.AggregateCacheNorthstar(ctx, start, end, p, end, true))
	state, err := repo.GetCacheNorthstarState(ctx)
	require.NoError(t, err)
	require.Equal(t, start, state.BackfilledFrom)
	require.Equal(t, end, state.AggregatedUntil)
	require.Equal(t, start, *state.ReconciledDay)
	data, err := repo.ReadCacheNorthstar(ctx, p.PolicyVersion, start, end, "all")
	require.NoError(t, err)
	require.Len(t, data.Hours, 24)
	// A server-side failure after a DELETE must roll back replacements AND state.
	_, err = db.Exec(`CREATE FUNCTION cache_northstar_test_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$;
 CREATE TRIGGER cache_northstar_test_fail BEFORE INSERT ON cache_northstar_hours FOR EACH ROW EXECUTE FUNCTION cache_northstar_test_fail()`)
	require.NoError(t, err)

	require.Error(t, repo.AggregateCacheNorthstar(ctx, start, end, p, end.Add(time.Hour), true))
	after, err := repo.GetCacheNorthstarState(ctx)
	require.NoError(t, err)
	require.Equal(t, state, after)
	again, err := repo.ReadCacheNorthstar(ctx, p.PolicyVersion, start, end, "all")
	require.NoError(t, err)
	require.Equal(t, data, again)
}
