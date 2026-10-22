//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 读路径（LATERAL）与跨实例认领在真实 PostgreSQL 上的行为。

func newReadPathMonitor(t *testing.T, repo service.ChannelMonitorRepository, primary string, extras []string) *service.ChannelMonitor {
	t.Helper()
	ctx := context.Background()
	m := &service.ChannelMonitor{
		Name:             "read-path-" + primary,
		Provider:         service.MonitorProviderOpenAI,
		APIMode:          service.MonitorAPIModeChatCompletions,
		Endpoint:         "https://api.openai.com",
		APIKey:           "encrypted",
		PrimaryModel:     primary,
		ExtraModels:      extras,
		Enabled:          true,
		IntervalSeconds:  60,
		BodyOverrideMode: service.MonitorBodyOverrideModeOff,
	}
	require.NoError(t, repo.Create(ctx, m))
	t.Cleanup(func() { _ = repo.Delete(ctx, m.ID) })
	return m
}

func TestChannelMonitorReadPathReturnsConfiguredModelsNewestFirst(t *testing.T) {
	ctx := context.Background()
	repo := NewChannelMonitorRepository(integrationEntClient, integrationDB)
	m := newReadPathMonitor(t, repo, "rp-primary", []string{"rp-extra", "rp-primary"})

	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	var rows []*service.ChannelMonitorHistoryRow
	for i := 0; i < 5; i++ {
		lat := 100 + i
		for _, model := range []string{"rp-primary", "rp-extra", "rp-removed"} {
			rows = append(rows, &service.ChannelMonitorHistoryRow{
				MonitorID: m.ID, Model: model, Status: service.MonitorStatusOperational,
				LatencyMs: &lat, CheckedAt: base.Add(time.Duration(i) * time.Minute),
			})
		}
	}
	require.NoError(t, repo.InsertHistoryBatch(ctx, rows))

	latest, err := repo.ListLatestForMonitorIDs(ctx, []int64{m.ID})
	require.NoError(t, err)
	got := map[string]int{}
	for _, l := range latest[m.ID] {
		require.NotNil(t, l.LatencyMs)
		got[l.Model] = *l.LatencyMs
	}
	require.Equal(t, map[string]int{"rp-primary": 104, "rp-extra": 104}, got,
		"newest row per configured model; a duplicated model once; removed models are gone")

	perModel, err := repo.ListLatestPerModel(ctx, m.ID)
	require.NoError(t, err)
	require.Len(t, perModel, 2)

	timeline, err := repo.ListRecentHistoryForMonitors(ctx, []int64{m.ID}, map[int64]string{m.ID: "rp-primary"}, 3)
	require.NoError(t, err)
	require.Len(t, timeline[m.ID], 3)
	require.True(t, timeline[m.ID][0].CheckedAt.After(timeline[m.ID][1].CheckedAt), "newest first")
	require.Equal(t, 104, *timeline[m.ID][0].LatencyMs)
}

func TestChannelMonitorClaimCheckAllowsOneClaimPerGap(t *testing.T) {
	ctx := context.Background()
	repo := NewChannelMonitorRepository(integrationEntClient, integrationDB)
	m := newReadPathMonitor(t, repo, "claim-primary", nil)
	claimer, ok := repo.(interface {
		ClaimCheck(ctx context.Context, id int64, minGap time.Duration) (bool, error)
	})
	require.True(t, ok)

	first, err := claimer.ClaimCheck(ctx, m.ID, time.Minute)
	require.NoError(t, err)
	require.True(t, first, "never checked: the first claim wins")

	second, err := claimer.ClaimCheck(ctx, m.ID, time.Minute)
	require.NoError(t, err)
	require.False(t, second, "a second instance within the gap must not claim the same round")

	forced, err := claimer.ClaimCheck(ctx, m.ID, 0)
	require.NoError(t, err)
	require.True(t, forced, "a zero gap (first round after an edit) always claims")

	loaded, err := repo.GetByID(ctx, m.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.LastCheckedAt)

	missing, err := claimer.ClaimCheck(ctx, m.ID+1_000_000, time.Minute)
	require.NoError(t, err)
	require.False(t, missing)
}
