//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestEngineLateLedgerFastEvidenceMatchesRestart(t *testing.T) {
	f := newQualityFixture(t)
	f.cycle() // The 12:00 horizon has been processed without these records.
	f.now = f.now.Add(15 * time.Second)
	completion := f.now.Add(-24 * time.Second) // 12:00:01
	f.cycle()                                  // horizon 12:00:15; ledger insertion is still delayed.
	require.NoError(t, f.ledger.InsertBatch(context.Background(), engineSamples(6, completion, true)))
	f.now = f.now.Add(5 * time.Second)
	f.cycle()
	steady := f.view()
	other := NewSchedQualityEngineService(f.store, f.ledger, f.switches, func() time.Time { return f.now }, nil)
	require.NoError(t, other.cycle(context.Background()))
	restarted := other.Snapshot().View.StateOf(10, 1, "synthetic-model")
	require.True(t, restarted.Failing, "a replay of identical durable evidence enters fast track")
	require.Equal(t, restarted.Failing, steady.Failing, "same durable evidence must not require a restart/leader change to be considered")
}

func TestQualityWindowCannotSkipRealPredecessor(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	current := engineSamples(1, now, true)[0]
	current.SessionKey = "synthetic-session"
	long := current
	long.AttemptID, long.StartedAt, long.FinishedAt = "long", now.Add(-182*time.Minute), now.Add(-179*time.Minute)
	intervening := current
	intervening.AttemptID = "intervening"
	intervening.StartedAt, intervening.FinishedAt = now.Add(-181*time.Minute), now.Add(-180*time.Minute-30*time.Second)
	intervening.Outcome = q.OutcomeAccountFault
	store := &previousCountingStore{fakeSchedAttemptStore: fakeSchedAttemptStore{Rows: []q.AttemptSample{long, intervening, current}}}
	w := schedQualityWindow{}
	w.merge(store.Rows, now)
	require.Len(t, w.samples[SchedAccountModel{1, "synthetic-model"}], 2)
	require.NoError(t, w.prepareContinuations(context.Background(), store))
	for _, row := range w.samples[SchedAccountModel{1, "synthetic-model"}] {
		if row.AttemptID == current.AttemptID {
			require.False(t, row.Continuation, "the actual predecessor failed; an older long-running success cannot replace it")
		}
	}
}

func TestQualityWindowLatePredecessorCorrectsCachedFacts(t *testing.T) {
	for _, hasPrevious := range []bool{false, true} {
		t.Run(map[bool]string{false: "negative-cache", true: "positive-cache"}[hasPrevious], func(t *testing.T) {
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			current := engineSamples(1, now, true)[0]
			current.SessionKey = "late-predecessor-session"
			old := current
			old.AttemptID, old.StartedAt, old.FinishedAt = "older-success", now.Add(-time.Hour), now.Add(-time.Hour+time.Second)
			store := &previousCountingStore{fakeSchedAttemptStore: fakeSchedAttemptStore{Rows: []q.AttemptSample{current}}}
			if hasPrevious {
				store.Rows = append(store.Rows, old)
			}
			w := schedQualityWindow{}
			w.merge(store.Rows, now)
			require.NoError(t, w.prepareContinuations(context.Background(), store))
			require.Equal(t, hasPrevious, w.previous[current.AttemptID] != nil)
			late := old
			late.AttemptID, late.StartedAt, late.FinishedAt = "late-failure", now.Add(-time.Minute), now.Add(-time.Minute+time.Second)
			late.Outcome = q.OutcomeAccountFault
			// Visible locally before ledger commit: it still supersedes the cached fact.
			w.merge([]q.AttemptSample{late}, now)
			require.NoError(t, w.prepareContinuations(context.Background(), store))
			require.Equal(t, late.AttemptID, w.previous[current.AttemptID].AttemptID)
			for _, row := range w.samples[SchedAccountModel{1, "synthetic-model"}] {
				if row.AttemptID == current.AttemptID {
					require.False(t, row.Continuation)
				}
			}
		})
	}
}

func TestEngineLedgerOverlapDoesNotRewindTwice(t *testing.T) {
	f := newQualityFixture(t)
	f.cycle()
	f.now = f.now.Add(15 * time.Second)
	f.cycle()
	completion := f.now.Add(-24 * time.Second)
	require.NoError(t, f.ledger.InsertBatch(context.Background(), engineSamples(6, completion, true)))
	f.now = f.now.Add(5 * time.Second)
	f.cycle()
	require.True(t, f.view().Failing)
	require.True(t, f.e.window.newFinishedAt.IsZero())
	f.now = f.now.Add(5 * time.Second)
	f.cycle()
	require.Len(t, f.store.Decisions, 1)
	require.Equal(t, f.now.Add(-10*time.Second), f.e.processedAt)
	require.True(t, f.e.window.newFinishedAt.IsZero())
}

func TestEngineFailedCommitKeepsWatermarkAndPendingReplay(t *testing.T) {
	f := newQualityFixture(t)
	f.cycle()
	original := f.e.Snapshot()
	processed := f.e.processedAt
	f.now = f.now.Add(15 * time.Second)
	require.NoError(t, f.ledger.InsertBatch(context.Background(), engineSamples(6, processed.Add(-time.Second), true)))
	f.store.CommitErr = errors.New("synthetic commit failure")
	require.ErrorIs(t, f.e.cycle(context.Background()), f.store.CommitErr)
	require.Same(t, original, f.e.Snapshot())
	require.Equal(t, processed, f.e.processedAt)
	require.Empty(t, f.store.Rows)
	require.Empty(t, f.store.Decisions)
	require.False(t, f.e.window.newFinishedAt.IsZero())
	f.store.CommitErr = nil
	f.cycle()
	require.True(t, f.view().Failing)
	require.Len(t, f.store.Decisions, 1)
}
