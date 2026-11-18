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
	var events []schedquality.AttemptSample
	r.SetObserver(func(s schedquality.AttemptSample) { events = append(events, s) })
	r.Start(context.Background())
	t.Cleanup(r.Stop)
	in := SchedAttemptStart{AccountID: 1, GroupID: 2, Model: "model", Stream: true}
	a := r.Begin(in)
	now = now.Add(20 * time.Second)
	a.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeTimeout, Waited: 20 * time.Second, Warm: true, FullTierFirstOutputTimeout: true})
	a.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault, Detail: "late"})
	in.AccountID = 2
	in.Warm = true
	b := r.Begin(in)
	b.MarkOutput(OutputFirstEvent, now.Add(time.Second))
	b.MarkOutput(OutputFirstEvent, now.Add(2*time.Second))
	now = now.Add(3 * time.Second)
	b.MarkOutput(OutputValid, now)
	b.MarkOutput(OutputValid, now.Add(time.Second))
	b.MarkOutput(OutputAnswer, now.Add(200*time.Millisecond))
	b.MarkOutput(OutputAnswer, now.Add(400*time.Millisecond))
	now = now.Add(500 * time.Millisecond)
	b.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeOK, Usage: &SchedAttemptUsage{PromptTokens: 100000, CacheReadTokens: 90000, CacheReadTokensPresent: true}})
	c := r.Begin(in)
	now = now.Add(time.Second)
	c.MarkOutput(OutputValid, now)
	c.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault, Detail: "STALL",
		Class: schedquality.ClassInterrupted, HTTPStatus: 503, StatusOrigin: schedquality.OriginUpstream, OutputBytes: 128})
	c.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeOK, Usage: nil})
	d := r.Begin(in)
	d.Finish(SchedAttemptFinish{Outcome: schedquality.Outcome(5), Detail: "CLIENT_CANCEL"})
	d.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeTimeout, Waited: time.Second})
	e := r.Begin(in)
	e.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeOK, Usage: nil})
	e.MarkOutput(OutputAnswer, now.Add(time.Second))
	require.Len(t, events, 10, "Begin and Finish must each notify exactly once")
	var observed []schedquality.AttemptSample
	for i := 0; i < len(events); i += 2 {
		require.Equal(t, 3, events[i].CaptureVersion)
		require.Equal(t, 3, events[i+1].CaptureVersion)
		require.False(t, events[i].FullTierFirstOutputTimeout)
		require.True(t, events[i].FinishedAt.IsZero())
		require.False(t, events[i+1].FinishedAt.IsZero())
		require.Equal(t, events[i].AttemptID, events[i+1].AttemptID)
		require.Nil(t, events[i].FirstEventMs, "in-flight snapshots must stay immutable")
		observed = append(observed, events[i+1])
	}
	require.Len(t, observed, 5)
	require.False(t, events[0].Warm)
	require.True(t, observed[0].Warm)
	require.True(t, observed[0].FullTierFirstOutputTimeout)
	require.True(t, events[2].Warm)
	require.False(t, observed[1].Warm)
	require.False(t, observed[1].FullTierFirstOutputTimeout)
	require.Equal(t, schedquality.OutcomeTimeout, observed[0].Outcome)
	require.Equal(t, 20000, *observed[0].CensoredMs)
	require.Equal(t, 3000, *observed[1].TTFOMs)
	require.Equal(t, 1000, *observed[1].FirstEventMs)
	require.Equal(t, 3200, *observed[1].FirstAnswerMs)
	require.Equal(t, 100000, *observed[1].PromptTokens)
	require.Equal(t, 90000, *observed[1].CacheReadTokens)
	require.Equal(t, schedquality.OutcomeAccountFault, observed[2].Outcome)
	require.Equal(t, 1000, *observed[2].TTFOMs)
	require.Equal(t, schedquality.ClassInterrupted, observed[2].Class)
	require.Equal(t, 503, *observed[2].HTTPStatus)
	require.Equal(t, schedquality.OriginUpstream, observed[2].StatusOrigin)
	require.Equal(t, 128, *observed[2].OutputBytes)
	require.Equal(t, schedquality.Outcome(5), observed[3].Outcome)
	require.Nil(t, observed[4].PromptTokens)
	require.Nil(t, observed[4].CacheReadTokens)
	require.Nil(t, observed[4].FirstAnswerMs)
	require.Nil(t, observed[4].HTTPStatus)
	for _, row := range observed {
		id, err := uuid.Parse(row.AttemptID)
		require.NoError(t, err)
		require.Equal(t, uuid.Version(7), id.Version())
	}
	require.NoError(t, r.Flush(context.Background()))
	require.Len(t, store.Rows, 5)
	for i, stored := range store.Rows {
		require.False(t, stored.InsertedAt.IsZero(), "the store assigns the database arrival time")
		require.True(t, observed[i].InsertedAt.IsZero(), "local observers must not invent database arrival times")
		stored.InsertedAt = time.Time{}
		require.Equal(t, observed[i], stored, "finished metadata must reach the ledger unchanged")
	}
	require.Len(t, events, 10, "flushing must not notify observers again")
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
		r.Begin(SchedAttemptStart{RequestID: "request"}).Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault, Detail: "failure"})
	}
	require.EqualValues(t, 1, r.Stats().DroppedQueueFull)
	require.Equal(t, schedquality.LedgerQueueSize, r.Stats().QueueDepth)
	first := r.takeBatch()
	require.Len(t, first, schedquality.LedgerBatchSize)
	r.Stop()

	store := &schedAttemptFailStore{}
	r = NewSchedAttemptRecorderService(store)
	r.now = func() time.Time { return now }
	r.Begin(SchedAttemptStart{RequestID: "retry"}).Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault, Detail: "failure"})
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

	r.Begin(SchedAttemptStart{RequestID: "recovered"}).Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault, Detail: "failure"})
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
