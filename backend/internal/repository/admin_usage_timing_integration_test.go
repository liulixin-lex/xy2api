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

func TestAdminRequestTimingPairsEndToEndWithoutChangingAccounting(t *testing.T) {
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
		name                    string
		first, duration         *int64
		wantFirst, wantDuration int
		wantOver                bool
		unknown                 bool
	}{
		{"complete_pair", new(int64(220)), new(int64(300)), 220, 300, true, false},
		{"legacy", nil, nil, 50, 140, false, false},
		{"missing_duration", new(int64(220)), nil, 50, 140, false, false},
		{"missing_first", nil, new(int64(300)), 50, 140, false, false},
		{"measured_zero", new(int64(0)), new(int64(0)), 0, 0, false, false},
		{"unknown_attempt", new(int64(220)), nil, 0, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := &service.UsageLog{UserID: u.ID, APIKeyID: k.ID, AccountID: a.ID, GroupID: &g.ID,
				RequestID: uuid.NewString(), Model: tc.name, CreatedAt: at, FirstTokenMs: new(50), DurationMs: new(140),
				EndToEndFirstOutputMs: tc.first, EndToEndDurationMs: tc.duration}
			if tc.unknown {
				row.FirstTokenMs, row.DurationMs = nil, nil
			}
			_, err := repo.Create(ctx, row)
			require.NoError(t, err)
			filters := usagestats.UsageLogFilters{GroupID: g.ID, Model: tc.name, StartTime: &start, EndTime: &end}
			page, _, err := repo.ListAdminRequests(ctx, pagination.PaginationParams{Page: 1, PageSize: 20}, filters)
			require.NoError(t, err)
			require.Len(t, page, 1)
			if tc.unknown {
				require.Nil(t, page[0].Log.FirstTokenMs)
				require.Nil(t, page[0].Log.DurationMs)
				require.Nil(t, page[0].TTFOOver)
				return
			}
			require.Equal(t, tc.wantFirst, *page[0].Log.FirstTokenMs)
			require.Equal(t, tc.wantDuration, *page[0].Log.DurationMs)
			require.LessOrEqual(t, *page[0].Log.FirstTokenMs, *page[0].Log.DurationMs)
			require.Equal(t, tc.wantOver, *page[0].TTFOOver, "color follows displayed first output")
			stats, err := repo.AdminRequestStats(ctx, filters)
			require.NoError(t, err)
			require.Equal(t, float64(140), stats.AverageDurationMs, "average duration keeps billing scope")
			original, err := repo.GetByID(ctx, row.ID)
			require.NoError(t, err)
			require.Equal(t, 50, *original.FirstTokenMs)
			require.Equal(t, 140, *original.DurationMs)
		})
	}

	// The unbilled path already stores ingress-relative duration and has no
	// billing column pair to switch. Missing first output stays unknown.
	for _, first := range []*int{new(220), nil} {
		id := uuid.NewString()
		require.NoError(t, repo.CreateUnbilledRequest(ctx, &service.GatewayUnbilledRequest{
			RequestID: id, CreatedAt: at, UserID: u.ID, APIKeyID: k.ID, GroupID: &g.ID,
			Model: "unbilled", Result: 3, FirstOutputMs: first, DurationMs: 300}))
		page, _, err := repo.ListAdminRequests(ctx, pagination.PaginationParams{Page: 1, PageSize: 20},
			usagestats.UsageLogFilters{RequestID: id, StartTime: &start, EndTime: &end})
		require.NoError(t, err)
		require.Len(t, page, 1)
		require.Equal(t, first, page[0].Log.FirstTokenMs)
		require.Equal(t, 300, *page[0].Log.DurationMs)
		if first == nil {
			require.Nil(t, page[0].TTFOOver)
		} else {
			require.True(t, *page[0].TTFOOver)
			require.LessOrEqual(t, *page[0].Log.FirstTokenMs, *page[0].Log.DurationMs)
		}
	}
}
