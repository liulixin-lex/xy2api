package resilience

import "time"

// PreludeTraceSnapshot describes the first release in the current attempt.
// A zero delay also represents content that arrived without a created event.
type PreludeTraceSnapshot struct {
	ReleaseEvent          string
	ReleaseAfterCreatedMS int64
}

// NotePreludeCreated starts an attempt's trace. A zero time resets it before
// reading any upstream data, including attempts that never send created.
func (s *RequestState) NotePreludeCreated(at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prelude = preludeTrace{createdAt: at}
}

// NotePreludeReleased freezes the event that releases the opening sequence.
// Actual first-output time and delivered bytes are tracked by the delivery writer.
func (s *RequestState) NotePreludeReleased(eventType string, at time.Time) {
	if s == nil || eventType == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prelude.releaseEvent != "" {
		return
	}
	s.prelude.releaseEvent = eventType
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
	return PreludeTraceSnapshot{ReleaseEvent: s.prelude.releaseEvent, ReleaseAfterCreatedMS: s.prelude.releaseAfterCreatedMS}
}
