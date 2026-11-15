//go:build unit

package service

import (
	"context"
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
	w.take(now.Add(180 * time.Minute))
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
	old := engineSamples(1, now.Add(-181*time.Minute), true)[0]
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
