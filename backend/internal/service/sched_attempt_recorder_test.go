//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/stretchr/testify/require"
)

func TestSchedAttemptLifecycle(t *testing.T) {
	store := &fakeSchedAttemptStore{}
	r := NewSchedAttemptRecorderService(store)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	var observed []schedquality.AttemptSample
	r.SetObserver(func(s schedquality.AttemptSample) { observed = append(observed, s) })
	r.Start(context.Background())
	t.Cleanup(r.Stop)
	in := SchedAttemptStart{AccountID: 1, GroupID: 2, Model: "model", Stream: true, BodyBytes: 100}
	a := r.Begin(in)
	now = now.Add(20 * time.Second)
	a.FinishTimeout(20 * time.Second)
	a.FinishOther("late")
	in.AccountID = 2
	b := r.Begin(in)
	now = now.Add(3 * time.Second)
	b.MarkFirstOutput(now)
	b.MarkFirstOutput(now.Add(time.Second))
	b.FinishOK(&SchedAttemptUsage{PromptTokens: 100000, CacheReadTokens: 90000})
	c := r.Begin(in)
	now = now.Add(time.Second)
	c.MarkFirstOutput(now)
	c.FinishFault("STALL")
	c.FinishOK(nil)
	d := r.Begin(in)
	d.FinishOther("client_cancel")
	d.FinishTimeout(time.Second)
	e := r.Begin(in)
	e.FinishOK(nil)
	require.Len(t, observed, 5)
	require.Equal(t, schedquality.OutcomeTimeout, observed[0].Outcome)
	require.Equal(t, 20000, *observed[0].CensoredMs)
	require.Equal(t, 3000, *observed[1].TTFOMs)
	require.Equal(t, 100000, *observed[1].PromptTokens)
	require.Equal(t, 90000, *observed[1].CacheReadTokens)
	require.Equal(t, schedquality.OutcomeAccountFault, observed[2].Outcome)
	require.Equal(t, 1000, *observed[2].TTFOMs)
	require.Equal(t, schedquality.OutcomeOther, observed[3].Outcome)
	require.Nil(t, observed[4].PromptTokens)
	require.Nil(t, observed[4].PrevPromptTokens)
	require.Nil(t, observed[4].CacheReadTokens)
	for _, row := range observed {
		id, err := uuid.Parse(row.AttemptID)
		require.NoError(t, err)
		require.Equal(t, uuid.Version(7), id.Version())
	}
	require.NoError(t, r.Flush(context.Background()))
	require.Len(t, store.Rows, 5)
}

func TestSchedAttemptSessionAndTestIsolation(t *testing.T) {
	store := &fakeSchedAttemptStore{}
	r := NewSchedAttemptRecorderService(store)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	var observed []schedquality.AttemptSample
	r.SetObserver(func(s schedquality.AttemptSample) { observed = append(observed, s) })
	in := SchedAttemptStart{AccountID: 1, GroupID: 2, Model: "model", SessionKey: "session", StickyWarm: true, Effort: "high", BodyBytes: 5}
	r.Begin(in).FinishOK(&SchedAttemptUsage{PromptTokens: 130000})
	now = now.Add(4 * time.Minute)
	warm := r.Begin(in)
	warm.FinishOK(&SchedAttemptUsage{PromptTokens: 3})
	require.True(t, observed[1].Warm)
	require.Equal(t, 130000, *observed[1].PrevPromptTokens)
	require.Equal(t, schedquality.ClassHeavy, observed[1].Class)
	now = now.Add(6 * time.Minute)
	r.Begin(in).FinishOK(&SchedAttemptUsage{PromptTokens: 100})
	require.False(t, observed[2].Warm)
	in.AccountID = 2
	r.Begin(in).FinishOK(&SchedAttemptUsage{PromptTokens: 200})
	require.False(t, observed[3].Warm)
	in.AccountID = 1
	r.Begin(in).FinishOK(&SchedAttemptUsage{PromptTokens: 300})
	require.False(t, observed[4].Warm)
	in.StickyWarm = false
	r.Begin(in).FinishOK(&SchedAttemptUsage{PromptTokens: 400})
	require.False(t, observed[5].Warm)
	var testRows []schedquality.AttemptSample
	in.Test = &SchedTestAttempt{Warm: true, PrevPromptTokens: 150000, Sink: func(s schedquality.AttemptSample) { testRows = append(testRows, s) }}
	r.Begin(in).FinishOK(&SchedAttemptUsage{PromptTokens: 1})
	require.Len(t, observed, 6)
	require.Equal(t, 6, r.Stats().QueueDepth)
	require.Len(t, testRows, 1)
	require.True(t, testRows[0].Warm)
	require.Equal(t, 150000, *testRows[0].PrevPromptTokens)
	require.Equal(t, schedquality.ClassHeavy, testRows[0].Class)
	in.Test = nil
	in.StickyWarm = true
	r.Begin(in).FinishOK(&SchedAttemptUsage{PromptTokens: 1})
	require.True(t, observed[6].Warm)
	require.Equal(t, 400, *observed[6].PrevPromptTokens)

	// The unstarted recorder drains its pending samples on Stop.
	r.Stop()
	require.Len(t, store.Rows, 7)
}

func TestSchedAttemptSessionUnknownUsage(t *testing.T) {
	store := &fakeSchedAttemptStore{}
	r := NewSchedAttemptRecorderService(store)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	var observed []schedquality.AttemptSample
	r.SetObserver(func(s schedquality.AttemptSample) { observed = append(observed, s) })
	in := SchedAttemptStart{AccountID: 1, GroupID: 2, Model: "gpt-6.1-sol", SessionKey: "session", StickyWarm: true, Stream: true, Effort: "xhigh", BodyBytes: 800000}
	r.Begin(in).FinishOK(&SchedAttemptUsage{PromptTokens: 100})
	now = now.Add(4 * time.Minute)
	interrupted := r.Begin(in)
	now = now.Add(time.Second)
	interrupted.MarkFirstOutput(now)
	interrupted.FinishOK(nil)
	interrupted.FinishOK(&SchedAttemptUsage{PromptTokens: 200, CacheReadTokens: 100})
	require.Len(t, observed, 2)
	require.True(t, observed[1].Warm)
	require.Equal(t, schedquality.ClassRegular, observed[1].Class)
	require.Equal(t, 1000, *observed[1].TTFOMs)
	require.Nil(t, observed[1].PromptTokens)
	require.Nil(t, observed[1].PrevPromptTokens)
	require.Nil(t, observed[1].CacheReadTokens)

	now = now.Add(4 * time.Minute)
	r.Begin(in).FinishOK(&SchedAttemptUsage{PromptTokens: 160000})
	require.Len(t, observed, 3)
	require.True(t, observed[2].Warm)
	require.Nil(t, observed[2].PrevPromptTokens)
	require.Equal(t, schedquality.ClassOf(in.Effort, schedquality.EstimateTokens(in.BodyBytes)), observed[2].Class)
	require.Equal(t, schedquality.ClassXHeavy, observed[2].Class)
	r.Stop()
	require.Len(t, store.Rows, 3)
	require.Nil(t, store.Rows[1].PromptTokens)
	require.Nil(t, store.Rows[1].PrevPromptTokens)
	require.Nil(t, store.Rows[1].CacheReadTokens)
}

type schedAttemptFailStore struct {
	fakeSchedAttemptStore
	muCalls sync.Mutex
	calls   int
}

func (s *schedAttemptFailStore) InsertBatch(context.Context, []schedquality.AttemptSample) error {
	s.muCalls.Lock()
	defer s.muCalls.Unlock()
	s.calls++
	return errors.New("write failed")
}

func TestSchedAttemptQueueDropsAndRetries(t *testing.T) {
	r := NewSchedAttemptRecorderService(&fakeSchedAttemptStore{})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	for i := 0; i < schedquality.LedgerQueueSize+1; i++ {
		r.Begin(SchedAttemptStart{RequestID: "request"}).FinishOther("failure")
	}
	require.EqualValues(t, 1, r.Stats().DroppedQueueFull)
	require.Equal(t, schedquality.LedgerQueueSize, r.Stats().QueueDepth)
	first := r.takeBatch()
	require.Len(t, first, schedquality.LedgerBatchSize)
	r.Stop()

	store := &schedAttemptFailStore{}
	r = NewSchedAttemptRecorderService(store)
	r.now = func() time.Time { return now }
	r.Begin(SchedAttemptStart{RequestID: "retry"}).FinishOther("failure")
	var pending schedAttemptPending
	for attempt := 1; attempt <= schedquality.LedgerWriteRetries; attempt++ {
		require.NoError(t, r.drain(context.Background(), &pending))
		require.Len(t, pending.rows, 1)
		require.Equal(t, "retry", pending.rows[0].RequestID)
		require.Equal(t, attempt, pending.failures)
		require.Zero(t, r.Stats().DroppedWriteFailed)
		require.Equal(t, attempt, store.calls)
	}
	ticks := make(chan time.Time, 1)
	ticks <- now.Add(schedquality.LedgerFlushInterval)
	require.Error(t, r.flushPending(context.Background(), &pending, ticks))
	require.Empty(t, ticks)
	require.Empty(t, pending.rows)
	require.Zero(t, pending.failures)
	require.EqualValues(t, 1, r.Stats().DroppedWriteFailed)
	require.Equal(t, schedquality.LedgerWriteRetries+1, store.calls)

	r.Begin(SchedAttemptStart{RequestID: "recovered"}).FinishOther("failure")
	require.NoError(t, r.drain(context.Background(), &pending))
	require.Len(t, pending.rows, 1)
	recovered := &fakeSchedAttemptStore{}
	r.store = recovered
	ticks <- now.Add(2 * schedquality.LedgerFlushInterval)
	require.NoError(t, r.flushPending(context.Background(), &pending, ticks))
	require.Empty(t, ticks)
	require.Empty(t, pending.rows)
	require.Zero(t, pending.failures)
	require.Len(t, recovered.Rows, 1)
	require.Equal(t, "recovered", recovered.Rows[0].RequestID)
	require.EqualValues(t, 1, r.Stats().DroppedWriteFailed)
	r.Stop()
}
