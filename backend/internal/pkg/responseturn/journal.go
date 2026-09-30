package responseturn

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// Attachment is a single downstream subscription. Only WriteNext fences the
// actual writer against replacement; a caller using Next must provide its own
// equivalent write fence. Close from an old epoch cannot detach a newer writer.
type Attachment struct {
	turn          *Turn
	Epoch         uint64
	replay        []*Event
	replayAt      int
	live          chan *Event
	done          chan struct{}
	closed        bool
	retainedBytes int64
	nextMu        sync.Mutex
}

// Attach captures history and installs its live subscription under the same
// lock. startingAfter is an original upstream sequence, never a local index.
// replace is for explicit native attachment only; duplicate create uses false.
func (t *Turn) Attach(scope Scope, startingAfter *int64, replace bool) (*Attachment, error) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	m := t.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked(m.cfg.Now())
	if m.closed {
		return nil, ErrClosed
	}
	if t.scope != scope {
		return nil, ErrNotFound
	}
	if t.expired {
		return nil, ErrExpired
	}
	if startingAfter != nil && *startingAfter < 0 {
		return nil, ErrCursor
	}
	if t.epoch > 0 || startingAfter != nil {
		if !t.recoverable {
			if t.unavailable == "recovery_quota_exceeded" || t.unavailable == "result_expired" {
				return nil, ErrCursorExpired
			}
			return nil, ErrUnavailable
		}
		if t.owner <= 0 || t.responseID == "" {
			return nil, ErrUnavailable
		}
	}
	if t.attachment != nil && !replace {
		return nil, ErrActive
	}
	var after uint64
	if startingAfter != nil {
		found := false
		for _, e := range t.events {
			if e.NativeSequence != nil && *e.NativeSequence == *startingAfter {
				after = e.InternalSequence
				found = true
				break
			}
		}
		if !found {
			return nil, ErrCursor
		}
	}
	replay := make([]*Event, 0, len(t.events))
	for _, e := range t.events {
		if e.InternalSequence > after {
			replay = append(replay, e)
		}
	}
	if t.attachment != nil {
		t.attachment.closeLocked()
	}
	if !t.detachedAt.IsZero() {
		t.offlineUsed = t.offlineUsedLocked(m.cfg.Now())
		t.detachedAt = time.Time{}
	}
	t.epoch++
	a := &Attachment{turn: t, Epoch: t.epoch, replay: replay, live: make(chan *Event, 1), done: make(chan struct{})}
	t.attachment = a
	if !t.isTerminalLocked() {
		t.state = StateRunning
		t.reason = ""
	}
	return a, nil
}

func (a *Attachment) closeLocked() {
	if !a.closed {
		a.closed = true
		close(a.done)
		if a.retainedBytes > 0 {
			a.turn.journalBytes -= a.retainedBytes
			a.turn.addBytesLocked(-a.retainedBytes)
			a.retainedBytes = 0
		}
		a.replay = nil
		a.replayAt = 0
		a.drainLiveLocked()
	}
}
func (a *Attachment) drainLiveLocked() {
	for {
		select {
		case <-a.live:
		default:
			return
		}
	}
}
func (a *Attachment) Close() { a.Detach(ReasonClientDetached) }
func (a *Attachment) Detach(reason Reason) {
	t := a.turn
	m := t.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	a.closeLocked()
	if t.attachment != a || t.epoch != a.Epoch {
		return
	}
	t.attachment = nil
	if t.isTerminalLocked() {
		return
	}
	if reason == "" {
		reason = ReasonClientDetached
	}
	if reason != ReasonClientDetached {
		t.cancelLocked(reason, m.cfg.Now())
		return
	}
	// A non-stream background execution is not governed by stream detachment.
	if !t.stream {
		return
	}
	if !t.recoverable {
		t.cancelLocked(ReasonClientDetached, m.cfg.Now())
		return
	}
	t.detachedAt = m.cfg.Now()
	t.state = StateDetached
	t.reason = ReasonClientDetached
}

func (a *Attachment) next(ctx context.Context) (Event, error) {
	for {
		t := a.turn
		m := t.manager
		m.mu.Lock()
		if a.closed || t.attachment != a || t.epoch != a.Epoch {
			m.mu.Unlock()
			return Event{}, ErrAttachmentReplaced
		}
		if a.replayAt < len(a.replay) {
			e := a.replay[a.replayAt]
			if a.retainedBytes > 0 {
				cost := e.cost()
				a.retainedBytes -= cost
				t.journalBytes -= cost
				t.addBytesLocked(-cost)
			}
			a.replay[a.replayAt] = nil
			a.replayAt++
			t.replayEvents++
			m.mu.Unlock()
			cloned := e.clone()
			cloned.Replay = true
			return cloned, nil
		}
		// Release replay references as soon as the history/live boundary is crossed.
		a.replay = nil
		terminal := t.isTerminalLocked()
		m.mu.Unlock()
		select {
		case e := <-a.live:
			return e.clone(), nil
		default:
		}
		if terminal {
			return Event{}, io.EOF
		}
		select {
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-a.done:
			return Event{}, ErrAttachmentReplaced
		case e := <-a.live:
			return e.clone(), nil
		case <-t.terminal:
			// A terminal event is enqueued before terminal is closed. Drain it before EOF.
		}
	}
}
func (a *Attachment) Next(ctx context.Context) (Event, error) {
	a.nextMu.Lock()
	defer a.nextMu.Unlock()
	return a.next(ctx)
}

// WriteNext keeps the sole downstream write serialized with Attach replacement.
// The callback must set a bounded transport write deadline; no manager lock is
// held while it writes or Flushes. Heartbeat writes should use Write as well.
func (a *Attachment) WriteNext(ctx context.Context, write func(Event) error) error {
	a.nextMu.Lock()
	defer a.nextMu.Unlock()
	e, err := a.next(ctx)
	if err != nil {
		return err
	}
	return a.Write(func() error { return write(e) })
}
func (a *Attachment) Write(write func() error) error {
	t := a.turn
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	m := t.manager
	m.mu.Lock()
	active := !a.closed && t.attachment == a && t.epoch == a.Epoch
	m.mu.Unlock()
	if !active {
		return ErrAttachmentReplaced
	}
	return write()
}

// Publish journals a complete event before exposing it. It applies bounded
// backpressure (one live event), never retries generation, and never waits for
// semantic text. A full recovery journal disables recovery for an attached
// client but does not impose the recovery single-event limit on its live stream.
func (t *Turn) Publish(ctx context.Context, event Event) (PublishResult, error) {
	t.publishMu.Lock()
	defer t.publishMu.Unlock()
	e := event.clone()
	m := t.manager
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return PublishResult{}, ErrClosed
	}
	if t.isTerminalLocked() {
		m.mu.Unlock()
		return PublishResult{}, ErrTerminal
	}
	if err := t.ctx.Err(); err != nil {
		t.cancelLocked(CancellationReason(context.Cause(t.ctx)), m.cfg.Now())
		m.mu.Unlock()
		return PublishResult{}, err
	}
	if e.NativeSequence != nil && *e.NativeSequence < 0 {
		t.failConsistencyLocked()
		m.mu.Unlock()
		return PublishResult{}, ErrConsistency
	}
	if e.ResponseID != "" {
		if t.responseID != "" && e.ResponseID != t.responseID {
			t.failConsistencyLocked()
			m.mu.Unlock()
			return PublishResult{}, ErrConsistency
		}
		key := responseID{t.scope, e.ResponseID}
		if old := m.responses[key]; old != nil && old != t {
			t.failConsistencyLocked()
			m.mu.Unlock()
			return PublishResult{}, ErrConsistency
		}
		t.responseID = e.ResponseID
		m.responses[key] = t
	}
	fingerprint := eventFingerprint(e)
	keys := []string{}
	if e.Identity != "" {
		keys = append(keys, "id:"+e.Identity)
	}
	if e.NativeSequence != nil {
		keys = append(keys, fmt.Sprintf("seq:%d", *e.NativeSequence))
	}
	duplicate := false
	for _, key := range keys {
		if old, ok := t.identities[key]; ok {
			if old != fingerprint {
				t.failConsistencyLocked()
				m.mu.Unlock()
				return PublishResult{}, ErrConsistency
			}
			duplicate = true
		}
	}
	if duplicate {
		r := t.publishResultLocked()
		r.Duplicate = true
		m.mu.Unlock()
		return r, nil
	}
	if e.NativeSequence != nil && t.hasNative && *e.NativeSequence <= t.lastNative {
		t.failConsistencyLocked()
		m.mu.Unlock()
		return PublishResult{}, ErrConsistency
	}
	if e.NativeSequence != nil {
		t.lastNative = *e.NativeSequence
		t.hasNative = true
	}
	if !e.Local && t.firstEventAt.IsZero() {
		t.firstEventAt = m.cfg.Now()
	}
	if e.Content && t.firstContentAt.IsZero() {
		t.firstContentAt = m.cfg.Now()
	}
	if !e.Local {
		t.sequence++
	}
	e.InternalSequence = t.sequence
	a := t.attachment
	if t.recoverable && !e.Local {
		if int64(len(e.Data)) > m.cfg.MaxEventBytes || !t.fitsLocked(e.cost()) {
			t.recoverable = false
			t.unavailable = "recovery_quota_exceeded"
			t.degradeJournalLocked(a)
			if a == nil {
				t.cancelLocked(ReasonQuota, m.cfg.Now())
				m.mu.Unlock()
				return t.publishResult(), &Cancellation{ReasonQuota}
			}
		} else {
			t.events = append(t.events, &e)
			t.journalBytes += e.cost()
			t.addBytesLocked(e.cost())
			for _, key := range keys {
				t.identities[key] = fingerprint
			}
		}
	}
	if e.terminal() && len(e.Result) > 0 && t.store {
		if !t.reserveResultLocked(e.Result) {
			t.recoverable = false
			t.unavailable = "recovery_quota_exceeded"
		}
	}
	result := t.publishResultLocked()
	m.mu.Unlock()
	var sendErr error
	if a != nil {
		select {
		case a.live <- &e:
		case <-a.done:
			// A replacement replays this already journaled event. An offline eligible
			// turn keeps it; neither path emits a new upstream create.
		case <-ctx.Done():
			sendErr = ctx.Err()
		case <-t.ctx.Done():
			sendErr = context.Cause(t.ctx)
		}
	}
	m.mu.Lock()
	if a != nil && a.closed {
		a.drainLiveLocked()
	}
	if e.terminal() && !t.isTerminalLocked() {
		t.finishLocked(e.Terminal, "", m.cfg.Now())
	}
	m.mu.Unlock()
	return result, sendErr
}
func (t *Turn) failConsistencyLocked() {
	t.recoverable = false
	t.unavailable = "event_consistency_error"
	t.finishLocked(StateFailed, ReasonConsistency, t.manager.cfg.Now())
}
func (t *Turn) publishResultLocked() PublishResult {
	return PublishResult{Recoverable: t.recoverable, RecoveryUnavailableReason: t.unavailable}
}
func (t *Turn) publishResult() PublishResult {
	m := t.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	return t.publishResultLocked()
}
