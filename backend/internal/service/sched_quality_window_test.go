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
	require.Len(t, w.recent[pair], 200)
	require.Len(t, out[pair], 201)
	require.Equal(t, "000", w.recent[pair][0].AttemptID)
	require.Equal(t, "199", w.recent[pair][199].AttemptID)
	require.Equal(t, 1, *w.acute[pair][0].TTFOMs)
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
