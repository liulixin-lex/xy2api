//go:build unit

package resilience

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestRequestTimingObservationsDoNotChangeBudget(t *testing.T) {
	now := time.Now()
	cfg := Defaults()
	b := NewBudget(WithClientFamily(context.Background(), "codex/1"), &cfg, KindText, now)
	st := NewRequestState()
	st.SetBudget(b)
	st.InitDelivery(context.Background(), now.Add(time.Millisecond), nil)
	st.MarkFirstBodyRead(now.Add(2 * time.Second))
	st.MarkBodyReceived(now.Add(101*time.Second), "body_eof", 12<<20)
	st.MarkBodyReceived(now.Add(102*time.Second), "handler_read", 7)
	st.NoteKeepaliveCommit(now.Add(15 * time.Second))
	st.NoteWireStatus(200)
	st.SetEdge("esa")
	deadline := b.Deadline()
	st.NoteOpenAIDispatch(now.Add(200*time.Second), "direct")
	st.NoteOpenAIDispatch(now.Add(241*time.Second), "plugin")
	start := now.Add(239 * time.Second)
	st.SetAttemptProfile(AttemptProfile{AttemptStart: start})
	st.SetAttemptProfile(AttemptProfile{AttemptStart: start, Warm: true})
	st.NoteAttemptTimer(start)
	timing := st.Timing()
	require.EqualValues(t, 12<<20, *timing.BodyBytes)
	require.EqualValues(t, 2000, *timing.FirstReadMs)
	require.EqualValues(t, 101000, *timing.BodyEOFMs)
	require.Equal(t, "arrival", *timing.AnchorBasis)
	require.True(t, *timing.AnchorEligible)
	require.EqualValues(t, 1, *timing.BodyReceiptRepeat)
	require.EqualValues(t, 200000, *timing.PreDispatchMs)
	require.EqualValues(t, 15000, *timing.KeepaliveCommitMs)
	require.EqualValues(t, 200, *timing.WireStatus)
	require.EqualValues(t, 2, *timing.DispatchCount)
	require.EqualValues(t, 0, *timing.MinDispatchRemainingMs)
	require.EqualValues(t, 240000, *timing.FinalDeadlineMs)
	require.EqualValues(t, 1, *timing.LateAttemptCount, "hidden replay can bypass arrival admission")
	require.EqualValues(t, 0, *timing.LateAttemptNoTimerCount)
	require.Equal(t, "arrival", *timing.BudgetStartMode)
	require.Equal(t, now.UnixMilli(), *timing.BudgetT0UnixMs)
	require.Equal(t, "codex", *timing.ClientFamily)
	require.Equal(t, "esa", *timing.CDNObserved)
	require.Nil(t, timing.ClientCancelMs)
	require.Equal(t, deadline, b.Deadline())
	require.Zero(t, b.Attempts())
	st.NoteResponseStage(now.Add(10*time.Second), 20*time.Second)
	var ops map[string]any
	require.NoError(t, json.Unmarshal([]byte(*st.TimingJSON()), &ops))
	require.Contains(t, ops, "response_stage")
	raw, err := json.Marshal(timing)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "response_stage")
	st.FinishTiming()
	st.FinishTiming()
}

func TestDispatchHistogramAndNoBudgetExclusions(t *testing.T) {
	now := time.Now()
	dispatchHist.Lock()
	dispatchHist.buckets = nil
	dispatchHist.Unlock()
	st := NewRequestState()
	st.NoteOpenAIDispatch(now, "direct")
	st.SetBudget(NewBudget(context.Background(), timingTestSettings(), KindImage, now))
	st.NoteOpenAIDispatch(now, "direct")
	require.Empty(t, DispatchHistogramSnapshot(time.Minute))
	b := NewBudget(context.Background(), timingTestSettings(), KindText, now)
	st.SetBudget(b)
	st.NoteOpenAIDispatch(now.Add(210*time.Second), "direct")
	st.NoteOpenAIDispatch(now.Add(239*time.Second), "direct")
	rows := DispatchHistogramSnapshot(time.Minute)
	require.Len(t, rows, 1)
	row := rows[0]
	require.EqualValues(t, 2, row.Count)
	require.Equal(t, 31.0, row.Sum)
	require.Equal(t, "lt1m", row.BodyBucket)
	for _, bucket := range row.Buckets {
		if bucket.LE == 1 {
			require.EqualValues(t, 1, bucket.Count)
		}
		if bucket.LE >= 30 {
			require.EqualValues(t, 2, bucket.Count)
		}
	}
}

func TestRequestTimingCancelUsesArrivalAndUploadStage(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	st := NewRequestState()
	st.SetBudget(NewBudget(ctx, timingTestSettings(), KindText, now))
	st.InitDelivery(ctx, now, nil)
	st.BeginUpload()
	cancel()
	st.ResolveDisconnect()
	timing := st.Timing()
	require.NotNil(t, timing.ClientCancelMs)
	require.Equal(t, "upload", *timing.CancelStage)
	require.Nil(t, timing.BodyEOFMs)
	var nilState *RequestState
	require.Equal(t, RequestTiming{}, nilState.Timing())
	require.Nil(t, nilState.TimingJSON())
}

func timingTestSettings() *Settings { cfg := Defaults(); return &cfg }

func TestRequestTimingLateNoTimerUsesConfiguredBase(t *testing.T) {
	t0 := time.Unix(1000, 0)
	for _, base := range []int{60, 240, 290} {
		for _, mode := range []string{BudgetStartArrival, BudgetStartBodyEOF} {
			cfg := Defaults()
			cfg.Budget.PreOutputBudgetSeconds = base
			cfg.Budget.BudgetStart = mode
			st := NewRequestState()
			st.SetBudget(NewBudget(context.Background(), &cfg, KindText, t0))
			st.MarkBodyReceived(t0.Add(10*time.Second), "body_eof", 1024)
			edge := t0.Add(time.Duration(base)*time.Second - 2*time.Second)
			st.SetAttemptProfile(AttemptProfile{AttemptStart: edge})
			st.SetAttemptProfile(AttemptProfile{AttemptStart: edge.Add(time.Millisecond)})
			st.SetAttemptProfile(AttemptProfile{AttemptStart: edge.Add(time.Millisecond), Warm: true})
			st.SetAttemptProfile(AttemptProfile{AttemptStart: edge.Add(2 * time.Millisecond)})
			st.NoteAttemptTimer(edge.Add(2 * time.Millisecond))
			timing := st.Timing()
			all, noTimer := int64(2), int64(1)
			require.Equal(t, all, *timing.LateAttemptCount)
			require.Equal(t, noTimer, *timing.LateAttemptNoTimerCount)
			raw, err := json.Marshal(timing)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "late_attempt")
			require.Contains(t, *st.TimingJSON(), "late_attempt_no_timer_count")
		}
	}
}
