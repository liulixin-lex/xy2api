//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestQualityWindowCompletionRetentionAndNoSampleCap(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := engineSamples(250, now.Add(-time.Minute), true)
	for i := range rows {
		rows[i].StartedAt = now.Add(-4 * time.Hour)
	}
	w := schedQualityWindow{}
	w.merge(append(rows, rows...), now)
	require.Len(t, w.samples[SchedAccountModel{1, "synthetic-model"}], 250)
	w.take(now.Add(250 * time.Minute))
	require.Empty(t, w.samples)
}

type previousCountingStore struct {
	fakeSchedAttemptStore
	calls int
}

func (s *previousCountingStore) LoadPreviousAttempts(ctx context.Context, rows []q.AttemptSample) (map[string]*q.AttemptSample, error) {
	s.calls++
	return s.fakeSchedAttemptStore.LoadPreviousAttempts(ctx, rows)
}

func TestQualityWindowCrossAccountPredecessorAndSupplementCache(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	old := engineSamples(1, now.Add(-5*time.Minute), true)[0]
	old.SessionKey = "session"
	current := engineSamples(1, now.Add(-time.Minute), true)[0]
	current.SessionKey = "session"
	store := &previousCountingStore{fakeSchedAttemptStore: fakeSchedAttemptStore{Rows: []q.AttemptSample{old, current}}}
	w := schedQualityWindow{}
	w.merge([]q.AttemptSample{current}, now)
	require.NoError(t, w.prepareContinuations(context.Background(), store))
	require.True(t, w.samples[SchedAccountModel{1, "synthetic-model"}][0].Continuation)
	require.Equal(t, 1, store.calls)
	require.NoError(t, w.prepareContinuations(context.Background(), store))
	require.Equal(t, 1, store.calls, "missing-window predecessor is cached by current attempt")
	// An intervening failed attempt on another account interrupts the chain.
	middle := old
	middle.AttemptID = "middle"
	middle.StartedAt = current.StartedAt.Add(-time.Minute)
	middle.FinishedAt = middle.StartedAt.Add(time.Second)
	middle.AccountID = 2
	middle.Outcome = q.OutcomeAccountFault
	w.merge([]q.AttemptSample{middle}, now)
	require.NoError(t, w.prepareContinuations(context.Background(), store))
	require.False(t, w.samples[SchedAccountModel{1, "synthetic-model"}][0].Continuation)
}

func TestQualityWindowStrictEarlierAndFinishedPredecessor(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := engineSamples(3, now.Add(-time.Minute), true)
	for i := range rows {
		rows[i].SessionKey = "session"
	}
	rows[0].StartedAt = rows[2].StartedAt.Add(-time.Minute)
	rows[0].FinishedAt = rows[2].StartedAt.Add(time.Second) // still running at dispatch
	w := schedQualityWindow{}
	w.merge(rows, now)
	store := &previousCountingStore{}
	require.NoError(t, w.prepareContinuations(context.Background(), store))
	for _, r := range w.samples[SchedAccountModel{1, "synthetic-model"}] {
		require.False(t, r.Continuation)
	}
}

func TestQualityWindowMasksRunningCachePredecessorTemporarily(t *testing.T) {
	for _, tc := range []struct {
		name         string
		finishOffset time.Duration
		verdict      q.Verdict
		turns        int
	}{
		{"completed before dispatch", -time.Second, q.VerdictPass, q.CacheMinTurns},
		{"overlapped dispatch", time.Second, q.VerdictUnknown, q.CacheMinTurns - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newQualityFixture(t)
			read, input, writes := 8, 2, 0
			turns := engineSamples(q.CacheMinTurns, f.now.Add(-20*time.Second), true)
			for i := range turns {
				turn := &turns[i]
				turn.SessionKey = fmt.Sprintf("session-%d", i)
				turn.CacheReadTokens, turn.InputTokens, turn.CacheCreationTokens = &read, &input, &writes
				old := *turn
				old.AttemptID += "-old"
				old.StartedAt = turn.StartedAt.Add(-2 * time.Minute)
				old.FinishedAt = turn.StartedAt.Add(-time.Minute)
				f.ledger.Rows = append(f.ledger.Rows, old, *turn)
			}
			current := turns[0]
			pair := SchedAccountModel{current.AccountID, current.Model}
			line := q.LineState{LineKey: q.LineKey{GroupID: current.GroupID, AccountID: current.AccountID, Model: current.Model, Line: q.DimensionCache}}
			assess := func(e *SchedQualityEngineService, verdict q.Verdict, count int) {
				t.Helper()
				e.window.take(f.now)
				require.NoError(t, e.loadEvidence(ctx, f.now))
				d, err := e.inputs(ctx, f.now, e.window.samples)
				require.NoError(t, err)
				got, basis := e.assessLine(d, line, q.EventRegular, f.now, nil)
				require.Equal(t, verdict, got)
				require.Equal(t, count, basis.Cache.Turns)
				require.Equal(t, .8, basis.Cache.Ratio, "exact cache ratio threshold")
			}
			assess(f.e, q.VerdictPass, q.CacheMinTurns)
			prior := f.e.window.previous[current.AttemptID]
			middle := current
			middle.AttemptID += "-middle"
			middle.StartedAt = current.StartedAt.Add(-30 * time.Second)
			middle.FinishedAt = time.Time{}
			middle.CacheReadTokens = nil // The predecessor itself is not a cache evidence turn.
			for _, offset := range []time.Duration{0, time.Second} {
				other := middle
				other.AttemptID = fmt.Sprintf("non-predecessor-%d", offset)
				other.StartedAt = current.StartedAt.Add(offset)
				f.e.Observe(other)
			}
			assess(f.e, q.VerdictPass, q.CacheMinTurns)
			f.e.Observe(middle)
			assess(f.e, q.VerdictUnknown, q.CacheMinTurns-1)
			require.Same(t, prior, f.e.window.previous[current.AttemptID])
			for _, row := range f.e.window.samples[pair] {
				if row.AttemptID == current.AttemptID {
					require.True(t, row.Continuation, "mask must not change the window sample")
				}
			}
			middle.FinishedAt = current.StartedAt.Add(tc.finishOffset)
			middle.InsertedAt = f.now
			f.ledger.Rows = append(f.ledger.Rows, middle) // Arrived after its successor.
			assess(f.e, tc.verdict, tc.turns)
			require.Equal(t, middle.AttemptID, f.e.window.previous[current.AttemptID].AttemptID)
			fresh := NewSchedQualityEngineService(f.store, f.ledger, f.switches, func() time.Time { return f.now }, nil)
			assess(fresh, tc.verdict, tc.turns)
		})
	}
}

func TestQualityWindowObserverOnlyTracksInFlight(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a := engineSamples(1, now, true)[0]
	a.FinishedAt = time.Time{}
	w := schedQualityWindow{}
	w.add(a)
	w.take(now)
	require.Empty(t, w.samples)
	require.Contains(t, w.running, a.AttemptID)
	a.FinishedAt = now.Add(time.Second)
	w.add(a)
	w.take(now.Add(2 * time.Second))
	require.Empty(t, w.samples)
	require.Equal(t, a.FinishedAt, w.running[a.AttemptID].FinishedAt)
	// Finish is not durable evidence, and a delayed Begin cannot erase it.
	begin := a
	begin.FinishedAt = time.Time{}
	w.add(begin)
	w.take(now.Add(3 * time.Second))
	require.Equal(t, a.FinishedAt, w.running[a.AttemptID].FinishedAt)
	a.InsertedAt = now.Add(30 * time.Second)
	w.merge([]q.AttemptSample{a}, now.Add(31*time.Second))
	require.Empty(t, w.running)
	require.Equal(t, a.InsertedAt, schedAvailableAt(w.samples[SchedAccountModel{1, "synthetic-model"}][0]))
	w.add(a)
	w.take(now.Add(32 * time.Second))
	require.Empty(t, w.running, "ledger-backed attempts cannot be resurrected")
	orphan := a
	orphan.AttemptID = "orphan"
	w.add(orphan)
	w.take(now.Add(2 * time.Minute))
	require.Contains(t, w.running, orphan.AttemptID)
	w.take(now.Add(2*time.Minute + time.Second))
	require.NotContains(t, w.running, orphan.AttemptID)
	begin.AttemptID = "never-finished"
	begin.StartedAt = now
	w.add(begin)
	w.take(now.Add(2 * time.Hour))
	require.Empty(t, w.running)
}
