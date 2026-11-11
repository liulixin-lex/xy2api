//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

type selectionSlotsFake struct {
	attempts []int64
	failed   map[int64]bool
	held     int
	events   *[]string
	after    func(int64)
}

func (f *selectionSlotsFake) tryAcquireAccountSlot(_ context.Context, id int64, _ int) (*AcquireResult, error) {
	f.attempts = append(f.attempts, id)
	*f.events = append(*f.events, fmt.Sprintf("slot:%d", id))
	if f.after != nil {
		f.after(id)
	}
	if f.failed[id] {
		return &AcquireResult{}, nil
	}
	f.held++
	return &AcquireResult{Acquired: true, ReleaseFunc: func() { f.held--; *f.events = append(*f.events, "release") }}, nil
}

type selectionBindingFake struct {
	owner     int64
	remaining time.Duration
	reads     int
	events    *[]string
	casFail   bool
	casError  bool
}

func (f *selectionBindingFake) GetSessionAccountIDWithTTL(context.Context, int64, string) (int64, time.Duration, error) {
	f.reads++
	return f.owner, f.remaining, nil
}
func (f *selectionBindingFake) CompareAndSwapSessionAccountID(_ context.Context, _ int64, _ string, old, target int64, _ time.Duration) (bool, error) {
	*f.events = append(*f.events, fmt.Sprintf("cas:%d", target))
	if f.casError {
		return false, errors.New("cache down")
	}
	if f.casFail || f.owner != old {
		return false, nil
	}
	f.owner = target
	return true, nil
}

type selectionSessionFixture struct {
	session    *selectionSession
	view       *fakeSchedView
	slots      *selectionSlotsFake
	binding    *selectionBindingFake
	owner      *Account
	candidates []*Account
	now        time.Time
	events     []string
}

func selectionNewSessionFixture() *selectionSessionFixture {
	f := &selectionSessionFixture{now: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC), owner: selectionTestAccount(10, 5), candidates: []*Account{selectionTestAccount(1, 1), selectionTestAccount(2, 2), selectionTestAccount(3, 3)}}
	f.view = selectionTestView(f.now)
	f.slots = &selectionSlotsFake{failed: map[int64]bool{}, events: &f.events}
	f.binding = &selectionBindingFake{owner: 10, remaining: 29 * time.Minute, events: &f.events}
	f.session = &selectionSession{view: func() schedquality.View { return f.view }, now: func() time.Time { return f.now }, slots: f.slots, cache: f.binding, bindings: f.binding, marks: &fullMarks{}, bindingTTL: 30 * time.Minute, recheck: func(_ context.Context, a *Account) *Account {
		f.events = append(f.events, fmt.Sprintf("check:%d", a.ID))
		return a
	}}
	return f
}
func (f *selectionSessionFixture) selectNow(ctx context.Context) *AccountSelectionResult {
	return f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{TokenBilled: true})
}
func (f *selectionSessionFixture) state(id int64, st schedquality.StateView) {
	f.view.States[fakeSchedViewKey{7, id, "m"}] = st
}

func TestOpenAISelectionSessionTakeoverRules(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*selectionSessionFixture)
		want  bool
	}{
		{"immediate", func(*selectionSessionFixture) {}, true},
		{"idle_too_short", func(f *selectionSessionFixture) {
			f.view.Takeovers[7] = fakeSchedTakeover{Idle: 5 * time.Minute, Enabled: true}
		}, false},
		{"idle_reached", func(f *selectionSessionFixture) {
			f.view.Takeovers[7] = fakeSchedTakeover{Idle: time.Minute, Enabled: true}
		}, true},
		{"group_disabled", func(f *selectionSessionFixture) { f.view.Takeovers[7] = fakeSchedTakeover{} }, false},
		{"global_disabled", func(f *selectionSessionFixture) { f.view.Enabled = false }, false},
		{"stale_even_if_exempt", func(f *selectionSessionFixture) {
			f.view.ViewAt = f.now.Add(-61 * time.Second)
			f.state(1, schedquality.StateView{AllOverride: schedquality.OverrideRef{Mode: schedquality.OverrideExempt}})
		}, false},
		{"exact_view_max_age", func(f *selectionSessionFixture) { f.view.ViewAt = f.now.Add(-60 * time.Second) }, true},
		{"manual_back", func(f *selectionSessionFixture) {
			f.state(1, schedquality.StateView{GroupOverride: schedquality.OverrideRef{Mode: schedquality.OverrideDemote}})
		}, false},
		{"exempt_cooling", func(f *selectionSessionFixture) {
			f.state(1, schedquality.StateView{State: schedquality.StateCooling, AllOverride: schedquality.OverrideRef{Mode: schedquality.OverrideExempt}})
		}, true},
		{"verified_level_four", func(f *selectionSessionFixture) {
			f.state(1, schedquality.StateView{State: schedquality.StateNormal, Level: 4, Since: f.now})
		}, true},
		{"unverified_five_minutes", func(f *selectionSessionFixture) {
			f.state(1, schedquality.StateView{Reason: schedquality.ReasonUnverified, Since: f.now.Add(-5 * time.Minute)})
		}, false},
		{"unverified_thirty_minutes", func(f *selectionSessionFixture) {
			f.state(1, schedquality.StateView{Reason: schedquality.ReasonUnverified, Since: f.now.Add(-30 * time.Minute)})
		}, true},
		{"unverified_exempt", func(f *selectionSessionFixture) {
			f.state(1, schedquality.StateView{Reason: schedquality.ReasonUnverified, Since: f.now, AllOverride: schedquality.OverrideRef{Mode: schedquality.OverrideExempt}})
		}, true},
		{"recently_full", func(f *selectionSessionFixture) { f.session.marks.Mark(1, f.now.Add(-59*time.Second)) }, false},
		{"full_expired", func(f *selectionSessionFixture) { f.session.marks.Mark(1, f.now.Add(-60*time.Second)) }, true},
		{"same_priority", func(f *selectionSessionFixture) { f.candidates[0].Priority = 5 }, false},
		{"lower_priority", func(f *selectionSessionFixture) { f.candidates[0].Priority = 6 }, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := selectionNewSessionFixture()
			f.candidates = f.candidates[:1]
			tt.setup(f)
			result := f.selectNow(context.Background())
			if tt.want {
				require.NotNil(t, result)
				require.Equal(t, int64(1), f.binding.owner)
				require.Equal(t, []string{"check:1", "slot:1", "check:1", "check:10", "cas:1"}, f.events)
				result.ReleaseFunc()
			} else {
				require.Nil(t, result)
				require.Equal(t, int64(10), f.binding.owner)
			}
			require.Zero(t, f.slots.held)
			if f.view.Takeovers[7].Idle == 0 {
				require.Zero(t, f.binding.reads)
			}
		})
	}
	for _, ctx := range []context.Context{withOpenAIStickyMigrationGuard(context.Background(), "resp_1", OpenAIUpstreamTransportHTTPSSE), withOpenAIStickyMigrationGuard(context.Background(), "", OpenAIUpstreamTransportResponsesWebsocket), withOpenAINoStickyMigration(context.Background()), withGroupQualityGateExempt(context.Background())} {
		f := selectionNewSessionFixture()
		require.Nil(t, f.selectNow(ctx))
		require.Empty(t, f.slots.attempts)
	}
}

func TestOpenAISelectionSessionTakeoverTwoCandidates(t *testing.T) {
	for _, both := range []bool{false, true} {
		t.Run(fmt.Sprint(both), func(t *testing.T) {
			f := selectionNewSessionFixture()
			f.slots.failed[1] = true
			f.slots.failed[2] = both
			result := f.selectNow(context.Background())
			require.Equal(t, []int64{1, 2}, f.slots.attempts)
			require.True(t, f.session.marks.Recently(1, f.now))
			if both {
				require.Nil(t, result)
				require.Equal(t, int64(10), f.binding.owner)
			} else {
				require.Equal(t, int64(2), f.binding.owner)
				result.ReleaseFunc()
			}
			require.Zero(t, f.slots.held)
		})
	}
}

func TestOpenAISelectionSessionRechecksEveryTakeoverCondition(t *testing.T) {
	cases := []struct {
		name   string
		change func(*selectionSessionFixture)
	}{
		{"unverified", func(f *selectionSessionFixture) {
			f.state(1, schedquality.StateView{Reason: schedquality.ReasonUnverified, Since: f.now})
		}},
		{"back", func(f *selectionSessionFixture) { f.state(1, schedquality.StateView{State: schedquality.StateCooling}) }},
		{"disabled", func(f *selectionSessionFixture) { f.view.Enabled = false }},
		{"stale", func(f *selectionSessionFixture) { f.view.ViewAt = f.now.Add(-61 * time.Second) }},
		{"priority", func(f *selectionSessionFixture) { f.candidates[0].Priority = 5 }},
		{"owner_priority", func(f *selectionSessionFixture) { f.owner.Priority = 1 }},
		{"new_full_mark", func(f *selectionSessionFixture) { f.session.marks.Mark(1, f.now) }},
		{"group_off", func(f *selectionSessionFixture) { f.view.Takeovers[7] = fakeSchedTakeover{} }},
		{"admission", func(f *selectionSessionFixture) {
			f.session.recheck = func(context.Context, *Account) *Account { return nil }
		}},
		{"cas_conflict", func(f *selectionSessionFixture) { f.binding.casFail = true }},
		{"cas_error", func(f *selectionSessionFixture) { f.binding.casError = true }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := selectionNewSessionFixture()
			f.candidates = f.candidates[:1]
			f.slots.after = func(int64) { tt.change(f) }
			require.Nil(t, f.selectNow(context.Background()))
			require.Equal(t, int64(10), f.binding.owner)
			require.Zero(t, f.slots.held)
			require.Contains(t, f.events, "release")
		})
	}
}

func TestOpenAISelectionSessionMigration(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*selectionSessionFixture)
		want  bool
	}{
		{"back_owner", func(*selectionSessionFixture) {}, true},
		{"unverified_target", func(f *selectionSessionFixture) {
			f.state(1, schedquality.StateView{Reason: schedquality.ReasonUnverified, Since: f.now})
		}, true},
		{"no_target", func(f *selectionSessionFixture) { f.state(1, schedquality.StateView{State: schedquality.StateCooling}) }, false},
		{"slot_full", func(f *selectionSessionFixture) { f.slots.failed[1] = true }, false},
		{"target_became_back", func(f *selectionSessionFixture) {
			f.slots.after = func(int64) { f.state(1, schedquality.StateView{State: schedquality.StateCooling}) }
		}, false},
		{"cas_conflict", func(f *selectionSessionFixture) { f.binding.casFail = true }, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := selectionNewSessionFixture()
			f.candidates = f.candidates[:1]
			f.state(10, schedquality.StateView{State: schedquality.StateCooling})
			tt.setup(f)
			result := f.selectNow(context.Background())
			if tt.want {
				require.NotNil(t, result)
				require.Equal(t, int64(1), f.binding.owner)
				require.Equal(t, []string{"check:1", "slot:1", "check:1", "cas:1"}, f.events)
				result.ReleaseFunc()
			} else {
				require.Nil(t, result)
				require.Equal(t, int64(10), f.binding.owner)
			}
			require.Zero(t, f.slots.held)
		})
	}
}

func TestOpenAISelectionSessionLatestViewFailureTriesSecond(t *testing.T) {
	f := selectionNewSessionFixture()
	f.slots.after = func(id int64) {
		if id == 1 {
			f.state(1, schedquality.StateView{Reason: schedquality.ReasonUnverified, Since: f.now})
		}
	}
	result := f.selectNow(context.Background())
	require.NotNil(t, result)
	require.Equal(t, []int64{1, 2}, f.slots.attempts)
	require.Equal(t, int64(2), f.binding.owner)
	require.NotContains(t, f.events, "cas:1")
	require.Equal(t, 1, f.slots.held)
	result.ReleaseFunc()
	require.Zero(t, f.slots.held)
}
