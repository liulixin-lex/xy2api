package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

type schedQualitySwitches interface {
	SchedQualityEnabled(ctx context.Context) bool
}

type SchedQualityEngineService struct {
	store       SchedQualityStore
	attempts    SchedAttemptStore
	switches    schedQualitySwitches
	now         func() time.Time
	mu          sync.Mutex
	window      schedQualityWindow
	snapshot    atomic.Pointer[SchedQualitySnapshot]
	processedAt time.Time
	loaded      bool
	insertedAt  time.Time
	configAt    time.Time
	policies    map[int64]SchedPolicyRecord
	members     map[int64][]int64
	seenResets  map[int64]time.Time
	callbackMu  sync.Mutex
	callbacks   []func(int64, time.Time)
	lifecycle   sync.Mutex
	stop        chan struct{}
	done        chan struct{}
	lockCache   LeaderLockCache
	db          *sql.DB
	instanceID  string
	obs         SchedObs
}

const schedQualityLedgerPageSize = 5000

var _ SchedQualityEngine = (*SchedQualityEngineService)(nil)

func NewSchedQualityEngineService(store SchedQualityStore, attempts SchedAttemptStore, _ SchedTestRunner, switches schedQualitySwitches, now func() time.Time, obs SchedObs) *SchedQualityEngineService {
	if now == nil {
		now = time.Now
	}
	s := &SchedQualityEngineService{store: store, attempts: attempts, switches: switches, now: now, seenResets: make(map[int64]time.Time), instanceID: uuid.NewString(), obs: schedObsOrNop(obs)}
	s.snapshot.Store(&SchedQualitySnapshot{View: &schedQualityView{}})
	return s
}

func (s *SchedQualityEngineService) SetLeaderLock(cache LeaderLockCache, db *sql.DB) {
	s.lockCache, s.db = cache, db
}

func (s *SchedQualityEngineService) Start(ctx context.Context) {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.done != nil {
		return
	}
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	// Recover synchronously so the first reader after Start sees restored state.
	if err := s.cycle(ctx); err != nil {
		slog.ErrorContext(ctx, "sched_quality.cycle", "error", err)
	}
	go func() {
		defer close(s.done)
		eval, cleanup := time.NewTicker(schedquality.EvalInterval), time.NewTicker(schedquality.CleanupInterval)
		defer eval.Stop()
		defer cleanup.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-eval.C:
				if err := s.cycle(ctx); err != nil {
					slog.ErrorContext(ctx, "sched_quality.cycle", "error", err)
				}
			case <-cleanup.C:
				s.cleanup(ctx)
			}
		}
	}()
}

func (s *SchedQualityEngineService) Stop() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.done == nil {
		return
	}
	close(s.stop)
	<-s.done
	s.done, s.stop = nil, nil
}

func (s *SchedQualityEngineService) Observe(sample schedquality.AttemptSample) { s.window.add(sample) }
func (s *SchedQualityEngineService) Snapshot() *SchedQualitySnapshot           { return s.snapshot.Load() }
func (s *SchedQualityEngineService) OnAccountReset(fn func(int64, time.Time)) {
	if fn == nil {
		return
	}
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	s.callbacks = append(s.callbacks, fn)
}

func (s *SchedQualityEngineService) notify(resets map[int64]time.Time) {
	s.callbackMu.Lock()
	callbacks := append([]func(int64, time.Time){}, s.callbacks...)
	s.callbackMu.Unlock()
	ids := make([]int64, 0, len(resets))
	for id := range resets {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		for _, fn := range callbacks {
			fn(id, resets[id])
		}
	}
}

type schedEvidenceLoader interface {
	schedPreviousLoader
	LoadFinishedSince(context.Context, time.Time) ([]schedquality.AttemptSample, error)
}

func (s *SchedQualityEngineService) loadEvidence(ctx context.Context, now time.Time) error {
	loader, ok := s.attempts.(schedEvidenceLoader)
	if !ok {
		return errors.New("attempt store does not implement v2 evidence reads")
	}
	cursor := s.insertedAt
	if !s.loaded {
		rows, err := loader.LoadFinishedSince(ctx, now.Add(-schedEvidenceRetention))
		if err != nil {
			return err
		}
		cursor = now.Add(-schedEvidenceRetention)
		s.window.merge(rows, now)
		for _, row := range rows {
			if row.InsertedAt.After(cursor) {
				cursor = row.InsertedAt
			}
		}
	}
	maxSeen := cursor
	after := cursor.Add(-schedquality.LedgerReloadOverlap)
	for {
		rows, latest, err := s.attempts.LoadInsertedAfter(ctx, after, schedQualityLedgerPageSize)
		if err != nil {
			return err
		}
		s.window.merge(rows, now)
		if len(rows) == 0 {
			break
		}
		if latest.After(maxSeen) {
			maxSeen = latest
		}
		if len(rows) < schedQualityLedgerPageSize {
			break
		}
		if !latest.After(after) {
			return errors.New("attempt ledger cursor did not advance")
		}
		after = latest
	}
	if err := s.window.prepareContinuations(ctx, loader); err != nil {
		return err
	}
	s.insertedAt, s.loaded = maxSeen, true
	return nil
}

func (s *SchedQualityEngineService) inputs(ctx context.Context, now time.Time, samples map[SchedAccountModel][]schedquality.AttemptSample) (*schedQualityData, error) {
	if err := s.refreshConfig(ctx, now); err != nil {
		return nil, err
	}
	d := &schedQualityData{now: now, samples: samples, globalEnabled: true}
	if s.switches != nil {
		d.globalEnabled = s.switches.SchedQualityEnabled(ctx)
	}
	return d, nil
}

func (s *SchedQualityEngineService) cycle(ctx context.Context) (cycleErr error) {
	started := s.now()
	defer func() { s.obs.Cycle(s.now().Sub(started), cycleErr != nil) }()
	s.mu.Lock()
	var notifications map[int64]time.Time
	defer func() { s.mu.Unlock(); s.notify(notifications) }()
	now := s.now()
	s.window.take(now)
	if err := s.loadEvidence(ctx, now); err != nil {
		return err
	}
	d, err := s.inputs(ctx, now, s.window.samples)
	if err != nil {
		return err
	}
	var states []schedquality.StateRow
	var overrides []SchedOverride
	var resets map[int64]time.Time
	var decisions []schedquality.Decision
	horizon := now.Add(-10 * time.Second)
	after := s.processedAt
	if after.IsZero() {
		after = now.Add(-5 * time.Minute)
	} else if late := s.window.newFinishedAt; !late.IsZero() && !late.After(after) {
		after = late.Add(-time.Microsecond)
		if floor := now.Add(-5 * time.Minute); after.Before(floor) {
			after = floor
		}
	}
	locked, err := s.store.RunCycle(ctx, func(tx SchedQualityTx) error {
		var err error
		states, err = tx.States()
		if err != nil {
			return err
		}
		overrides, err = tx.Overrides()
		if err != nil {
			return err
		}
		resets, err = tx.ResetTimes()
		if err != nil {
			return err
		}
		var changed, deleted []schedquality.StateRow
		states, changed, deleted, decisions = s.replay(d, states, resets, after, horizon)
		if err := tx.Upsert(changed); err != nil {
			return err
		}
		if err := tx.Delete(deleted); err != nil {
			return err
		}
		if err := tx.InsertDecisions(decisions); err != nil {
			return err
		}
		var expired, active []SchedOverride
		for _, o := range overrides {
			if (o.ExpiresAt != nil && !o.ExpiresAt.After(now)) || o.GroupID != 0 && !s.isMember(o.GroupID, o.AccountID) {
				expired = append(expired, o)
			} else {
				active = append(active, o)
			}
		}
		if err := tx.DeleteOverrides(expired); err != nil {
			return err
		}
		overrides = active
		return nil
	})
	if err != nil {
		return err
	}
	if !locked {
		s.processedAt = time.Time{}
		states, overrides, resets, err = s.store.LoadRows(ctx)
		if err != nil {
			return err
		}
	} else {
		s.processedAt = horizon
		for _, decision := range decisions {
			logSchedDecision(ctx, decision)
			if decision.Action == "enter" || decision.Action == "renew" {
				s.obs.Count("cooled", 1)
			} else {
				s.obs.Count("released", 1)
			}
		}
	}
	s.window.newFinishedAt = time.Time{}
	notifications = s.publish(d, states, overrides, resets, now)
	return nil
}

// replay advances chronological event instants, not the polling clock. Persisted
// Since/EvidenceSince skip events already covered by a previous leader or reset.
func (s *SchedQualityEngineService) replay(d *schedQualityData, states []schedquality.StateRow, resets map[int64]time.Time, after, horizon time.Time) (kept, changed, deleted []schedquality.StateRow, decisions []schedquality.Decision) {
	keys, rows := s.rowKeys(d, states)
	initial := make(map[schedQualityKey]schedquality.StateRow)
	for _, row := range states {
		initial[schedQualityKeyOf(row)] = row
	}
	enabled := keys[:0]
	for _, key := range keys {
		if !s.policies[key.group].Policy.Enabled {
			delete(rows, key)
			continue
		}
		enabled = append(enabled, key)
		if _, ok := rows[key]; !ok {
			rows[key] = schedquality.StateRow{AccountID: key.account, Model: key.model, GroupID: key.group}
		}
	}
	keys = enabled
	events := make(map[time.Time]map[SchedAccountModel]bool)
	add := func(at time.Time) {
		if events[at] == nil {
			events[at] = make(map[SchedAccountModel]bool)
		}
	}
	for at := after.Truncate(time.Minute).Add(time.Minute); !at.After(horizon); at = at.Add(time.Minute) {
		add(at)
	}
	for pair, samples := range d.samples {
		for _, sample := range samples {
			if sample.FinishedAt.After(after) && !sample.FinishedAt.After(horizon) && sample.Stream && (sample.TTFOMs != nil || sample.Outcome == schedquality.OutcomeTimeout) {
				add(sample.FinishedAt)
				events[sample.FinishedAt][pair] = true
			}
		}
	}
	for _, key := range keys {
		r := rows[key]
		if r.State == schedquality.StateCooling && !r.Until.After(horizon) {
			add(r.Until)
		}
	}
	transition := func(key schedQualityKey, at time.Time, kind schedquality.EvaluationKind) {
		prev := rows[key]
		if at.Before(prev.Since) || !at.After(resets[key.account]) || at.Before(prev.EvidenceSince) {
			return
		}
		prev.EvidenceSince = schedQualitySince(prev.EvidenceSince, resets[key.account])
		next, _ := schedquality.AdvanceAt(prev, s.policies[key.group].Policy, d.samples[SchedAccountModel{key.account, key.model}], at, kind)
		rows[key] = next
		action := ""
		if prev.State != next.State {
			if next.State == schedquality.StateCooling {
				action = "enter"
			} else {
				action = "release"
			}
		} else if next.State == schedquality.StateCooling && !next.Since.Equal(prev.Since) {
			action = "renew"
		}
		if action == "" {
			return
		}
		basis := schedquality.DecisionBasis{RuleVersion: schedquality.RuleVersion}
		if next.Basis != nil {
			basis = *next.Basis
		}
		decisions = append(decisions, schedquality.Decision{At: at, Scope: schedquality.Scope{AccountID: key.account, Model: key.model, GroupID: key.group}, Action: action, Level: next.Level, Until: next.Until, Basis: basis})
		if next.State == schedquality.StateCooling && !next.Until.After(horizon) {
			add(next.Until)
		}
	}
	for len(events) > 0 {
		var at time.Time
		for t := range events {
			if at.IsZero() || t.Before(at) {
				at = t
			}
		}
		fast := events[at]
		delete(events, at)
		for _, key := range keys {
			r := rows[key]
			if r.State == schedquality.StateCooling && !r.Until.After(at) {
				transition(key, at, schedquality.EvaluateDeadline)
			}
		}
		if at.After(after) && at.Equal(at.Truncate(time.Minute)) {
			for _, key := range keys {
				if rows[key].State == schedquality.StateNormal {
					transition(key, at, schedquality.EvaluateRegular)
				}
			}
		}
		for _, key := range keys {
			if fast[SchedAccountModel{key.account, key.model}] && rows[key].State == schedquality.StateNormal {
				transition(key, at, schedquality.EvaluateFast)
			}
		}
	}
	active := make(map[schedQualityKey]bool)
	for _, key := range keys {
		row := rows[key]
		active[key] = true
		if row.Since.IsZero() {
			continue
		}
		kept = append(kept, row)
		if !reflect.DeepEqual(row, initial[key]) {
			changed = append(changed, row)
		}
	}
	for key, row := range initial {
		if !active[key] {
			deleted = append(deleted, row)
		}
	}
	sort.Slice(deleted, func(i, j int) bool {
		return schedQualityLess(schedQualityKeyOf(deleted[i]), schedQualityKeyOf(deleted[j]))
	})
	return
}

func (s *SchedQualityEngineService) rebuild(ctx context.Context) (map[int64]time.Time, error) {
	now := s.now()
	d, err := s.inputs(ctx, now, s.window.samples)
	if err != nil {
		return nil, err
	}
	states, overrides, resets, err := s.store.LoadRows(ctx)
	if err != nil {
		return nil, err
	}
	return s.publish(d, states, overrides, resets, s.snapshot.Load().At), nil
}
func (s *SchedQualityEngineService) Reset(ctx context.Context, id int64) (time.Time, error) {
	s.mu.Lock()
	var notifications map[int64]time.Time
	defer func() { s.mu.Unlock(); s.notify(notifications) }()
	at, err := s.store.RunReset(ctx, id)
	if err != nil {
		return time.Time{}, err
	}
	notifications, err = s.rebuild(ctx)
	return at, err
}

func (s *SchedQualityEngineService) SetOverride(ctx context.Context, o SchedOverride) error {
	if o.AccountID <= 0 || o.GroupID < 0 || o.Mode != schedquality.OverrideExempt {
		return infraerrors.BadRequest("INVALID_SCHED_OVERRIDE", "invalid quality override")
	}
	s.mu.Lock()
	var notifications map[int64]time.Time
	defer func() { s.mu.Unlock(); s.notify(notifications) }()
	if err := s.refreshConfig(ctx, s.now()); err != nil {
		return err
	}
	if o.GroupID != 0 && !s.isMember(o.GroupID, o.AccountID) {
		return infraerrors.BadRequest("INVALID_SCHED_GROUP", "account does not belong to this OpenAI group")
	}
	if err := s.store.UpsertOverride(ctx, o); err != nil {
		return err
	}
	var err error
	notifications, err = s.rebuild(ctx)
	return err
}

func (s *SchedQualityEngineService) ClearOverride(ctx context.Context, a, g int64) error {
	s.mu.Lock()
	var notifications map[int64]time.Time
	defer func() { s.mu.Unlock(); s.notify(notifications) }()
	if err := s.store.DeleteOverride(ctx, a, g); err != nil {
		return err
	}
	var err error
	notifications, err = s.rebuild(ctx)
	return err
}

func (s *SchedQualityEngineService) cleanup(ctx context.Context) {
	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, "sched_quality:ledger_cleanup", s.instanceID, 10*time.Minute)
	if !ok {
		return
	}
	defer release()
	started := s.now()
	rows, err := s.attempts.DeleteBefore(ctx, started.Add(-schedquality.LedgerRetention))
	attrs := []any{"component", "sched_quality", "kind", "ledger", "rows", rows, "duration_ms", s.now().Sub(started).Milliseconds()}
	if err != nil {
		slog.ErrorContext(ctx, "sched_quality.cleanup", append(attrs, "error", err)...)
		return
	}
	slog.InfoContext(ctx, "sched_quality.cleanup", attrs...)
	decisions, err := s.store.DeleteDecisionsBefore(ctx, started.Add(-30*24*time.Hour))
	if err != nil {
		slog.ErrorContext(ctx, "sched_quality.cleanup", "kind", "decisions", "error", err)
	} else {
		slog.InfoContext(ctx, "sched_quality.cleanup", "kind", "decisions", "rows", decisions)
	}
}
