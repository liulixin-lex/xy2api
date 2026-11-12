//go:build unit

package service

import (
	"bytes"
	"container/list"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func selectionTestAccount(id int64, priority int) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: priority, Concurrency: 5}
}

func selectionTestView(now time.Time) *fakeSchedView {
	return &fakeSchedView{ViewAt: now, Enabled: true, States: map[fakeSchedViewKey]schedquality.StateView{}, Takeovers: map[int64]fakeSchedTakeover{7: {Enabled: true}}}
}

func TestOpenAISelectionComparator(t *testing.T) {
	tests := []struct {
		name string
		a, b selectionKey
	}{
		{"standing", selectionKey{Standing: 0, Priority: 2}, selectionKey{Standing: 1, Priority: 1}},
		{"priority", selectionKey{Priority: 1, Rate: 100, LoadRate: 100}, selectionKey{Priority: 2, Rate: 0, LoadRate: 0}},
		{"rate", selectionKey{RateKnown: true, Rate: 1, Affinity: 1, LoadRate: 100}, selectionKey{RateKnown: true, Rate: 2}},
		{"known_before_unknown_rate", selectionKey{RateKnown: true, Rate: 100, Affinity: 1, LoadRate: 100}, selectionKey{}},
		{"unknown_rates_use_affinity", selectionKey{Affinity: 0, ID: 2}, selectionKey{Affinity: 1, ID: 1}},
		{"affinity", selectionKey{Affinity: 0, LoadRate: 100}, selectionKey{Affinity: 1}},
		{"load", selectionKey{LoadRate: 1, LastUsed: 100}, selectionKey{LoadRate: 2}},
		{"last_used", selectionKey{LastUsed: 0, ID: 2}, selectionKey{LastUsed: 1, ID: 1}},
		{"id", selectionKey{ID: 1}, selectionKey{ID: 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.True(t, selectionLess(tt.a, tt.b))
			require.False(t, selectionLess(tt.b, tt.a))
			require.False(t, selectionLess(tt.a, tt.a))
		})
	}
}

func TestOpenAISelectionStanding(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		state   schedquality.StateView
		enabled bool
		age     time.Duration
		want    schedquality.Standing
	}{
		{"normal", schedquality.StateView{}, true, 0, schedquality.StandFront},
		{"cooling", schedquality.StateView{State: schedquality.StateCooling}, true, 0, schedquality.StandBack},
		{"disabled", schedquality.StateView{State: schedquality.StateCooling}, false, 0, schedquality.StandFront},
		{"stale_cooling", schedquality.StateView{State: schedquality.StateCooling}, true, 61 * time.Second, schedquality.StandFront},
		{"exactly_60_seconds", schedquality.StateView{State: schedquality.StateCooling}, true, 60 * time.Second, schedquality.StandBack},
		{"stale_demote", schedquality.StateView{GroupOverride: schedquality.OverrideRef{Mode: schedquality.OverrideDemote}}, true, 61 * time.Second, schedquality.StandBack},
		{"disabled_demote", schedquality.StateView{GroupOverride: schedquality.OverrideRef{Mode: schedquality.OverrideDemote}}, false, 0, schedquality.StandBack},
		{"exempt_cooling", schedquality.StateView{State: schedquality.StateCooling, AllOverride: schedquality.OverrideRef{Mode: schedquality.OverrideExempt}}, true, 0, schedquality.StandFront},
		{"group_demote_over_all_exempt", schedquality.StateView{GroupOverride: schedquality.OverrideRef{Mode: schedquality.OverrideDemote}, AllOverride: schedquality.OverrideRef{Mode: schedquality.OverrideExempt}}, true, 0, schedquality.StandBack},
		{"expired_group_falls_back_to_all", schedquality.StateView{State: schedquality.StateCooling, GroupOverride: schedquality.OverrideRef{Mode: schedquality.OverrideDemote, Expires: now}, AllOverride: schedquality.OverrideRef{Mode: schedquality.OverrideExempt}}, true, 0, schedquality.StandFront},
		{"expired_without_view_refresh", schedquality.StateView{GroupOverride: schedquality.OverrideRef{Mode: schedquality.OverrideDemote, Expires: now}}, true, 0, schedquality.StandFront},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := selectionTestView(now.Add(-tt.age))
			view.Enabled = tt.enabled
			account := selectionTestAccount(1, 1)
			account.Credentials = map[string]any{"model_mapping": map[string]any{"alias": "outbound"}}
			view.States[fakeSchedViewKey{7, 1, "outbound"}] = tt.state
			key := buildSelectionKey(view, 7, "alias", account, selectionRequest{}, now)
			require.Equal(t, int(tt.want), key.Standing)
		})
	}
	t.Run("override_in_other_group", func(t *testing.T) {
		view := selectionTestView(now)
		view.States[fakeSchedViewKey{8, 1, "m"}] = schedquality.StateView{AllOverride: schedquality.OverrideRef{Mode: schedquality.OverrideExempt}, State: schedquality.StateCooling}
		require.Equal(t, 0, buildSelectionKey(view, 8, "m", selectionTestAccount(1, 1), selectionRequest{}, now).Standing)
	})
}

func TestOpenAISelectionKeyInputs(t *testing.T) {
	now := time.Now()
	view := selectionTestView(now)
	a, b := selectionTestAccount(1, 1), selectionTestAccount(2, 1)
	ra, rb := 2.0, 1.0
	a.RateMultiplier = &ra
	b.RateMultiplier = &rb
	recent := &openAIRecentSessionSuccess{entries: make(map[openAIRecentSessionKey]*list.Element)}
	recent.record(openAIRecentSessionKey{7, "session", "m"}, a.ID, now)
	req := selectionRequest{SessionHash: "session", Recent: recent}
	ak, bk := buildSelectionKey(view, 7, "m", a, req, now), buildSelectionKey(view, 7, "m", b, req, now)
	require.Equal(t, 0, ak.LoadRate)
	require.Equal(t, 0, bk.LoadRate)
	require.Zero(t, ak.Rate)
	require.True(t, selectionLess(ak, bk))
	req.TokenBilled = true
	require.True(t, selectionLess(buildSelectionKey(view, 7, "m", b, req, now), buildSelectionKey(view, 7, "m", a, req, now)))
	req.OAuthRateMultiplier = &ra
	a.Type = AccountTypeOAuth
	require.Equal(t, ra, buildSelectionKey(view, 7, "m", a, req, now).Rate)
}

func TestOpenAISelectionUnknownRate(t *testing.T) {
	now := time.Now()
	view := selectionTestView(now)
	for _, tt := range []struct {
		name  string
		setup func(*Account)
	}{
		{"unsupported_type", func(a *Account) { a.Type = "unsupported" }},
		{"nan", func(a *Account) { rate := math.NaN(); a.RateMultiplier = &rate }},
		{"infinite", func(a *Account) { rate := math.Inf(1); a.RateMultiplier = &rate }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			unknown, known := selectionTestAccount(1, 1), selectionTestAccount(2, 1)
			tt.setup(unknown)
			req := selectionRequest{TokenBilled: true}
			uk, kk := buildSelectionKey(view, 7, "m", unknown, req, now), buildSelectionKey(view, 7, "m", known, req, now)
			require.False(t, uk.RateKnown)
			require.Zero(t, uk.Rate, "unknown rates must stay JSON-safe for the board")
			require.True(t, kk.RateKnown)
			require.True(t, selectionLess(kk, uk))
			require.False(t, selectionLess(uk, kk))
			req.TokenBilled = false
			uk, kk = buildSelectionKey(view, 7, "m", unknown, req, now), buildSelectionKey(view, 7, "m", known, req, now)
			require.Equal(t, kk.RateKnown, uk.RateKnown)
			require.Zero(t, kk.Rate)
			require.Zero(t, uk.Rate)
			require.True(t, selectionLess(uk, kk), "non-token requests ignore rates and fall through to ID")
		})
	}
}

func TestOpenAISelectionFullMarks(t *testing.T) {
	now := time.Now()
	marks := &fullMarks{}
	a := selectionTestAccount(1, 1)
	factor := 100
	a.LoadFactor = &factor
	marks.observe([]*Account{a}, map[int64]*AccountLoadInfo{1: {CurrentConcurrency: 5, LoadRate: 5}}, now)
	require.True(t, marks.Recently(1, now.Add(59*time.Second)))
	require.False(t, marks.Recently(1, now.Add(60*time.Second)))
	marks.Mark(1, now.Add(-time.Second))
	require.Equal(t, now, marks.last(1))
	a.Concurrency = 10
	marks.observe([]*Account{a}, map[int64]*AccountLoadInfo{1: {CurrentConcurrency: 5, LoadRate: 100}}, now.Add(time.Second))
	require.Equal(t, now, marks.last(1))
}

func TestOpenAISelectionStaleViewWarningRateLimited(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	selectionStaleViewWarning.Lock()
	saved := selectionStaleViewWarning.at
	selectionStaleViewWarning.at = time.Time{}
	selectionStaleViewWarning.Unlock()
	defer func() {
		selectionStaleViewWarning.Lock()
		selectionStaleViewWarning.at = saved
		selectionStaleViewWarning.Unlock()
	}()
	now := time.Now()
	view := selectionTestView(now.Add(-schedquality.ViewMaxAge - time.Second))
	require.False(t, selectionAuto(view, now))
	require.False(t, selectionAuto(view, now.Add(time.Second)))
	require.Equal(t, 1, strings.Count(logs.String(), "sched_quality.view_stale"))
	require.False(t, selectionAuto(view, now.Add(schedquality.ViewMaxAge)))
	require.Equal(t, 2, strings.Count(logs.String(), "sched_quality.view_stale"))
	view.ViewAt = now
	require.True(t, selectionAuto(view, now), "refresh restores automatic state")
}
