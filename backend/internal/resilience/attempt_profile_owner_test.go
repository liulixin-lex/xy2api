//go:build unit

package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAttemptTimerOwnerSurvivesSendAndWarmUpdates(t *testing.T) {
	for _, responseStage := range []bool{false, true} {
		t0 := time.Unix(1000, 0)
		owner := t0.Add(239 * time.Second)
		st := NewRequestState()
		st.SetBudget(NewBudget(context.Background(), timingTestSettings(), KindText, t0))
		profile := AttemptProfile{AccountID: 7, AttemptStart: owner, TimerOwnerStart: owner, Warm: true}
		st.SetAttemptProfile(profile)
		var plan TimeoutPlan
		if !responseStage {
			plan = st.PlanAttemptTimeout(30*time.Second, false, owner)
		}
		profile.AttemptStart = owner.Add(100 * time.Millisecond)
		st.SetAttemptProfile(profile)
		profile = st.AttemptProfile()
		profile.Warm = false
		st.SetAttemptProfile(profile)
		if responseStage {
			st.ResponseStagePlan(profile.AttemptStart)
		} else {
			require.Equal(t, owner, plan.Start(), "send-time update never rewrites the timer plan")
		}
		st.NoteSemanticOutput(profile.AttemptStart.Add(time.Millisecond))
		require.EqualValues(t, 1, *st.AttemptFirstOutputMs())
		timing := st.Timing()
		require.EqualValues(t, 1, *timing.LateAttemptCount)
		require.Zero(t, *timing.LateAttemptNoTimerCount)
		// A new dispatch on the same account has a new owner and counts once.
		profile.TimerOwnerStart = owner.Add(200 * time.Millisecond)
		profile.AttemptStart = profile.TimerOwnerStart
		st.SetAttemptProfile(profile)
		timing = st.Timing()
		require.EqualValues(t, 2, *timing.LateAttemptCount)
		require.EqualValues(t, 1, *timing.LateAttemptNoTimerCount)
	}
}
