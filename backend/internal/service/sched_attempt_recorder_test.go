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
	store := &schedAttemptFailStore{}
	r := NewSchedAttemptRecorderService(store)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	for i := 0; i < 8193; i++ {
		r.Begin(SchedAttemptStart{RequestID: "request"}).FinishOther("failure")
	}
	require.EqualValues(t, 1, r.Stats().DroppedQueueFull)
	require.Equal(t, 8192, r.Stats().QueueDepth)
	first := r.takeBatch()
	require.Len(t, first, 200)
	var pending []schedquality.AttemptSample
	require.Error(t, r.drain(context.Background(), &pending))
	require.EqualValues(t, 7992, r.Stats().DroppedWriteFailed)
	store.muCalls.Lock()
	require.Equal(t, 40*4, store.calls)
	store.muCalls.Unlock()
	r.Stop()
}
