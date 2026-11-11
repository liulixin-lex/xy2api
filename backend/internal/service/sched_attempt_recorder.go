package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"go.uber.org/zap"
)

type schedAttemptFlush struct {
	ctx  context.Context
	done chan error
}

type schedAttemptPending struct {
	rows     []schedquality.AttemptSample
	failures int
}

type SchedAttemptRecorderStats struct {
	QueueDepth         int
	DroppedQueueFull   uint64
	DroppedWriteFailed uint64
}

type SchedAttemptRecorderService struct {
	store  SchedAttemptStore
	now    func() time.Time
	memory *schedAttemptSessionMemory

	observerMu sync.RWMutex
	observer   func(schedquality.AttemptSample)

	queueMu sync.Mutex
	queue   []schedquality.AttemptSample
	head    int
	count   int
	wake    chan struct{}
	flush   chan schedAttemptFlush
	done    chan struct{}

	lifecycleMu sync.Mutex
	started     bool
	cancel      context.CancelFunc
	stopped     atomic.Bool

	droppedQueueFull   atomic.Uint64
	droppedWriteFailed atomic.Uint64
	lastQueueWarning   atomic.Int64
	lastWriteWarning   atomic.Int64
}

var _ SchedAttemptRecorder = (*SchedAttemptRecorderService)(nil)

func NewSchedAttemptRecorderService(store SchedAttemptStore) *SchedAttemptRecorderService {
	return &SchedAttemptRecorderService{
		store: store, now: time.Now, memory: newSchedAttemptSessionMemory(),
		queue: make([]schedquality.AttemptSample, 8192),
		wake:  make(chan struct{}, 1), flush: make(chan schedAttemptFlush), done: make(chan struct{}),
	}
}

func (s *SchedAttemptRecorderService) Start(ctx context.Context) {
	if s == nil || s.store == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started || s.stopped.Load() {
		return
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	go s.run(ctx)
}

func (s *SchedAttemptRecorderService) Stop() {
	if s == nil || s.store == nil {
		return
	}
	// Also drain samples collected before the lifecycle was started.
	s.Start(context.Background())
	s.lifecycleMu.Lock()
	s.stopped.Store(true)
	s.cancel()
	s.lifecycleMu.Unlock()
	<-s.done
}

func (s *SchedAttemptRecorderService) SetObserver(fn func(schedquality.AttemptSample)) {
	if s == nil {
		return
	}
	s.observerMu.Lock()
	s.observer = fn
	s.observerMu.Unlock()
}

func (s *SchedAttemptRecorderService) Begin(in SchedAttemptStart) SchedAttempt {
	if s == nil || s.stopped.Load() || (s.store == nil && in.Test == nil) {
		return schedAttemptNoop{}
	}
	at := s.now()
	row := schedquality.AttemptSample{
		AttemptID: uuid.Must(uuid.NewV7()).String(), StartedAt: at,
		AccountID: in.AccountID, GroupID: in.GroupID, Model: in.Model, Stream: in.Stream,
		SessionKey: in.SessionKey, RequestID: in.RequestID,
	}
	estTokens := schedquality.EstimateTokens(in.BodyBytes)
	if in.Test != nil {
		row.Warm = in.Test.Warm
		if row.Warm {
			prev := in.Test.PrevPromptTokens
			row.PrevPromptTokens = &prev
		}
	} else if in.SessionKey != "" {
		prev, ok := s.memory.get(schedAttemptSessionKey{in.GroupID, in.SessionKey}, at)
		row.Warm = in.StickyWarm && ok && prev.accountID == in.AccountID && prev.model == in.Model &&
			at.Sub(prev.finishedAt) <= schedquality.WarmMaxGap
		if row.Warm {
			row.PrevPromptTokens = prev.promptTokens
		}
	}
	if row.PrevPromptTokens != nil {
		estTokens = *row.PrevPromptTokens
	}
	row.Class = schedquality.ClassOf(in.Effort, estTokens)
	var sink func(schedquality.AttemptSample)
	if in.Test != nil {
		sink = in.Test.Sink
	}
	return &schedAttemptHandle{recorder: s, row: row, test: in.Test != nil, sink: sink}
}

type schedAttemptNoop struct{}

func (schedAttemptNoop) MarkFirstOutput(time.Time)   {}
func (schedAttemptNoop) FinishOK(*SchedAttemptUsage) {}
func (schedAttemptNoop) FinishFault(string)          {}
func (schedAttemptNoop) FinishTimeout(time.Duration) {}
func (schedAttemptNoop) FinishOther(string)          {}

type schedAttemptHandle struct {
	recorder *SchedAttemptRecorderService
	mu       sync.Mutex
	finished atomic.Bool
	row      schedquality.AttemptSample
	test     bool
	sink     func(schedquality.AttemptSample)
}

func (h *schedAttemptHandle) MarkFirstOutput(at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.finished.Load() && h.row.TTFOMs == nil {
		ms := int(at.Sub(h.row.StartedAt).Milliseconds())
		h.row.TTFOMs = &ms
	}
}

func (h *schedAttemptHandle) FinishOK(u *SchedAttemptUsage) {
	h.finish(schedquality.OutcomeOK, "", 0, u)
}
func (h *schedAttemptHandle) FinishFault(detail string) {
	h.finish(schedquality.OutcomeAccountFault, detail, 0, nil)
}
func (h *schedAttemptHandle) FinishTimeout(waited time.Duration) {
	h.finish(schedquality.OutcomeTimeout, "", waited, nil)
}
func (h *schedAttemptHandle) FinishOther(detail string) {
	h.finish(schedquality.OutcomeOther, detail, 0, nil)
}

func (h *schedAttemptHandle) finish(outcome schedquality.Outcome, detail string, waited time.Duration, u *SchedAttemptUsage) {
	if !h.finished.CompareAndSwap(false, true) {
		return
	}
	h.mu.Lock()
	row := h.row
	h.mu.Unlock()
	row.FinishedAt, row.Outcome = h.recorder.now(), outcome
	if r := []rune(detail); len(r) > 32 {
		detail = string(r[:32])
	}
	row.Detail = detail
	if outcome == schedquality.OutcomeTimeout {
		ms := int(waited.Milliseconds())
		row.CensoredMs = &ms
	}
	if u != nil && outcome == schedquality.OutcomeOK {
		prompt, cached := u.PromptTokens, u.CacheReadTokens
		row.PromptTokens, row.CacheReadTokens = &prompt, &cached
	} else {
		row.PrevPromptTokens = nil
	}
	if h.test {
		if h.sink != nil {
			h.sink(row)
		}
		return
	}
	s := h.recorder
	s.observerMu.RLock()
	observer := s.observer
	s.observerMu.RUnlock()
	if observer != nil {
		observer(row)
	}
	s.enqueue(row)
	if outcome == schedquality.OutcomeOK && row.SessionKey != "" {
		s.memory.put(schedAttemptSession{
			key: schedAttemptSessionKey{row.GroupID, row.SessionKey}, accountID: row.AccountID,
			model: row.Model, promptTokens: row.PromptTokens, finishedAt: row.FinishedAt,
		})
	}
}

func (s *SchedAttemptRecorderService) enqueue(row schedquality.AttemptSample) {
	s.queueMu.Lock()
	dropped := s.count == len(s.queue)
	if dropped {
		s.head = (s.head + 1) % len(s.queue)
		s.count--
	}
	s.queue[(s.head+s.count)%len(s.queue)] = row
	s.count++
	fullBatch := s.count >= 200
	s.queueMu.Unlock()
	if dropped {
		s.droppedQueueFull.Add(1)
		s.warnDrop("queue_full", &s.lastQueueWarning, nil)
	}
	if fullBatch {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

func (s *SchedAttemptRecorderService) Stats() SchedAttemptRecorderStats {
	s.queueMu.Lock()
	n := s.count
	s.queueMu.Unlock()
	return SchedAttemptRecorderStats{n, s.droppedQueueFull.Load(), s.droppedWriteFailed.Load()}
}

func (s *SchedAttemptRecorderService) warnDrop(reason string, last *atomic.Int64, err error) {
	at := s.now().UnixNano()
	prev := last.Load()
	if (prev != 0 && at-prev < int64(5*time.Second)) || !last.CompareAndSwap(prev, at) {
		return
	}
	stats := s.Stats()
	logger.L().With(zap.String("component", "service.sched_attempt_recorder"), zap.String("reason", reason),
		zap.Uint64("dropped_queue_full", stats.DroppedQueueFull), zap.Uint64("dropped_write_failed", stats.DroppedWriteFailed),
		zap.Error(err)).Warn("sched_attempt.dropped")
}

func (s *SchedAttemptRecorderService) Flush(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	started := s.started
	s.lifecycleMu.Unlock()
	if !started {
		return errors.New("sched attempt recorder is not started")
	}
	req := schedAttemptFlush{ctx: ctx, done: make(chan error, 1)}
	select {
	case s.flush <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return nil
	}
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *SchedAttemptRecorderService) takeBatch() []schedquality.AttemptSample {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	n := min(s.count, 200)
	batch := make([]schedquality.AttemptSample, n)
	for i := range batch {
		batch[i] = s.queue[s.head]
		s.queue[s.head] = schedquality.AttemptSample{}
		s.head = (s.head + 1) % len(s.queue)
	}
	s.count -= n
	return batch
}

func (s *SchedAttemptRecorderService) run(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var pending schedAttemptPending
	for {
		select {
		case <-ctx.Done():
			s.stopped.Store(true)
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.flushPending(shutdown, &pending, ticker.C)
			cancel()
			return
		case <-ticker.C:
			_ = s.drain(ctx, &pending)
		case <-s.wake:
			if pending.failures == 0 {
				_ = s.drain(ctx, &pending)
			}
		case req := <-s.flush:
			flushCtx, cancel := context.WithCancel(req.ctx)
			stop := context.AfterFunc(ctx, cancel)
			err := s.flushPending(flushCtx, &pending, ticker.C)
			stop()
			cancel()
			req.done <- err
		}
	}
}

func (s *SchedAttemptRecorderService) flushPending(ctx context.Context, pending *schedAttemptPending, ticks <-chan time.Time) error {
	var result error
	for {
		if pending.failures > 0 {
			select {
			case <-ticks:
			case <-ctx.Done():
				return errors.Join(result, ctx.Err())
			}
		}
		result = errors.Join(result, s.drain(ctx, pending))
		if len(pending.rows) == 0 || ctx.Err() != nil {
			return result
		}
	}
}

func (s *SchedAttemptRecorderService) drain(ctx context.Context, pending *schedAttemptPending) error {
	remaining := s.Stats().QueueDepth
	var result error
	for len(pending.rows) > 0 || remaining > 0 {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if len(pending.rows) == 0 {
			pending.rows = s.takeBatch()
			if len(pending.rows) == 0 {
				break
			}
			remaining -= len(pending.rows)
		}
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.store.InsertBatch(writeCtx, pending.rows)
		cancel()
		if ctx.Err() != nil && err != nil {
			return errors.Join(result, ctx.Err())
		}
		if err != nil {
			pending.failures++
			if pending.failures <= 3 {
				return result
			}
			s.droppedWriteFailed.Add(uint64(len(pending.rows)))
			s.warnDrop("write_failed", &s.lastWriteWarning, err)
			result = errors.Join(result, err)
		}
		*pending = schedAttemptPending{}
	}
	return result
}
