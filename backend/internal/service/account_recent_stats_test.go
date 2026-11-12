//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recentStatsRepoCall struct {
	ids        []int64
	asOf       time.Time
	monitoring bool
}
type recentStatsRepoStub struct {
	calls []recentStatsRepoCall
	rows  []AccountRecentStatsRow
	err   error
}

func (r *recentStatsRepoStub) Batch(_ context.Context, ids []int64, asOf time.Time, monitoring bool) ([]AccountRecentStatsRow, error) {
	r.calls = append(r.calls, recentStatsRepoCall{append([]int64(nil), ids...), asOf, monitoring})
	return r.rows, r.err
}
func recentStatsFloat(v float64) *float64 { return &v }

func TestRecentStatsScenario20Tiers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value float64
		n     int64
		want  string
	}{
		{"four_samples", 5000, 4, ""}, {"five_samples", 5000, 5, "good"},
		{"five_seconds", 5000, 8, "good"}, {"above_five_unrounded", 5000.001, 8, "mid"},
		{"ten_seconds", 10000, 8, "mid"}, {"above_ten_unrounded", 10000.001, 8, "bad"},
	} {
		t.Run("ttft/"+tc.name, func(t *testing.T) {
			item := recentStatsItem(AccountRecentStatsRow{TTFTP50MS: recentStatsFloat(tc.value), TTFTN: tc.n}, true)
			require.Equal(t, tc.value, *item.TTFT.P50MS)
			if tc.want == "" {
				require.Nil(t, item.TTFT.Tier)
			} else {
				require.Equal(t, tc.want, *item.TTFT.Tier)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		bad, n int64
		want   string
	}{
		{"twenty_nine", 1, 29, ""}, {"thirty", 1, 30, "bad"}, {"zero", 0, 30, "good"},
		{"one_percent", 1, 100, "good"}, {"above_one_unrounded", 1, 99, "mid"},
		{"three_percent", 3, 100, "mid"}, {"above_three_unrounded", 3, 99, "bad"},
	} {
		t.Run("error/"+tc.name, func(t *testing.T) {
			item := recentStatsItem(AccountRecentStatsRow{ErrorN: tc.n, ErrorBad: tc.bad}, true)
			require.Equal(t, float64(tc.bad)/float64(tc.n), *item.Error.Rate)
			if tc.want == "" {
				require.Nil(t, item.Error.Tier)
			} else {
				require.Equal(t, tc.want, *item.Error.Tier)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		value float64
		n     int64
		want  string
	}{
		{"four_samples", .85, 4, ""}, {"five_samples", .85, 5, "good"}, {"eighty_five", .85, 10, "good"},
		{"below_eighty_five_unrounded", .849999, 10, "mid"}, {"seventy_five", .75, 10, "mid"},
		{"below_seventy_five_unrounded", .749999, 10, "bad"}, {"zero_read", 0, 5, "bad"},
	} {
		t.Run("cache/"+tc.name, func(t *testing.T) {
			item := recentStatsItem(AccountRecentStatsRow{CacheRatio: recentStatsFloat(tc.value), CacheN: tc.n}, true)
			require.Equal(t, tc.value, *item.Cache.Ratio)
			if tc.want == "" {
				require.Nil(t, item.Cache.Tier)
			} else {
				require.Equal(t, tc.want, *item.Cache.Tier)
			}
		})
	}
}

func TestRecentStatsScenario23EmptyAndOnlyFailures(t *testing.T) {
	repo := &recentStatsRepoStub{rows: []AccountRecentStatsRow{{AccountID: 2, ErrorN: 2, ErrorBad: 2}}}
	s := NewAccountRecentStatsService(repo, &OpsService{})
	got, err := s.Batch(context.Background(), []int64{2, 1, 2})
	require.NoError(t, err)
	require.Len(t, got.Items, 2)
	require.Equal(t, AccountRecentStatsItem{AccountID: 1, Error: &AccountRecentStatsError{}}, got.Items[0])
	require.Nil(t, got.Items[1].TTFT.P50MS)
	require.Nil(t, got.Items[1].Cache.Ratio)
	require.Equal(t, 1.0, *got.Items[1].Error.Rate)
	require.Nil(t, got.Items[1].Error.Tier)
	empty, err := s.Batch(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, empty.Items)
	require.Empty(t, empty.Items)
	require.Len(t, repo.calls, 1)
}

func TestRecentStatsScenario24MonitoringAndCutoffCache(t *testing.T) {
	ctx := context.Background()
	ops := &OpsService{}
	repo := &recentStatsRepoStub{rows: []AccountRecentStatsRow{{AccountID: 1, ErrorN: 100, ErrorBad: 1}}}
	s := NewAccountRecentStatsService(repo, ops)
	now := time.Date(2026, 11, 12, 12, 0, 14, 999, time.FixedZone("test", -7*3600))
	s.now = func() time.Time { return now }
	first, err := s.Batch(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 12, 19, 0, 0, 0, time.UTC), first.AsOf)
	require.Equal(t, .01, *first.Items[0].Error.Rate)
	repo.rows[0].ErrorBad = 2
	cached, err := s.Batch(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, .01, *cached.Items[0].Error.Rate)
	require.Len(t, repo.calls, 1)
	ops.SetMonitoringEnabled(false)
	off, err := s.Batch(ctx, []int64{1})
	require.NoError(t, err)
	require.Nil(t, off.Items[0].Error)
	require.False(t, repo.calls[1].monitoring)
	ops.SetMonitoringEnabled(true)
	on, err := s.Batch(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, .02, *on.Items[0].Error.Rate)
	require.True(t, repo.calls[2].monitoring)
	repo.rows[0].ErrorBad = 3
	now = now.Add(time.Second)
	fresh, err := s.Batch(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, first.AsOf.Add(15*time.Second), fresh.AsOf)
	require.Equal(t, .03, *fresh.Items[0].Error.Rate)
	require.Len(t, repo.calls, 4)
}

func TestRecentStatsScenario25BatchAndBoundedCache(t *testing.T) {
	repo := &recentStatsRepoStub{}
	s := NewAccountRecentStatsService(repo, &OpsService{})
	now := time.Date(2026, 11, 12, 12, 0, 7, 0, time.UTC)
	s.now = func() time.Time { return now }
	ids := make([]int64, 100)
	for i := range ids {
		ids[i] = int64(100 - i)
	}
	got, err := s.Batch(context.Background(), ids)
	require.NoError(t, err)
	require.Len(t, got.Items, 100)
	require.Len(t, repo.calls, 1)
	for i, item := range got.Items {
		require.EqualValues(t, i+1, item.AccountID)
	}
	require.Equal(t, got.AsOf, repo.calls[0].asOf)
	_, err = s.Batch(context.Background(), []int64{102, 100, 101})
	require.NoError(t, err)
	require.Equal(t, []int64{101, 102}, repo.calls[1].ids)
	require.Equal(t, got.AsOf, repo.calls[1].asOf)
	for batch := 0; batch < 11; batch++ {
		ids := make([]int64, RecentStatsMaxAccounts)
		for i := range ids {
			ids[i] = int64(batch*RecentStatsMaxAccounts + i + 1)
		}
		_, err := s.Batch(context.Background(), ids)
		require.NoError(t, err)
		require.LessOrEqual(t, len(s.cache), recentStatsCacheMaxAccounts)
	}
	now = now.Add(15 * time.Second)
	_, err = s.Batch(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Len(t, s.cache, 1)
}

func TestRecentStatsQueryFailureIsNotCached(t *testing.T) {
	repo := &recentStatsRepoStub{err: errors.New("database unavailable")}
	s := NewAccountRecentStatsService(repo, &OpsService{})
	_, err := s.Batch(context.Background(), []int64{1})
	require.EqualError(t, err, "database unavailable")
	require.Empty(t, s.cache)
	repo.err = nil
	_, err = s.Batch(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Len(t, repo.calls, 2)
}
