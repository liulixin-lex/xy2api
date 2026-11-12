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

func TestSchedQualityWindowOrderingDedupeAndAcuteOther(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	var w schedQualityWindow
	pair := SchedAccountModel{1, "model"}
	// Reverse arrival order, same start-time ties, and repeated ledger overlap.
	for i := 0; i < 210; i++ {
		r := q.AttemptSample{AttemptID: fmt.Sprintf("%03d", i), AccountID: 1, Model: "model", Outcome: q.OutcomeOK, StartedAt: now.Add(-time.Duration(i) * time.Second)}
		w.add(r)
		w.add(r)
	}
	for i := 0; i < 100; i++ {
		w.add(q.AttemptSample{AttemptID: fmt.Sprint("other", i), AccountID: 1, Model: "model", Outcome: q.OutcomeOther, StartedAt: now})
	}
	ms := 1
	w.add(q.AttemptSample{AttemptID: "acute-other", AccountID: 1, Model: "model", Outcome: q.OutcomeOther, StartedAt: now, Stream: true, TTFOMs: &ms})
	ms = 999
	out := w.take(now)
	require.Len(t, w.samples[pair], 201)
	require.Len(t, out[pair], 201)
	require.Equal(t, "000", w.samples[pair][1].AttemptID)
	require.Equal(t, "199", w.samples[pair][200].AttemptID)
	require.Equal(t, 1, *w.samples[pair][0].TTFOMs)
	w.add(q.AttemptSample{AttemptID: "too-old", AccountID: 1, Model: "model", StartedAt: now.Add(-7 * time.Hour), Outcome: q.OutcomeOK})
	require.Len(t, w.take(now)[pair], 201)
	require.Empty(t, w.take(now.Add(q.WindowMaxAge+time.Second)))
}
func TestSchedQualityAcuteOtherBreaksStreak(t *testing.T) {
	f := newQualityFixture(t)
	f.acute(2)
	ms := 1000
	f.e.Observe(q.AttemptSample{AttemptID: "other-first-output", AccountID: 1, Model: "gpt-6.1-sol", StartedAt: f.now, Stream: true, Outcome: q.OutcomeOther, TTFOMs: &ms})
	f.now = f.now.Add(time.Second)
	f.acute(1)
	f.cycle()
	require.Equal(t, q.StateNormal, f.view(10).State)
	require.False(t, f.e.Snapshot().Evidence[SchedAccountModel{1, "gpt-6.1-sol"}].Acute)
	require.Equal(t, 3, f.e.Snapshot().Evidence[SchedAccountModel{1, "gpt-6.1-sol"}].Samples)
}
func TestSchedQualityObserveDuringTransaction(t *testing.T) {
	f := newQualityFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.e.store = &qualityHookStore{fakeSchedQualityStore: f.store, hook: func() { close(entered); <-release }}
	cycleDone := make(chan error, 1)
	go func() { cycleDone <- f.e.cycle(context.Background()) }()
	<-entered
	// Observe must finish while the transaction and the cycle mutex are still held.
	observed := make(chan struct{})
	go func() {
		f.e.Observe(q.AttemptSample{AttemptID: "live", AccountID: 1, Model: "live", StartedAt: f.now, Outcome: q.OutcomeOK})
		close(observed)
	}()
	select {
	case <-observed:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("Observe blocked on the evaluation transaction")
	}
	close(release)
	require.NoError(t, <-cycleDone)
}

func TestSchedQualityCycleMergesLedgerWithSingleObserveDrain(t *testing.T) {
	f := newQualityFixture(t)
	pair := SchedAccountModel{1, "cycle-boundary"}
	sample := func(id string) q.AttemptSample {
		return q.AttemptSample{AttemptID: id, AccountID: pair.AccountID, Model: pair.Model, StartedAt: f.now, InsertedAt: f.now, Outcome: q.OutcomeOK}
	}
	f.e.Observe(sample("before-cycle"))
	f.e.loaded = true
	reads := 0
	f.e.attempts = &qualityPageStore{fakeSchedAttemptStore: f.ledger, read: func(time.Time, int) ([]q.AttemptSample, time.Time, error) {
		reads++
		require.Empty(t, f.e.window.pending, "the cycle must drain local samples before reading the ledger")
		if reads == 1 {
			f.e.Observe(sample("during-ledger-read"))
		}
		return []q.AttemptSample{sample("before-cycle"), sample("from-ledger")}, f.now, nil
	}}
	f.cycle()
	require.Equal(t, 2, f.e.Snapshot().Evidence[pair].Samples, "new ledger samples join this cycle and overlap is deduplicated")
	require.Len(t, f.e.window.pending[pair], 1, "Observe is drained only once at cycle start")
	f.cycle()
	require.Equal(t, 3, f.e.Snapshot().Evidence[pair].Samples)
	require.Empty(t, f.e.window.pending)
}

func TestSchedQualityWindowReusesSortedSamples(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	var window schedQualityWindow
	pair := SchedAccountModel{1, "reuse"}
	for i := 0; i < q.WindowMaxSamples; i++ {
		window.add(q.AttemptSample{AttemptID: fmt.Sprintf("%03d", i), AccountID: 1, Model: pair.Model, StartedAt: now.Add(-time.Duration(i) * time.Second), Outcome: q.OutcomeOK})
	}
	original := window.take(now)[pair]
	require.Equal(t, float64(0), testing.AllocsPerRun(5, func() { window.take(now) }))
	require.Same(t, &original[0], &window.take(now)[pair][0])
	window.merge([]q.AttemptSample{original[0]}, now)
	require.Same(t, &original[0], &window.samples[pair][0], "ledger overlap must not rebuild the sorted window")
	late := q.AttemptSample{AttemptID: "late", AccountID: 1, Model: pair.Model, StartedAt: now.Add(-time.Millisecond), Outcome: q.OutcomeOK}
	window.merge([]q.AttemptSample{late}, now)
	require.Len(t, window.samples[pair], q.WindowMaxSamples)
	require.Equal(t, "000", window.samples[pair][0].AttemptID)
	require.Equal(t, "late", window.samples[pair][1].AttemptID)
	require.Equal(t, "198", window.samples[pair][199].AttemptID)
	require.Equal(t, "199", original[199].AttemptID, "merging new samples must not mutate the previous sorted slice")
}
