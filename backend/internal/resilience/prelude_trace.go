package resilience

import "time"

// PreludeReleaseSchema retains only the shape of the released event, item or part.
type PreludeReleaseSchema struct {
	EventKeys    string
	ItemType     string
	ItemKeys     string
	HasEncrypted bool
}

// PreludeTraceSnapshot describes the first release in the current attempt.
// A zero delay also represents content that arrived without a created event.
type PreludeTraceSnapshot struct {
	StagedAt              time.Time
	ReleaseEvent          string
	ReleaseAfterCreatedMS int64
	PreludeReleaseSchema
}

// NotePreludeCreated starts an attempt's trace. A zero time resets it before
// reading any upstream data, including attempts that never send created.
func (s *RequestState) NotePreludeCreated(at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.IsZero() {
		s.prelude = preludeTrace{}
		return
	}
	s.prelude = preludeTrace{createdAt: at, stagedAt: s.prelude.stagedAt}
}

// NotePreludeStaged marks an unknown metadata event held before first output.
func (s *RequestState) NotePreludeStaged(at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.prelude.stagedAt = at
	s.mu.Unlock()
}

// NotePreludeReleased freezes the event that releases the opening sequence.
// Actual first-output time and delivered bytes are tracked by the delivery writer.
func (s *RequestState) NotePreludeReleased(eventType string, at time.Time, schema PreludeReleaseSchema) {
	if s == nil || eventType == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prelude.releaseEvent != "" {
		return
	}
	s.prelude.releaseEvent = eventType
	s.prelude.schema = schema
	if !s.prelude.createdAt.IsZero() {
		s.prelude.releaseAfterCreatedMS = max(0, at.Sub(s.prelude.createdAt).Milliseconds())
	}
}

func (s *RequestState) PreludeTrace() PreludeTraceSnapshot {
	if s == nil {
		return PreludeTraceSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return PreludeTraceSnapshot{StagedAt: s.prelude.stagedAt, ReleaseEvent: s.prelude.releaseEvent, ReleaseAfterCreatedMS: s.prelude.releaseAfterCreatedMS, PreludeReleaseSchema: s.prelude.schema}
}
