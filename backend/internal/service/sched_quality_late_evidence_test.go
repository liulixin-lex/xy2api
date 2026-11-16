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

func TestQualityWindowCannotSkipRealPredecessor(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	current := engineSamples(1, now, true)[0]
	current.SessionKey = "synthetic-session"
	long := current
	long.AttemptID, long.StartedAt, long.FinishedAt = "long", now.Add(-252*time.Minute), now.Add(-249*time.Minute)
	intervening := current
	intervening.AttemptID = "intervening"
	intervening.StartedAt, intervening.FinishedAt = now.Add(-251*time.Minute), now.Add(-250*time.Minute-30*time.Second)
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

func TestEngineFailedCommitKeepsWatermarkAndPendingReplay(t *testing.T) {
	f := newQualityFixture(t)
	f.cycle()
	original := f.e.Snapshot()
	processed := f.e.processedAt
	f.badTTFT()
	f.now = f.now.Add(5 * time.Minute)
	f.store.CommitErr = errors.New("synthetic commit failure")
	require.ErrorIs(t, f.e.cycle(context.Background()), f.store.CommitErr)
	require.Same(t, original, f.e.Snapshot())
	require.Equal(t, processed, f.e.processedAt)
	require.Empty(t, f.store.Rows)
	require.False(t, f.e.window.newFinishedAt.IsZero())
	f.store.CommitErr = nil
	f.cycle()
	require.Equal(t, q.IdentityRear, f.view().TTFO.Identity)
	decisions := len(f.store.Decisions)
	f.now = f.now.Add(5 * time.Second)
	f.cycle()
	require.Len(t, f.store.Decisions, decisions)
	require.True(t, f.e.window.newFinishedAt.IsZero())
}
