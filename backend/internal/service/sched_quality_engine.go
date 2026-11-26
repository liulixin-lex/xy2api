package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
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
	store                  SchedQualityStore
	attempts               SchedAttemptStore
	switches               schedQualitySwitches
	now                    func() time.Time
	mu                     sync.Mutex
	window                 schedQualityWindow
	snapshot               atomic.Pointer[SchedQualitySnapshot]
	performance            atomic.Pointer[SchedPerfSnapshot]
	processedAt            time.Time
	loaded                 bool
	loadSince, loadThrough time.Time
	finishedCursor         SchedAttemptCursor
	finishedLoaded         bool
	loadRows, loadPages    int
	insertedAt             time.Time
	configAt               time.Time
	rule                   SchedTTFTRuleRecord
	leases                 SchedTrialLeases
	policies               map[int64]SchedPolicyRecord
	members                map[int64][]int64
	seenResets             map[int64]time.Time
	callbackMu             sync.Mutex
	callbacks              []func(int64, time.Time)
	lifecycle              sync.Mutex
	stop                   chan struct{}
	done                   chan struct{}
	lockCache              LeaderLockCache
	db                     *sql.DB
	instanceID             string
	obs                    SchedObs
}

const schedQualityLedgerPageSize = 5000

var _ SchedQualityEngine = (*SchedQualityEngineService)(nil)

func NewSchedQualityEngineService(store SchedQualityStore, attempts SchedAttemptStore, switches schedQualitySwitches, now func() time.Time, obs SchedObs) *SchedQualityEngineService {
	if now == nil {
		now = time.Now
	}
	s := &SchedQualityEngineService{store: store, attempts: attempts, switches: switches, now: now, seenResets: make(map[int64]time.Time), instanceID: uuid.NewString(), obs: schedObsOrNop(obs), rule: SchedTTFTRuleRecord{Params: schedquality.DefaultRuleBParams()}}
	at := now().UTC().Truncate(schedPerfTick)
	s.performance.Store(&SchedPerfSnapshot{at: at, from: at})
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
	perfCtx, cancel := context.WithCancel(ctx)
	perfDone := make(chan struct{})
	go func() { defer close(perfDone); s.runPerformance(perfCtx) }()
	go func() {
		defer close(s.done)
		defer func() { cancel(); <-perfDone }()
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

func (s *SchedQualityEngineService) loadEvidence(ctx context.Context, now time.Time) (err error) {
	boot := !s.loaded
	if boot && s.loadSince.IsZero() {
		s.loadSince = now.Add(-schedEvidenceRetention)
		s.loadThrough = now
		s.insertedAt = s.loadSince
	}
	if boot {
		defer func() {
			if err != nil {
				slog.WarnContext(ctx, "sched_quality.loading", "component", "sched_quality", "status", "incomplete", "layering", false, "rows", s.loadRows, "pages", s.loadPages, "error", err)
			}
		}()
	}
	for !s.finishedLoaded {
		if err = ctx.Err(); err != nil {
			return err
		}
		var rows []schedquality.AttemptSample
		rows, err = s.attempts.LoadFinishedPage(ctx, s.loadSince, s.loadThrough, s.finishedCursor, schedQualityLedgerPageSize)
		if err != nil {
			return err
		}
		s.window.merge(rows, now)
		s.loadRows += len(rows)
		s.loadPages++
		if len(rows) > 0 {
			last := rows[len(rows)-1]
			next := SchedAttemptCursor{At: last.FinishedAt, AttemptID: last.AttemptID}
			if !next.At.After(s.finishedCursor.At) && (!next.At.Equal(s.finishedCursor.At) || next.AttemptID <= s.finishedCursor.AttemptID) {
				return errors.New("finished evidence cursor did not advance")
			}
			s.finishedCursor = next
		}
		slog.InfoContext(ctx, "sched_quality.loading", "component", "sched_quality", "status", "page", "layering", false, "rows", s.loadRows, "pages", s.loadPages)
		if len(rows) < schedQualityLedgerPageSize {
			s.finishedLoaded = true
		}
	}
	after := s.insertedAt.Add(-schedquality.LedgerReloadOverlap)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		var rows []schedquality.AttemptSample
		var latest time.Time
		rows, latest, err = s.attempts.LoadInsertedAfter(ctx, after, schedQualityLedgerPageSize)
		if err != nil {
			return err
		}
		s.window.merge(rows, now)
		if latest.After(s.insertedAt) {
			s.insertedAt = latest
		}
		if len(rows) < schedQualityLedgerPageSize || boot && !latest.Before(s.loadThrough) {
			break
		}
		if !latest.After(after) {
			return errors.New("attempt ledger cursor did not advance")
		}
		after = latest
	}
	if err = s.window.prepareContinuations(ctx, s.attempts); err != nil {
		return err
	}
	s.loaded = true
	if boot {
		slog.InfoContext(ctx, "sched_quality.loading", "component", "sched_quality", "status", "complete", "rows", s.loadRows, "pages", s.loadPages)
	}
	return nil
}

func (s *SchedQualityEngineService) inputs(ctx context.Context, now time.Time, samples map[SchedAccountModel][]schedquality.AttemptSample) (*schedQualityData, error) {
	if err := s.refreshConfig(ctx, now); err != nil {
		return nil, err
	}
	d := &schedQualityData{evidenceSince: schedQualitySince(s.loadSince, now.Add(-schedEvidenceRetention)), now: now, samples: samples, globalEnabled: true, running: s.window.running, previous: s.window.previous, runningCache: s.window.runningPredecessors(), ruleSamples: make(map[SchedAccountModel][]schedquality.RuleBSample), ruleGroups: make(map[schedGroupModel][]schedquality.RuleBSample)}
	for pair, rows := range samples {
		d.ruleSamples[pair] = ruleBSamples(rows)
	}
	for key, rows := range s.window.byGroupModel {
		d.ruleGroups[key] = ruleBSamples(rows)
	}
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
	oldPolicies, oldRule := s.policies, s.rule
	var batch *schedReplayBatch
	locked, err := s.store.RunCycle(ctx, func(tx SchedQualityTx) error {
		var err error
		batch, err = s.replayTx(ctx, tx, d)
		return err
	})
	if err != nil {
		s.policies, s.rule = oldPolicies, oldRule
		return err
	}
	if !locked {
		s.processedAt = time.Time{}
		batch = &schedReplayBatch{}
		batch.states, batch.overrides, batch.resets, err = s.store.LoadRows(ctx)
		if err != nil {
			return err
		}
	} else {
		s.commitReplay(ctx, batch, batch.states)
	}
	s.window.newFinishedAt = time.Time{}
	notifications = s.publish(d, batch.states, batch.overrides, batch.resets, now)
	return nil
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
	at, groups, err := s.store.RunReset(ctx, id)
	if err != nil {
		return time.Time{}, err
	}
	for _, group := range groups {
		s.releaseGroup(ctx, schedQualityKey{id, group.Model, group.GroupID})
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

func (s *SchedQualityEngineService) SetTrialLeases(leases SchedTrialLeases) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leases = leases
}
func (s *SchedQualityEngineService) releaseEndedTrials(ctx context.Context, before, after []schedquality.LineState) {
	trial, present := make(map[schedQualityKey]bool), make(map[schedQualityKey]bool)
	for _, r := range after {
		k := schedQualityKey{r.AccountID, r.Model, r.GroupID}
		present[k] = true
		trial[k] = trial[k] || r.Trial
	}
	groups := make(map[schedQualityKey]bool)
	for _, r := range before {
		k := schedQualityKey{r.AccountID, r.Model, r.GroupID}
		if (r.Trial || !present[k]) && !trial[k] {
			groups[k] = true
		}
	}
	for k := range groups {
		s.releaseGroup(ctx, k)
	}
}
func (s *SchedQualityEngineService) releaseGroup(ctx context.Context, k schedQualityKey) {
	if s.leases == nil {
		return
	}
	if err := s.leases.ReleaseGroup(ctx, k.account, k.model, k.group); err != nil {
		slog.WarnContext(ctx, "sched_quality.release_trial_group", "account_id", k.account, "model", k.model, "group_id", k.group, "error", err)
	}
}

// Each action records the deadline it actually starts, never a leftover state deadline.
func schedDecisionUntil(action schedquality.Action, next schedquality.LineState) time.Time {
	switch action {
	case schedquality.ActionTrial:
		return next.TrialUntil
	case schedquality.ActionEnter, schedquality.ActionRenew, schedquality.ActionTrialFail:
		return next.Until
	default:
		return time.Time{}
	}
}
func (s *SchedQualityEngineService) countDecision(action schedquality.Action) {
	switch action {
	case schedquality.ActionEnter, schedquality.ActionRenew, schedquality.ActionTrialFail:
		s.obs.Count("cooled", 1)
	case schedquality.ActionRelease, schedquality.ActionTrialPass, schedquality.ActionTrialTimeout:
		s.obs.Count("released", 1)
	case schedquality.ActionTrial:
		s.obs.Count("trial", 1)
	case schedquality.ActionRelax:
		s.obs.Count("relax", 1)
	}
}

// The Redis observation is written after the lease check and compared with
// the evaluator's clock. Use the same two-second allowance as cross-instance
// session-failure clearing for the uncertainty at each end of the trial.
const trialSeatBoundaryAllowance = SessionFailureClockSkewAllowance

// trialSeats describes the original trial window even when its deadline is replayed late.
func (s *SchedQualityEngineService) trialSeats(ctx context.Context, trial schedquality.LineState, budget time.Duration) string {
	if s.leases == nil || budget <= 0 || trial.Epoch.IsZero() || trial.TrialUntil.IsZero() {
		return "unknown"
	}
	readCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	at, err := s.leases.LastFull(readCtx, trial.AccountID, trial.Model, trial.GroupID)
	switch {
	case err != nil:
		return "unknown"
	case at.IsZero() || at.Before(trial.Epoch.Add(-trialSeatBoundaryAllowance)):
		return "not_observed"
	case at.After(trial.Epoch.Add(trialSeatBoundaryAllowance)) && at.Before(trial.TrialUntil.Add(-trialSeatBoundaryAllowance)):
		return "full"
	default:
		return "unknown"
	}
}
