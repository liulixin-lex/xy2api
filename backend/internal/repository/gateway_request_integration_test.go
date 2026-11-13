//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGatewayRequestsExperienceUnionAndIdempotence(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	u := mustCreateUser(t, client, &service.User{Email: "request-experience@example.test"})
	k := mustCreateApiKey(t, client, &service.APIKey{UserID: u.ID, Key: "synthetic-experience-key", Name: "fixture"})
	a := mustCreateAccount(t, client, &service.Account{Name: "fixture"})
	g := mustCreateGroup(t, client, &service.Group{Name: "fixture"})
	at := time.Now().UTC().Truncate(time.Second)
	ms, input, read, creation := 100, 10, 90, 0
	usage := &service.UsageLog{UserID: u.ID, APIKeyID: k.ID, AccountID: a.ID, GroupID: &g.ID,
		RequestID: uuid.NewString(), Model: "fixture", CreatedAt: at.Add(-time.Hour),
		InputTokens: input, CacheReadTokens: read, CacheCreationTokens: creation}
	first64 := int64(ms)
	usage.EndToEndFirstOutputMs = &first64
	_, err := repo.Create(ctx, usage)
	require.NoError(t, err)
	// This legacy row has no result, but is still in the denominator.
	for i, result := range []int16{1, 2, 3, 4} {
		first := (i + 2) * 100
		row := &service.GatewayUnbilledRequest{RequestID: uuid.NewString(), CreatedAt: at,
			UserID: u.ID, APIKeyID: k.ID, GroupID: &g.ID, Model: "fixture", Result: result,
			FirstOutputMs: &first, InputTokens: &input, CacheReadTokens: &read, CacheCreationTokens: &creation}
		if result == 4 {
			row.InputTokens = nil // An incomplete cache sample must not enter either sum.
			row.CacheReadTokens = new(int)
		}
		require.NoError(t, repo.CreateUnbilledRequest(ctx, row))
		require.NoError(t, repo.CreateUnbilledRequest(ctx, row))
	}
	// Outside either boundary and a different group must not leak into the window.
	for _, when := range []time.Time{at.Add(-time.Hour - time.Microsecond), at.Add(time.Microsecond)} {
		require.NoError(t, repo.CreateUnbilledRequest(ctx, &service.GatewayUnbilledRequest{
			RequestID: uuid.NewString(), CreatedAt: when, GroupID: &g.ID, Result: 3}))
	}
	got, err := repo.GroupLast60(ctx, []int64{g.ID, -1}, at)
	require.NoError(t, err)
	require.EqualValues(t, 5, got[g.ID].Requests)
	require.Equal(t, 300.0, *got[g.ID].TTFOP50Ms)
	require.InDelta(t, 0.2, *got[g.ID].FailedRate, 1e-9)
	require.InDelta(t, 0.9, *got[g.ID].CacheRatio, 1e-9)
	require.Zero(t, got[-1].Requests)
	require.Nil(t, got[-1].CacheRatio)
	require.Nil(t, got[-1].FailedRate)
	require.Nil(t, got[-1].TTFOP50Ms)
}

func TestGatewayRequestsExperienceLargeWindowUsesIndexes(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// Use temporary copies to avoid changing other integration fixtures. The
	// inherited indexes are the actual migrated definitions, not test inventions.
	for _, table := range []string{"usage_logs", "gateway_unbilled_requests"} {
		_, err = tx.ExecContext(ctx, fmt.Sprintf("CREATE TEMP TABLE %s (LIKE public.%s INCLUDING ALL) ON COMMIT DROP", table, table))
		require.NoError(t, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO gateway_unbilled_requests
		(request_id, created_at, user_id, api_key_id, group_id, platform, model, stream, result, duration_ms)
		SELECT 'synthetic-' || n, now() - interval '2 days', 1, 1, n % 100, 'openai', 'fixture', true, 3, 10
		FROM generate_series(1, 200000) n`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `ANALYZE gateway_unbilled_requests; ANALYZE usage_logs`)
	require.NoError(t, err)
	at := time.Now().UTC()
	rows, err := tx.QueryContext(ctx, "EXPLAIN (FORMAT TEXT) "+groupLast60Query, pq.Array([]int64{1, 2}), at.Add(-time.Hour), at)
	require.NoError(t, err)
	var plan string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan += line + "\n"
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Contains(t, plan, "Index", plan)
	require.NotContains(t, plan, "Seq Scan on gateway_unbilled_requests", plan)
	start := time.Now()
	_, err = newUsageLogRepositoryWithSQL(nil, tx).GroupLast60(ctx, []int64{1, 2}, at)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 3*time.Second)
	t.Logf("200000-row bounded union: %s; plan:\n%s", time.Since(start), plan)
}
