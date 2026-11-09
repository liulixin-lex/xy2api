//go:build unit

package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBudgetTraceRecordsOnlyActualDeadlineConstraints(t *testing.T) {
	ctx, trace := WithBudgetTraceForTest(context.Background())
	start := time.Now()
	cfg := Defaults()
	b := NewBudget(ctx, &cfg, KindText, start)
	st := NewRequestState()
	st.SetBudget(b)
	st.SetAttemptProfile(AttemptProfile{AccountID: 7, AttemptStart: start, Warm: true})
	// A configuration cap alone is not t*. Neither is an allowed attempt.
	require.Equal(t, 60*time.Second, b.ClampWait(120*time.Second, start))
	require.True(t, b.CanAttempt(start))
	require.Len(t, trace.Events(), 1)
	at := start.Add(230 * time.Second)
	require.Equal(t, 8*time.Second, b.ClampWait(120*time.Second, at))
	require.False(t, b.CanAttempt(start.Add(239*time.Second)))
	require.False(t, b.ConsumeInPlaceRetry(9, false, start.Add(239*time.Second)))
	events := trace.Events()
	require.Len(t, events, 4)
	require.Equal(t, "wait_clamped", events[1].Kind)
	require.Equal(t, 230*time.Second, events[1].DecisionOffset)
	require.Equal(t, at, events[1].DecisionAt)
	require.Less(t, events[1].Offset, time.Second, "event time is sampled inside the function, separate from its now argument")
	require.Equal(t, 120*time.Second, events[1].RawInput)
	require.Equal(t, 60*time.Second, events[1].Input)
	require.Equal(t, 8*time.Second, events[1].Output)
	require.Equal(t, "CanAttempt", events[2].Site)
	require.EqualValues(t, 7, events[2].AccountID)
	require.Equal(t, "ConsumeInPlaceRetry", events[3].Site)
	require.EqualValues(t, 9, events[3].AccountID)
	require.Zero(t, b.Attempts())
	// Earlier cancellation/attempt-limit branches must not claim deadline t*.
	b.RecordAttemptAt(start)
	b.maxAttempts = 1
	require.False(t, b.CanAttempt(start.Add(241*time.Second)))
	require.False(t, b.ConsumeInPlaceRetry(9, false, start.Add(241*time.Second)))
	require.Len(t, trace.Events(), 4)
	require.NotContains(t, *st.TimingJSON(), "wait_clamped")
	require.NotContains(t, *st.TimingJSON(), "budget_base_s")
}

func TestBudgetTracePlanFreezesProfileAndSeparatesStages(t *testing.T) {
	ctx, trace := WithBudgetTraceForTest(context.Background())
	start := time.Now()
	cfg := Defaults()
	st := NewRequestState()
	st.SetBudget(NewBudget(ctx, &cfg, KindText, start))
	initial := AttemptProfile{AccountID: 7, Model: "old", AttemptStart: start, Warm: true, Stream: true, BodyBytes: 100}
	st.SetAttemptProfile(initial)
	first := st.PlanAttemptTimeout(30*time.Second, false, start)
	st.NoteKeepaliveCommit(start.Add(time.Second))
	st.SetAttemptProfile(AttemptProfile{AccountID: 8, Model: "new", AttemptStart: start.Add(time.Second)})
	second := st.PlanAttemptTimeout(240*time.Second, false, start.Add(time.Second))
	first.ObserveFiredForTest("first")
	second.ObserveFiredForTest("second")
	events := trace.Events()
	a, b := events[len(events)-2], events[len(events)-1]
	require.Equal(t, initial, a.Plan.Profile)
	require.EqualValues(t, 7, a.AccountID)
	require.False(t, a.Plan.CommittedAtStart)
	require.False(t, a.DeadlineLimited)
	require.EqualValues(t, 8, b.AccountID)
	require.True(t, b.Plan.CommittedAtStart)
	require.True(t, b.DeadlineLimited)
	require.NotEqual(t, a.Plan.Sequence, b.Plan.Sequence)
	require.Equal(t, 239*time.Second, b.Plan.Effective)
	require.Equal(t, a.At.Sub(start), a.Offset)
	// Returned records are copies, including nested plan/profile values.
	events[0].Kind = "corrupt"
	require.NotEqual(t, "corrupt", trace.Events()[0].Kind)
}
