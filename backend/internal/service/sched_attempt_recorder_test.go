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
	in := SchedAttemptStart{AccountID: 1, GroupID: 2, Model: "model", Stream: true}
	a := r.Begin(in)
	now = now.Add(20 * time.Second)
	a.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeTimeout, Waited: 20 * time.Second})
	a.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault, Detail: "late"})
	in.AccountID = 2
	b := r.Begin(in)
	now = now.Add(3 * time.Second)
	b.MarkFirstOutput(now)
	b.MarkFirstOutput(now.Add(time.Second))
	b.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeOK, Usage: &SchedAttemptUsage{PromptTokens: 100000, CacheReadTokens: 90000}})
	c := r.Begin(in)
	now = now.Add(time.Second)
	c.MarkFirstOutput(now)
	c.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault, Detail: "STALL"})
	c.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeOK, Usage: nil})
	d := r.Begin(in)
	d.Finish(SchedAttemptFinish{Outcome: schedquality.Outcome(5), Detail: "CLIENT_CANCEL"})
	d.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeTimeout, Waited: time.Second})
	e := r.Begin(in)
	e.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeOK, Usage: nil})
	require.Len(t, observed, 5)
	require.Equal(t, schedquality.OutcomeTimeout, observed[0].Outcome)
	require.Equal(t, 20000, *observed[0].CensoredMs)
	require.Equal(t, 3000, *observed[1].TTFOMs)
	require.Equal(t, 100000, *observed[1].PromptTokens)
	require.Equal(t, 90000, *observed[1].CacheReadTokens)
	require.Equal(t, schedquality.OutcomeAccountFault, observed[2].Outcome)
	require.Equal(t, 1000, *observed[2].TTFOMs)
	require.Equal(t, schedquality.Outcome(5), observed[3].Outcome)
	require.Nil(t, observed[4].PromptTokens)
	require.Nil(t, observed[4].CacheReadTokens)
	for _, row := range observed {
		id, err := uuid.Parse(row.AttemptID)
		require.NoError(t, err)
		require.Equal(t, uuid.Version(7), id.Version())
	}
	require.NoError(t, r.Flush(context.Background()))
	require.Len(t, store.Rows, 5)
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
