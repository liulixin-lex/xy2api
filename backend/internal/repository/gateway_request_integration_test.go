//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
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

func TestAdminRequestListStatsAttemptsShareFacts(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	u := mustCreateUser(t, client, &service.User{Email: "admin-requests@example.test"})
	k := mustCreateApiKey(t, client, &service.APIKey{UserID: u.ID, Key: "synthetic-admin-key", Name: "fixture"})
	a := mustCreateAccount(t, client, &service.Account{Name: "final"})
	b := mustCreateAccount(t, client, &service.Account{Name: "previous"})
	g := mustCreateGroup(t, client, &service.Group{Name: "fixture"})
	at := time.Now().UTC().Truncate(time.Millisecond)
	start, end := at.Add(-time.Hour), at.Add(time.Second)
	ms := int64(100)
	usage := &service.UsageLog{UserID: u.ID, APIKeyID: k.ID, AccountID: a.ID, GroupID: &g.ID,
		RequestID: "billing-fixture", Model: "fixture", CreatedAt: at, InputTokens: 10, CacheReadTokens: 90,
		TotalCost: 1, ActualCost: 0.5}
	usage.EndToEndFirstOutputMs = &ms
	_, err := repo.Create(ctx, usage)
	require.NoError(t, err)
	gatewayID := uuid.NewString()
	_, err = tx.ExecContext(ctx, "UPDATE usage_logs SET gateway_request_id=$1,result=1,switches=0,fallback=false,takeover=false WHERE id=$2", gatewayID, usage.ID)
	require.NoError(t, err)
	old := *usage
	old.ID, old.RequestID, old.EndToEndFirstOutputMs = 0, "old-fixture", nil
	legacyFirst := 999
	old.FirstTokenMs = &legacyFirst
	_, err = repo.Create(ctx, &old)
	require.NoError(t, err)
	input, read, creation, first := 100, 0, 100, 300
	failed := &service.GatewayUnbilledRequest{RequestID: uuid.NewString(), CreatedAt: at,
		UserID: u.ID, APIKeyID: k.ID, GroupID: &g.ID, AccountID: &b.ID, Model: "fixture", Result: 3,
		FirstOutputMs: &first, InputTokens: &input, CacheReadTokens: &read, CacheCreationTokens: &creation}
	require.NoError(t, repo.CreateUnbilledRequest(ctx, failed))
	canceled := *failed
	canceled.RequestID, canceled.Result, canceled.AccountID = uuid.NewString(), 4, nil
	canceled.InputTokens, canceled.FirstOutputMs = nil, nil
	require.NoError(t, repo.CreateUnbilledRequest(ctx, &canceled))
	// Two attempt rows must never multiply request fees or totals. A previous
	// account does not make the final request match that account's filter.
	for i := 0; i < 2; i++ {
		_, err = tx.ExecContext(ctx, `INSERT INTO account_attempt_samples
			(attempt_id,started_at,finished_at,account_id,group_id,model,request_class,stream,warm,outcome,request_id)
			VALUES ($1,$2,$3,$4,$5,'fixture',1,true,false,$6,$7)`, uuid.NewString(), at.Add(time.Duration(i-2)*time.Second), at, b.ID, g.ID, i+3, gatewayID)
		require.NoError(t, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES ('group_quality_gate_policies',$1,now()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`,
		fmt.Sprintf(`{"groups":{"%d":{"v4":{"enabled":true,"ttft_seconds":0.2}}}}`, g.ID))
	require.NoError(t, err)
	filters := usagestats.UsageLogFilters{GroupID: g.ID, StartTime: &start, EndTime: &end, ExactTotal: true}
	page, total, err := repo.ListAdminRequests(ctx, pagination.PaginationParams{Page: 1, PageSize: 20}, filters)
	require.NoError(t, err)
	require.EqualValues(t, 4, total.Total)
	require.Len(t, page, 4)
	for _, row := range page {
		switch {
		case row.GatewayRequestID != nil && *row.GatewayRequestID == gatewayID:
			require.True(t, row.HasAttempts)
			require.Equal(t, "usage", row.Source)
			require.Equal(t, usage.ID, row.Log.ID)
			require.Equal(t, 100, *row.Log.FirstTokenMs)
			require.False(t, *row.TTFOOver)
		case row.Result != nil && *row.Result == 3:
			require.Equal(t, "unbilled", row.Source)
			require.True(t, *row.TTFOOver)
			require.False(t, row.HasAttempts)
		case row.Result == nil:
			require.Nil(t, row.Log.FirstTokenMs, "never substitute attempt timing for missing user timing")
			require.Nil(t, row.TTFOOver)
		case *row.Result == 4:
			require.Nil(t, row.InputTokens)
			require.Nil(t, row.Log.Account)
		}
	}
	stats, err := repo.AdminRequestStats(ctx, filters)
	require.NoError(t, err)
	require.EqualValues(t, 4, stats.TotalRequests)
	require.EqualValues(t, 1, stats.Success)
	require.EqualValues(t, 1, stats.Failed)
	require.Equal(t, 100.0, *stats.TTFOP50Ms) // lower nearest rank for two measurements
	require.InDelta(t, 0.45, *stats.CacheRatio, 1e-9)
	require.Equal(t, 2.0, stats.TotalCost)
	filters.AccountID = b.ID
	filtered, count, err := repo.ListAdminRequests(ctx, pagination.PaginationParams{}, filters)
	require.NoError(t, err)
	require.EqualValues(t, 1, count.Total)
	require.Equal(t, int16(3), *filtered[0].Result)
	stats, err = repo.AdminRequestStats(ctx, filters)
	require.NoError(t, err)
	require.EqualValues(t, count.Total, stats.TotalRequests)
	filters.AccountID = 0
	result := int16(4)
	filters.Result = &result
	stats, err = repo.AdminRequestStats(ctx, filters)
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.TotalRequests)
	require.Nil(t, stats.CacheRatio)
	filters.Result = nil
	bt := int8(0)
	filters.BillingType = &bt
	stats, err = repo.AdminRequestStats(ctx, filters)
	require.NoError(t, err)
	require.EqualValues(t, 2, stats.TotalRequests)
	attempts, err := repo.RequestAttempts(ctx, gatewayID, at)
	require.NoError(t, err)
	require.Len(t, attempts, 2)
	require.EqualValues(t, 3, attempts[1].Outcome, "legacy outcome 4 is only mapped for display")
	require.True(t, attempts[0].StartedAt.Before(attempts[1].StartedAt))
	attempts, err = repo.RequestAttempts(ctx, gatewayID, at.Add(-8*24*time.Hour))
	require.NoError(t, err)
	require.Empty(t, attempts)
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
	from := at.Add(-time.Hour)
	filters := usagestats.UsageLogFilters{GroupID: 1, StartTime: &from, EndTime: &at}
	start = time.Now()
	stats, err := newUsageLogRepositoryWithSQL(nil, tx).AdminRequestStats(ctx, filters)
	require.NoError(t, err)
	require.Zero(t, stats.TotalRequests)
	require.Less(t, time.Since(start), 3*time.Second)
	where, args, err := adminRequestWhere(filters)
	require.NoError(t, err)
	rows, err = tx.QueryContext(ctx, "EXPLAIN (FORMAT TEXT) "+adminRequestsCTE+"SELECT id FROM requests "+where+" ORDER BY created_at DESC LIMIT 20", args...)
	require.NoError(t, err)
	plan = ""
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan += line + "\n"
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.NotContains(t, plan, "Seq Scan on gateway_unbilled_requests", plan)
	require.Contains(t, plan, "Index", plan)
	t.Logf("admin union list plan:\n%s", plan)
}
