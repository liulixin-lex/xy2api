//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAdminRequestTimingPreservesV064ValuesAndSeparatesGroupExperience(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	u := mustCreateUser(t, client, &service.User{Email: "timing@example.test"})
	k := mustCreateApiKey(t, client, &service.APIKey{UserID: u.ID, Key: uuid.NewString(), Name: "timing"})
	a := mustCreateAccount(t, client, &service.Account{Name: "timing"})
	g := mustCreateGroup(t, client, &service.Group{Name: "timing"})
	at := time.Now().UTC().Truncate(time.Millisecond)
	start, end := at.Add(-time.Minute), at.Add(time.Second)
	_, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES ('group_quality_gate_policies',$1,now()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`,
		fmt.Sprintf(`{"groups":{"%d":{"v4":{"enabled":true,"ttft_seconds":0.15}}}}`, g.ID))
	require.NoError(t, err)

	for _, tc := range []struct {
		name            string
		first, duration *int
		endToEndFirst   int64
	}{
		{"original", new(50), new(140), 220},
		{"slow", new(300), new(500), 800},
		{"measured_zero", new(0), new(0), 100},
		{"unknown", nil, nil, 900},
	} {
		row := &service.UsageLog{UserID: u.ID, APIKeyID: k.ID, AccountID: a.ID, GroupID: &g.ID,
			RequestID: uuid.NewString(), Model: tc.name, CreatedAt: at, FirstTokenMs: tc.first, DurationMs: tc.duration,
			EndToEndFirstOutputMs: &tc.endToEndFirst}
		_, err := repo.Create(ctx, row)
		require.NoError(t, err)
		filters := usagestats.UsageLogFilters{GroupID: g.ID, Model: tc.name, StartTime: &start, EndTime: &end}
		page, _, err := repo.ListAdminRequests(ctx, pagination.PaginationParams{Page: 1, PageSize: 20}, filters)
		require.NoError(t, err)
		require.Len(t, page, 1)
		require.Equal(t, tc.first, page[0].Log.FirstTokenMs, "D27 keeps v0.6.4 first_token_ms even when end-to-end output is available")
		require.Equal(t, tc.duration, page[0].Log.DurationMs, "D27 keeps v0.6.4 duration_ms")
		stats, err := repo.AdminRequestStats(ctx, filters)
		require.NoError(t, err)
		if tc.first == nil {
			require.Nil(t, page[0].TTFOOver)
			require.Nil(t, stats.TTFOP50Ms, "end-to-end output must not fill missing original timing")
		} else {
			require.Equal(t, *tc.first > 150, *page[0].TTFOOver)
			require.EqualValues(t, *tc.first, *stats.TTFOP50Ms)
			require.EqualValues(t, *tc.duration, stats.AverageDurationMs)
		}
	}

	// Administrator and user rows use the same untouched stored fields.
	users, _, err := repo.ListByUser(ctx, u.ID, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	page, _, err := repo.ListAdminRequests(ctx, pagination.PaginationParams{Page: 1, PageSize: 20},
		usagestats.UsageLogFilters{GroupID: g.ID, StartTime: &start, EndTime: &end})
	require.NoError(t, err)
	require.Len(t, page, len(users))
	byID := make(map[int64]service.UsageLog, len(users))
	for _, row := range users {
		byID[row.ID] = row
	}
	for _, row := range page {
		require.Equal(t, byID[row.Log.ID].FirstTokenMs, row.Log.FirstTokenMs)
		require.Equal(t, byID[row.Log.ID].DurationMs, row.Log.DurationMs)
	}

	// Unbilled rows have no v0.6.4 bill: retain their recorded timing, including
	// unknown output, and include the same displayed values in usage P50.
	for _, first := range []*int{new(75), nil} {
		id := uuid.NewString()
		require.NoError(t, repo.CreateUnbilledRequest(ctx, &service.GatewayUnbilledRequest{
			RequestID: id, CreatedAt: at, UserID: u.ID, APIKeyID: k.ID, GroupID: &g.ID,
			Model: "unbilled", Result: 3, FirstOutputMs: first, DurationMs: 100}))
		page, _, err := repo.ListAdminRequests(ctx, pagination.PaginationParams{Page: 1, PageSize: 20},
			usagestats.UsageLogFilters{RequestID: id, StartTime: &start, EndTime: &end})
		require.NoError(t, err)
		require.Len(t, page, 1)
		require.Equal(t, first, page[0].Log.FirstTokenMs)
		require.Equal(t, 100, *page[0].Log.DurationMs)
	}
	stats, err := repo.AdminRequestStats(ctx, usagestats.UsageLogFilters{GroupID: g.ID, StartTime: &start, EndTime: &end})
	require.NoError(t, err)
	require.EqualValues(t, 6, stats.TotalRequests)
	require.Equal(t, new(50.0), stats.TTFOP50Ms, "original row values: 0,50,75,300; nulls excluded")
	experience, err := repo.GroupLast60(ctx, []int64{g.ID}, end)
	require.NoError(t, err)
	require.EqualValues(t, 6, experience[g.ID].Requests)
	require.Equal(t, new(220.0), experience[g.ID].TTFOP50Ms, "group keeps end-to-end values: 75,100,220,800,900")
}
