package service

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type schedObsWindow struct {
	cycles       int64
	cyclesFailed int64
	cycleMSMax   int64
	tests        int64
	testsAborted int64
	cooled       int64
	released     int64
	slowWarned   bool
}

// SchedObservabilityService emits one summary per instance every five minutes.
type SchedObservabilityService struct {
	mu     sync.Mutex
	window schedObsWindow

	lifecycle sync.Mutex
	stop      chan struct{}
	done      chan struct{}
	newTicker func(time.Duration) (<-chan time.Time, func())
}

var _ SchedObs = (*SchedObservabilityService)(nil)

func NewSchedObservabilityService() *SchedObservabilityService {
	return &SchedObservabilityService{newTicker: func(d time.Duration) (<-chan time.Time, func()) {
		ticker := time.NewTicker(d)
		return ticker.C, ticker.Stop
	}}
}

func (s *SchedObservabilityService) Count(name string, n int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch name {
	case "tests":
		s.window.tests += n
	case "tests_aborted":
		s.window.testsAborted += n
	case "cooled":
		s.window.cooled += n
	case "released":
		s.window.released += n
	}
}

func (s *SchedObservabilityService) Cycle(d time.Duration, failed bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.window.cycles++
	if failed {
		s.window.cyclesFailed++
	}
	if ms := d.Milliseconds(); ms > s.window.cycleMSMax {
		s.window.cycleMSMax = ms
	}
	if d > 5*time.Second && !s.window.slowWarned {
		s.window.slowWarned = true
		slog.Warn("sched_quality.cycle", "component", "sched_quality", "duration_ms", d.Milliseconds())
	}
}

func (s *SchedObservabilityService) summary(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.window
	slog.InfoContext(ctx, "sched_quality.summary", "component", "sched_quality",
		"cycles", w.cycles, "cycles_failed", w.cyclesFailed, "cycle_ms_max", w.cycleMSMax,
		"tests", w.tests, "tests_aborted", w.testsAborted, "cooled", w.cooled, "released", w.released)
	s.window = schedObsWindow{}
}

func (s *SchedObservabilityService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.done != nil {
		return
	}
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	ticks, stopTicker := s.newTicker(5 * time.Minute)
	go func() {
		defer close(s.done)
		defer stopTicker()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-ticks:
				s.summary(ctx)
			}
		}
	}()
}

func (s *SchedObservabilityService) Stop() {
	if s == nil {
		return
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.done == nil {
		return
	}
	close(s.stop)
	<-s.done
	s.done, s.stop = nil, nil
}
