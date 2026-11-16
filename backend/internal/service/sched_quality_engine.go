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
	rule        SchedTTFTRuleRecord
	peers       *peerBucket
	leases      SchedTrialLeases
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

func NewSchedQualityEngineService(store SchedQualityStore, attempts SchedAttemptStore, switches schedQualitySwitches, now func() time.Time, obs SchedObs) *SchedQualityEngineService {
	if now == nil {
		now = time.Now
	}
	s := &SchedQualityEngineService{store: store, attempts: attempts, switches: switches, now: now, seenResets: make(map[int64]time.Time), instanceID: uuid.NewString(), obs: schedObsOrNop(obs), rule: SchedTTFTRuleRecord{Params: schedquality.DefaultRuleBParams()}}
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

func (s *SchedQualityEngineService) loadEvidence(ctx context.Context, now time.Time) error {
	cursor := s.insertedAt
	if !s.loaded {
		rows, err := s.attempts.LoadFinishedSince(ctx, now.Add(-schedEvidenceRetention))
		if err != nil {
			return err
		}
		cursor = now.Add(-schedEvidenceRetention)
		s.window.merge(rows, now)
		// A recently inserted attempt may have finished before the completion
		// window. Scan the entire insertion window on boot, not just the newest
		// completion's overlap, because availability is max(finished, inserted).
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
	if err := s.window.prepareContinuations(ctx, s.attempts); err != nil {
		return err
	}
	s.insertedAt, s.loaded = maxSeen, true
	return nil
}

func (s *SchedQualityEngineService) inputs(ctx context.Context, now time.Time, samples map[SchedAccountModel][]schedquality.AttemptSample) (*schedQualityData, error) {
	if err := s.refreshConfig(ctx, now); err != nil {
		return nil, err
	}
	d := &schedQualityData{now: now, samples: samples, globalEnabled: true, running: s.window.running, peers: s.peers, ruleSamples: make(map[SchedAccountModel][]schedquality.RuleBSample), ruleGroups: make(map[schedGroupModel][]schedquality.RuleBSample)}
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
	var states, previous []schedquality.LineState
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
		// Configuration must match the states protected by this transaction, even
		// when another instance changed it since our last cached membership refresh.
		policies, err := s.store.LoadPolicies(ctx)
		if err != nil {
			return err
		}
		rule, err := s.store.LoadTTFTRule(ctx)
		if err != nil {
			return err
		}
		s.policies, s.rule = policies, rule
		states, err = tx.States()
		if err != nil {
			return err
		}
		previous = append([]schedquality.LineState(nil), states...)
		overrides, err = tx.Overrides()
		if err != nil {
			return err
		}
		resets, err = tx.ResetTimes()
		if err != nil {
			return err
		}
		var changed []schedquality.LineState
		var deleted []schedquality.LineKey
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
		s.peers = nil
		d.peers = nil
		states, overrides, resets, err = s.store.LoadRows(ctx)
		if err != nil {
			return err
		}
	} else {
		s.processedAt = horizon
		s.peers = d.peers
		s.releaseEndedTrials(ctx, previous, states)
		for _, decision := range decisions {
			LogSchedQualityDecision(ctx, decision)
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

// replay advances complete event batches in chronological order. EvaluatedAt is
// persisted even when Step emits no decision, preventing repeat work on restart.
func (s *SchedQualityEngineService) replay(d *schedQualityData, states []schedquality.LineState, resets map[int64]time.Time, after, horizon time.Time) (kept, changed []schedquality.LineState, deleted []schedquality.LineKey, decisions []schedquality.Decision) {
	keys, loaded := s.rowKeys(d, states)
	initial := make(map[schedquality.LineKey]schedquality.LineState)
	for _, r := range states {
		initial[r.LineKey] = r
	}
	rows := make(map[schedquality.LineKey]schedquality.LineState)
	enabled := keys[:0]
	for _, k := range keys {
		if s.policies[k.GroupID].Policy.Enabled {
			r := loaded[k]
			r.LineKey = k
			rows[k] = r
			enabled = append(enabled, k)
		}
	}
	keys = enabled
	events := make(map[time.Time]map[SchedAccountModel]bool)
	add := func(at time.Time) {
		if !at.IsZero() && !at.After(horizon) && at.After(after) {
			if events[at] == nil {
				events[at] = make(map[SchedAccountModel]bool)
			}
		}
	}
	for at := after.Truncate(time.Minute).Add(time.Minute); !at.After(horizon); at = at.Add(time.Minute) {
		add(at)
	}
	for pair, samples := range d.samples {
		for _, r := range samples {
			at := schedAvailableAt(r)
			add(at)
			if events[at] != nil {
				events[at][pair] = true
			}
		}
	}
	schedule := func(r schedquality.LineState) {
		for _, at := range []time.Time{r.Until, r.TrialUntil, r.LastRear.Add(schedquality.LevelClearQuiet)} {
			if at.After(r.EvaluatedAt) {
				if !at.After(after) {
					at = after.Add(time.Nanosecond)
				}
				add(at)
			}
		}
	}
	for _, r := range rows {
		schedule(r)
	}
	for len(events) > 0 {
		var at time.Time
		for t := range events {
			if at.IsZero() || t.Before(at) {
				at = t
			}
		}
		touched := events[at]
		delete(events, at)
		bucket := at.UTC().Truncate(schedquality.RegularInterval)
		if d.peers == nil || !d.peers.at.Equal(bucket) {
			d.peers = freezePeers(at, rows)
		}
		fiveMin := at.Equal(bucket)
		minute := at.Equal(at.Truncate(time.Minute))
		for _, k := range keys {
			prev := rows[k]
			if !at.After(prev.EvaluatedAt) || !at.After(resets[k.AccountID]) {
				continue
			}
			kind, due := schedquality.Due(prev, at, fiveMin, minute || touched[SchedAccountModel{k.AccountID, k.Model}])
			if !due {
				continue
			}
			v, basis := s.assessLine(d, prev, kind, at, resets)
			next, action := schedquality.Step(prev, kind, v, basis, at)
			rows[k] = next
			if action != "" {
				decisions = append(decisions, schedquality.Decision{At: at, Key: k, Action: action, Level: next.Level, Until: next.Until, Basis: basis})
			}
			schedule(next)
		}
	}
	for _, k := range keys {
		r := rows[k]
		if r.EvaluatedAt.IsZero() {
			continue
		}
		kept = append(kept, r)
		if !reflect.DeepEqual(r, initial[k]) {
			changed = append(changed, r)
		}
	}
	for k := range initial {
		if _, ok := rows[k]; !ok {
			deleted = append(deleted, k)
		}
	}
	sort.Slice(deleted, func(i, j int) bool { return schedLineLess(deleted[i], deleted[j]) })
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
	rows, _, _, err := s.store.LoadRows(ctx)
	if err != nil {
		return time.Time{}, err
	}
	groups := make(map[schedQualityKey]bool)
	for _, r := range rows {
		if r.AccountID == id {
			groups[schedQualityKey{r.AccountID, r.Model, r.GroupID}] = true
		}
	}
	s.releaseGroups(ctx, groups)
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
	s.releaseGroups(ctx, groups)
}
func (s *SchedQualityEngineService) releaseGroups(ctx context.Context, groups map[schedQualityKey]bool) {
	if s.leases == nil {
		return
	}
	for k := range groups {
		if err := s.leases.ReleaseGroup(ctx, k.account, k.model, k.group); err != nil {
			slog.WarnContext(ctx, "sched_quality.release_trial_group", "account_id", k.account, "model", k.model, "group_id", k.group, "error", err)
		}
	}
}
