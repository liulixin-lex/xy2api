package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type SchedPerfLedger interface {
	LoadPerfConfig(context.Context) (map[int64]SchedPolicyRecord, map[int64]time.Time, error)
	LoadPerfPage(ctx context.Context, from, at time.Time, after SchedAttemptCursor, inserted bool, limit int) ([]SchedPerfFact, SchedAttemptCursor, bool, error)
	LoadPerfSwitches(ctx context.Context, from, at time.Time, afterID int64, limit int) ([]SchedPerfSwitch, error)
}

type schedPerfStore struct {
	facts                    map[string]SchedPerfFact
	switches                 map[int64]SchedPerfSwitch
	boot, inserted, switched time.Time
	finishedCursor           SchedAttemptCursor
	loaded                   bool
}

func (s *SchedQualityEngineService) PerformanceSnapshot() *SchedPerfSnapshot {
	return s.performance.Load()
}

// The store belongs to one background worker. No short-window lock or route
// reader touches the mutable ledger or either of its independent watermarks.
func (s *SchedQualityEngineService) runPerformance(ctx context.Context) {
	w := schedPerfStore{facts: make(map[string]SchedPerfFact), switches: make(map[int64]SchedPerfSwitch)}
	for ctx.Err() == nil {
		at := s.now().UTC().Truncate(schedPerfTick)
		if p := s.PerformanceSnapshot(); p == nil || !p.complete && !p.at.Equal(at) {
			s.performance.Store(&SchedPerfSnapshot{at: at, from: at})
		}
		if err := s.publishPerformance(ctx, &w, at); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "sched_quality.performance_load", "error", err)
		}
		now := s.now()
		delay := now.Truncate(schedPerfTick).Add(schedPerfTick).Sub(now)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *SchedQualityEngineService) publishPerformance(ctx context.Context, w *schedPerfStore, at time.Time) error {
	if err := w.load(ctx, s.attempts, at); err != nil {
		return err
	}
	policies, resets, err := s.attempts.LoadPerfConfig(ctx)
	if err != nil {
		return err
	}
	s.performance.Store(buildSchedPerf(w.facts, w.switches, policies, resets, at))
	return nil
}

func (w *schedPerfStore) load(ctx context.Context, ledger SchedPerfLedger, at time.Time) error {
	cut := at.Add(-schedPerfWindow)
	for id, f := range w.facts {
		if f.Finish < cut.UnixMicro() {
			delete(w.facts, id)
		}
	}
	for id, e := range w.switches {
		if e.At.Before(cut) {
			delete(w.switches, id)
		}
	}
	if w.boot.IsZero() {
		w.boot = at
		w.inserted = at
		w.switched = at.Add(-schedPerfWindow)
	}
	if !w.loaded {
		if err := w.pages(ctx, ledger, w.boot.Add(-schedPerfWindow), w.boot, &w.finishedCursor, false); err != nil {
			return err
		}
		w.loaded = true
	}
	from := w.inserted.Add(-q.LedgerReloadOverlap)
	var cursor SchedAttemptCursor
	if err := w.pages(ctx, ledger, from, at, &cursor, true); err != nil {
		return err
	}
	w.inserted = at
	from = w.switched.Add(-q.LedgerReloadOverlap)
	if from.Before(cut) {
		from = cut
	}
	var id int64
	for {
		events, err := ledger.LoadPerfSwitches(ctx, from, at, id, schedQualityLedgerPageSize)
		if err != nil {
			return err
		}
		for _, e := range events {
			w.switches[e.ID] = e
		}
		if len(events) < schedQualityLedgerPageSize {
			break
		}
		last := events[len(events)-1]
		if last.At.Before(from) || last.At.Equal(from) && last.ID <= id {
			return errors.New("performance switch cursor did not advance")
		}
		from, id = last.At, last.ID
	}
	w.switched = at
	return nil
}

func (w *schedPerfStore) pages(ctx context.Context, ledger SchedPerfLedger, from, at time.Time, cursor *SchedAttemptCursor, inserted bool) error {
	for {
		facts, next, done, err := ledger.LoadPerfPage(ctx, from, at, *cursor, inserted, schedQualityLedgerPageSize)
		if err != nil {
			return err
		}
		if !done && (next.At.Before(cursor.At) || next.At.Equal(cursor.At) && next.AttemptID <= cursor.AttemptID) {
			return errors.New("performance attempt cursor did not advance")
		}
		for _, f := range facts {
			w.facts[f.ID] = f
		}
		*cursor = next
		if done {
			return nil
		}
	}
}
