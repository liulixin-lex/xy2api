//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestSelectionGroupGateAndManualOnly(t *testing.T) {
	for _, mode := range []string{"enabled", "group_disabled", "global_disabled", "stale", "manual_only", "exempt"} {
		t.Run(mode, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
			st := schedquality.StateView{Failing: true}
			v := &selectionPolicyView{View: f.view, disabled: mode == "group_disabled"}
			f.svc.selectionBoard.view = func() schedquality.View { return v }
			if mode == "global_disabled" {
				f.view.Enabled = false
			}
			if mode == "stale" {
				f.view.ViewAt = f.view.ViewAt.Add(-schedquality.ViewMaxAge - time.Second)
			}
			if mode == "exempt" {
				st.GroupOverride.Mode = schedquality.OverrideExempt
			}
			f.view.States[fakeSchedViewKey{7, 1, "m"}] = st
			ctx := context.Background()
			if mode == "manual_only" {
				ctx = withOpenAISelectionManualOnly(ctx)
			}
			want := int64(1)
			if mode == "enabled" {
				want = 2
			}
			require.Equal(t, want, f.selectNow(t, ctx, "", nil).Account.ID)
		})
	}
}

func TestSelectionMigrationTriesEveryAvailableNormalTarget(t *testing.T) {
	f := selectionNewSessionFixture()
	f.state(f.owner.ID, schedquality.StateView{Failing: true})
	f.slots.failed[1], f.slots.failed[2] = true, true
	selected := f.selectNow(context.Background())
	require.NotNil(t, selected)
	defer selected.ReleaseFunc()
	require.Equal(t, int64(3), selected.Account.ID)
	require.Equal(t, []int64{1, 2, 3}, f.slots.attempts, "the two-candidate limit applies only to takeover")
}

func TestSelectionTakeoverIdleChoices(t *testing.T) {
	idle, enabled := (&schedQualityView{}).TakeoverIdle(7)
	require.True(t, enabled)
	require.Equal(t, 900*time.Second, idle, "an absent group policy has the frozen 15-minute option")
	for _, seconds := range schedquality.TakeoverIdleChoices {
		for _, elapsed := range []time.Duration{0, 299 * time.Second, 300 * time.Second, 899 * time.Second, 900 * time.Second, 1799 * time.Second, 1800 * time.Second} {
			t.Run(fmt.Sprintf("option=%d/idle=%s", seconds, elapsed), func(t *testing.T) {
				f := selectionNewSessionFixture()
				f.candidates = f.candidates[:1]
				f.session.bindingTTL = 2 * time.Hour
				f.binding.remaining = f.session.bindingTTL - elapsed
				f.view.Takeovers[7] = fakeSchedTakeover{Idle: time.Duration(seconds) * time.Second, Enabled: seconds >= 0}
				selected := f.selectNow(context.Background())
				if seconds >= 0 && elapsed >= time.Duration(seconds)*time.Second {
					require.NotNil(t, selected)
					selected.ReleaseFunc()
				} else {
					require.Nil(t, selected)
					require.Equal(t, int64(10), f.binding.owner)
				}
			})
		}
	}
}
