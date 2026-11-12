//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

var lastResortAvoidCases = []struct {
	name      string
	avoid     bool
	only      bool
	busyOther bool
	busyBoth  bool
	want      int64
}{
	{name: "avoided_healthy_before_cooled", avoid: true, want: lrAccountB},
	{name: "only_avoided_remains", avoid: true, only: true, want: lrAccountA},
	{name: "no_avoid_keeps_priority_order", want: lrAccountA},
	{name: "avoided_free_when_other_busy", avoid: true, busyOther: true, want: lrAccountA},
	{name: "all_busy_waits_on_unavoided", avoid: true, busyBoth: true, want: lrAccountB},
}

func TestResilienceGatewayLastResortPrefersUnavoided(t *testing.T) {
	for _, scheduler := range lastResortSchedulers {
		for _, tc := range lastResortAvoidCases {
			t.Run(string(scheduler)+"/"+tc.name, func(t *testing.T) {
				env := newGatewayLastResortEnv(t, 1, 5)
				if tc.avoid {
					a := env.repo.accountsByID[lrAccountA]
					a.TempUnschedulableUntil, a.TempUnschedulableReason = nil, ""
					r := env.gateway.resilience
					require.NoError(t, r.store.AddSessionAvoid(t.Context(), selectionTestGroupID, lrSession, lrAccountA, time.Minute))
					r.avoidingSessions.Store(sessionMarkKey(selectionTestGroupID, lrSession), time.Now().Add(time.Minute))
				}
				if tc.only {
					env.repo.accountsByID[lrAccountB].Schedulable = false
				}
				if tc.busyOther || tc.busyBoth {
					env.conc.busy[lrAccountB] = -1
				}
				if tc.busyBoth {
					env.conc.busy[lrAccountA] = -1
				}
				sel, _ := selectGatewayLastResort(t, env)
				require.Equal(t, tc.want, sel.Account.ID)
				require.Equal(t, !tc.busyBoth, sel.Acquired)
			})
		}
	}
}

func TestResilienceGeminiLastResortPrefersUnavoided(t *testing.T) {
	for _, scheduler := range lastResortSchedulers {
		t.Run(string(scheduler), func(t *testing.T) {
			env := newGatewayLastResortEnv(t, 1, 5)
			for _, account := range env.repo.accountsByID {
				account.Platform = PlatformGemini
			}
			env.gateway.groupRepo = &mockGroupRepoForGateway{groups: map[int64]*Group{
				selectionTestGroupID: {ID: selectionTestGroupID, Platform: PlatformGemini, Status: StatusActive, Hydrated: true},
			}}
			a := env.repo.accountsByID[lrAccountA]
			a.TempUnschedulableUntil, a.TempUnschedulableReason = nil, ""
			r := env.gateway.resilience
			require.NoError(t, r.store.AddSessionAvoid(t.Context(), selectionTestGroupID, lrSession, lrAccountA, time.Minute))
			r.avoidingSessions.Store(sessionMarkKey(selectionTestGroupID, lrSession), time.Now().Add(time.Minute))
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			gid := selectionTestGroupID
			sel, err := env.gateway.SelectAccountWithLoadAwareness(ctx, &gid, lrSession, "gemini-2.5-pro", nil, "", 0)
			require.NoError(t, err)
			require.NotNil(t, sel)
			require.NotNil(t, sel.Account)
			if sel.ReleaseFunc != nil {
				t.Cleanup(sel.ReleaseFunc)
			}
			require.Equal(t, lrAccountB, sel.Account.ID)
			require.Equal(t, lrAccountB, st.LastResort())
		})
	}
}

func TestResilienceOpenAILastResortPrefersUnavoided(t *testing.T) {
	for _, scheduler := range lastResortSchedulers {
		for _, tc := range lastResortAvoidCases {
			t.Run(string(scheduler)+"/"+tc.name, func(t *testing.T) {
				env := newOpenAILastResortEnv(t, 1, 5)
				if tc.avoid {
					a := env.account(lrAccountA)
					a.TempUnschedulableUntil, a.TempUnschedulableReason = nil, ""
					r := env.svc.resilience
					require.NoError(t, r.store.AddSessionAvoid(t.Context(), selectionTestGroupID, lrSession, lrAccountA, time.Minute))
					r.avoidingSessions.Store(sessionMarkKey(selectionTestGroupID, lrSession), time.Now().Add(time.Minute))
				}
				if tc.only {
					env.account(lrAccountB).Schedulable = false
				}
				if tc.busyOther || tc.busyBoth {
					env.conc.busy[lrAccountB] = -1
				}
				if tc.busyBoth {
					env.conc.busy[lrAccountA] = -1
				}
				sel := selectOpenAILastResort(t, env)
				require.Equal(t, tc.want, sel.Account.ID)
				require.Equal(t, !tc.busyBoth, sel.Acquired)
			})
		}
	}
}

func TestResilienceOpenAILastResortAvoidOutranksStanding(t *testing.T) {
	env := newOpenAILastResortEnv(t, 1, 5)
	a := env.account(lrAccountA)
	a.TempUnschedulableUntil, a.TempUnschedulableReason = nil, ""
	r := env.svc.resilience
	require.NoError(t, r.store.AddSessionAvoid(t.Context(), selectionTestGroupID, lrSession, lrAccountA, time.Minute))
	r.avoidingSessions.Store(sessionMarkKey(selectionTestGroupID, lrSession), time.Now().Add(time.Minute))
	view := selectionTestView(time.Now())
	view.States[fakeSchedViewKey{selectionTestGroupID, lrAccountB, "gpt-5.1"}] = schedquality.StateView{State: schedquality.StateCooling}
	NewOpenAISelectionBoard(env.svc, func() schedquality.View { return view })
	require.Equal(t, lrAccountB, selectOpenAILastResort(t, env).Account.ID)
}
