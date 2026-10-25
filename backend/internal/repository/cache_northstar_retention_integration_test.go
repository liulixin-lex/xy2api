//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Real PostgreSQL semantics for batched ctid deletes and strict cutoffs.
func TestCacheNorthstarPostgresRetention(t *testing.T) {
	ctx := context.Background()
	db, _ := northstarPostgres(t)
	rollup := time.Date(2026, 4, 28, 4, 0, 0, 0, time.UTC)
	trace := time.Date(2026, 7, 27, 4, 0, 0, 0, time.UTC)
	for _, query := range []string{
		// More than one batch of expired rollups, one boundary row and one newer row.
		`INSERT INTO cache_northstar_hourly(policy_version,bucket_start,platform,group_id,account_id,upstream_host,client_family,protocol,model_key,threshold_band,has_media)
 SELECT 'v', $1::timestamptz - INTERVAL '1 hour', 'openai', 1, g, 'h', 'c', 'p', 'm', 0, false FROM generate_series(1, 5003) g
 UNION ALL SELECT 'v', $1::timestamptz, 'openai', 1, 1, 'h', 'c', 'p', 'm', 0, false
 UNION ALL SELECT 'v', $1::timestamptz + INTERVAL '1 hour', 'openai', 1, 1, 'h', 'c', 'p', 'm', 0, false`,
		`INSERT INTO cache_northstar_hours(policy_version,bucket_start)
 SELECT 'v', $1::timestamptz + g * INTERVAL '1 hour' FROM generate_series(-3, 1) g`,
		`INSERT INTO cache_observability_events(at,kind,label)
 SELECT $1::timestamptz + g * INTERVAL '1 second', 'config', 'e' || g FROM generate_series(-2, 1) g`,
	} {
		_, err := db.ExecContext(ctx, query, rollup)
		require.NoError(t, err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO cache_trace_hourly(bucket_start,process_id,platform,transport,relay_family,pck_class_out,loss_class,model_gen,fork_kind)
 SELECT $1::timestamptz - INTERVAL '1 hour', 'p' || g, 'openai', 'http', 'r', 'none', 'none', 'g', 'none' FROM generate_series(1, 10001) g
 UNION ALL SELECT $1::timestamptz, 'p', 'openai', 'http', 'r', 'none', 'none', 'g', 'none'
 UNION ALL SELECT $1::timestamptz + INTERVAL '1 hour', 'p', 'openai', 'http', 'r', 'none', 'none', 'g', 'none'`, trace)
	require.NoError(t, err)
	// Rollup-age trace rows prove each table uses its own cutoff.
	_, err = db.ExecContext(ctx, `INSERT INTO cache_trace_hourly(bucket_start,process_id,platform,transport,relay_family,pck_class_out,loss_class,model_gen,fork_kind)
 VALUES ($1::timestamptz + INTERVAL '1 day', 'p', 'openai', 'http', 'r', 'none', 'none', 'g', 'none')`, rollup)
	require.NoError(t, err)

	repo := NewCacheNorthstarRepository(db)
	require.NoError(t, repo.CleanupCacheNorthstar(ctx, rollup, trace))
	for table, want := range map[string]struct {
		column string
		cutoff time.Time
		rows   int
	}{
		"cache_northstar_hourly":     {"bucket_start", rollup, 2},
		"cache_northstar_hours":      {"bucket_start", rollup, 2},
		"cache_observability_events": {"at", rollup, 2},
		"cache_trace_hourly":         {"bucket_start", trace, 2},
	} {
		var rows int
		var oldest time.Time
		require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*), min("+want.column+") FROM "+table).Scan(&rows, &oldest), table)
		require.Equal(t, want.rows, rows, table)
		require.True(t, oldest.Equal(want.cutoff), table)
	}
	// Nothing left to expire: the probe alone succeeds.
	require.NoError(t, repo.CleanupCacheNorthstar(ctx, rollup, trace))
}
