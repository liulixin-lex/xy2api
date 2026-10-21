package resilience

import (
	"sync"
	"testing"
	"time"
)

func TestPreludeTraceTracksOneAttempt(t *testing.T) {
	s := NewRequestState()
	created := time.Unix(100, 0)
	s.NotePreludeCreated(created)
	s.NotePreludeReleased("response.output_text.delta", created.Add(17*time.Millisecond))
	s.NotePreludeReleased("response.completed", created.Add(time.Second))
	got := s.PreludeTrace()
	if got.ReleaseEvent != "response.output_text.delta" || got.ReleaseAfterCreatedMS != 17 {
		t.Fatalf("first release must freeze: %+v", got)
	}
	s.NotePreludeCreated(time.Time{})
	if got = s.PreludeTrace(); got.ReleaseEvent != "" || got.ReleaseAfterCreatedMS != 0 {
		t.Fatalf("new attempt leaks old release: %+v", got)
	}
	s.NotePreludeReleased("response.function_call_arguments.delta", created)
	if got = s.PreludeTrace(); got.ReleaseAfterCreatedMS != 0 {
		t.Fatalf("no created has no latency: %+v", got)
	}
	s.NotePreludeCreated(created)
	s.NotePreludeReleased("response.completed", created.Add(-time.Second))
	if got = s.PreludeTrace(); got.ReleaseAfterCreatedMS != 0 {
		t.Fatalf("negative delay: %+v", got)
	}
}

func TestPreludeTraceConcurrent(t *testing.T) {
	s := NewRequestState()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				s.NotePreludeCreated(time.Now())
				s.NotePreludeReleased("response.completed", time.Now())
				_ = s.PreludeTrace()
			}
		})
	}
	wg.Wait()
	var absent *RequestState
	absent.NotePreludeCreated(time.Now())
	absent.NotePreludeReleased("response.completed", time.Now())
	_ = absent.PreludeTrace()
}
