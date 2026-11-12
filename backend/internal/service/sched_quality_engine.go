package service

import (
	"context"
	"database/sql"
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
	SchedQualityTestEnabled(ctx context.Context) bool
}

type SchedQualityEngineService struct {
	store      SchedQualityStore
	attempts   SchedAttemptStore
	runner     SchedTestRunner
	switches   schedQualitySwitches
	now        func() time.Time
	mu         sync.Mutex
	window     schedQualityWindow
	snapshot   atomic.Pointer[SchedQualitySnapshot]
	loaded     bool
	insertedAt time.Time
	configAt   time.Time
	policies   map[int64]SchedPolicyRecord
	members    map[int64][]int64
	seenResets map[int64]time.Time
	callbackMu sync.Mutex
	callbacks  []func(int64, time.Time)
	lifecycle  sync.Mutex
	stop       chan struct{}
	done       chan struct{}
	lockCache  LeaderLockCache
	db         *sql.DB
	instanceID string
}

const schedQualityLedgerPageSize = 5000

var _ SchedQualityEngine = (*SchedQualityEngineService)(nil)

func NewSchedQualityEngineService(store SchedQualityStore, attempts SchedAttemptStore, runner SchedTestRunner, switches schedQualitySwitches, now func() time.Time) *SchedQualityEngineService {
	if now == nil {
		now = time.Now
	}
	s := &SchedQualityEngineService{store: store, attempts: attempts, runner: runner, switches: switches, now: now, seenResets: make(map[int64]time.Time), instanceID: uuid.NewString()}
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
		rows, err := s.attempts.LoadStartedSince(ctx, now.Add(-schedquality.WindowMaxAge))
		if err != nil {
			return err
		}
		cursor = now.Add(-schedquality.WindowMaxAge)
		s.window.merge(rows, now)
		for _, row := range rows {
			if row.InsertedAt.After(cursor) {
				cursor = row.InsertedAt
			}
		}
	}
	// The store returns complete inserted_at groups, including ties at the page boundary.
	maxSeen := cursor
	after := cursor.Add(-schedquality.LedgerReloadOverlap)
	for {
		rows, maxInserted, err := s.attempts.LoadInsertedAfter(ctx, after, schedQualityLedgerPageSize)
		if err != nil {
			return err
		}
		s.window.merge(rows, now)
		if len(rows) == 0 {
			break
		}
		if maxInserted.After(maxSeen) {
			maxSeen = maxInserted
		}
		if len(rows) < schedQualityLedgerPageSize {
			break
		}
		after = maxInserted
	}
	s.insertedAt, s.loaded = maxSeen, true
	return nil
}

func (s *SchedQualityEngineService) cycle(ctx context.Context) error {
	s.mu.Lock()
	var resets map[int64]time.Time
	defer func() { s.mu.Unlock(); s.notify(resets) }()
	now := s.now()
	// Bound local evidence even when any subsequent read fails.
	samples := s.window.take(now)
	if err := s.loadEvidence(ctx, now); err != nil {
		return err
	}
	d, err := s.inputs(ctx, now, samples)
	if err != nil {
		return err
	}
	var states []schedquality.StateRow
	var overrides []SchedOverride
	var resetAt map[int64]time.Time
	var transitions []schedQualityTransition
	wanted := make(map[SchedAccountModel]bool)
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
		resetAt, err = tx.ResetTimes()
		if err != nil {
			return err
		}
		keys, previous := s.rowKeys(d, states)
		var upserts, deleted, kept []schedquality.StateRow
		for _, key := range keys {
			prev := previous[key]
			if prev.AccountID == 0 {
				prev = schedquality.StateRow{AccountID: key.account, Model: key.model, GroupID: key.group}
			}
			since := schedQualitySince(resetAt[key.account], prev.Since)
			summary := d.summary(SchedAccountModel{key.account, key.model}, since)
			in := schedquality.Inputs{Policy: s.policies[key.group].Policy, Summary: summary, Tests: d.finished[SchedAccountModel{key.account, key.model}], TestsEnabled: d.testsEnabled, ResetAt: resetAt[key.account]}
			next, changed, remove, wantTest := schedquality.Next(prev, in, now)
			if remove {
				if _, ok := previous[key]; ok {
					deleted = append(deleted, prev)
				}
				continue
			}
			// A zero Since is the pure rules' representation of an absent row.
			if !next.Since.IsZero() {
				kept = append(kept, next)
				if changed {
					upserts = append(upserts, next)
				}
			}
			if changed && (prev.State != next.State || !prev.Since.Equal(next.Since)) {
				verdicts, fromTest := schedquality.Effective(schedquality.Judge(summary, in.Policy), in.Tests, key.model, in.Policy, in.ResetAt, prev.Since, now)
				transitions = append(transitions, schedQualityTransition{from: prev, to: next, summary: summary, verdicts: verdicts, fromTest: fromTest, policy: in.Policy, test: schedquality.SummarizeTests(in.Tests, key.model, in.Policy, in.ResetAt, prev.Since, now)})
			}
			if wantTest && d.globalEnabled && d.testsEnabled {
				wanted[SchedAccountModel{key.account, key.model}] = true
			}
		}
		for key, row := range previous {
			if !s.isMember(key.group, key.account) {
				deleted = append(deleted, row)
			}
		}
		var expired, active []SchedOverride
		for _, o := range overrides {
			if (o.ExpiresAt != nil && !o.ExpiresAt.After(now)) || (o.GroupID != 0 && !s.isMember(o.GroupID, o.AccountID)) {
				expired = append(expired, o)
			} else {
				active = append(active, o)
			}
		}
		if len(upserts) > 0 {
			if err := tx.Upsert(upserts); err != nil {
				return err
			}
		}
		if len(deleted) > 0 {
			sort.Slice(deleted, func(i, j int) bool {
				return schedQualityLess(schedQualityKeyOf(deleted[i]), schedQualityKeyOf(deleted[j]))
			})
			if err := tx.Delete(deleted); err != nil {
				return err
			}
		}
		if len(expired) > 0 {
			if err := tx.DeleteOverrides(expired); err != nil {
				return err
			}
		}
		states, overrides = kept, active
		return nil
	})
	if err != nil {
		return err
	}
	if !locked {
		states, overrides, resetAt, err = s.store.LoadRows(ctx)
		if err != nil {
			return err
		}
	} else {
		for _, tr := range transitions {
			tr.log(ctx)
		}
		pairs := make([]SchedAccountModel, 0, len(wanted))
		for pair := range wanted {
			pairs = append(pairs, pair)
		}
		sort.Slice(pairs, func(i, j int) bool {
			if pairs[i].AccountID != pairs[j].AccountID {
				return pairs[i].AccountID < pairs[j].AccountID
			}
			return pairs[i].Model < pairs[j].Model
		})
		for _, pair := range pairs {
			if _, err := s.runner.Request(ctx, pair.AccountID, pair.Model, true, 0); err != nil {
				slog.ErrorContext(ctx, "sched_quality.request_test", "account_id", pair.AccountID, "model", pair.Model, "error", err)
			}
		}
		if err := s.runner.ReapStale(ctx); err != nil {
			slog.ErrorContext(ctx, "sched_quality.reap_tests", "error", err)
		}
	}
	resets = s.publish(d, states, overrides, resetAt, now)
	return nil
}

func (s *SchedQualityEngineService) inputs(ctx context.Context, now time.Time, samples map[SchedAccountModel][]schedquality.AttemptSample) (*schedQualityData, error) {
	if err := s.refreshConfig(ctx, now); err != nil {
		return nil, err
	}
	tests, err := s.runner.Fresh(ctx)
	if err != nil {
		return nil, err
	}
	d := &schedQualityData{now: now, samples: samples, tests: make(map[SchedAccountModel][]SchedTestRecord, len(tests)), finished: make(map[SchedAccountModel][]schedquality.TestResult), summaries: make(map[schedQualitySummaryKey]schedquality.Summary), globalEnabled: true, testsEnabled: true}
	if s.switches != nil {
		d.globalEnabled = s.switches.SchedQualityEnabled(ctx)
		d.testsEnabled = s.switches.SchedQualityTestEnabled(ctx)
	}
	for pair, records := range tests {
		cloned := make([]SchedTestRecord, len(records))
		for i, record := range records {
			record = cloneSchedQualityTest(record)
			cloned[i] = record
			if !record.Running {
				d.finished[pair] = append(d.finished[pair], record.TestResult)
			}
		}
		d.tests[pair] = cloned
	}
	return d, nil
}

func (s *SchedQualityEngineService) rebuild(ctx context.Context) (map[int64]time.Time, error) {
	now := s.now()
	d, err := s.inputs(ctx, now, s.window.take(now))
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
	previous := s.snapshot.Load()
	at, err := s.store.RunReset(ctx, id)
	if err != nil {
		return time.Time{}, err
	}
	for _, row := range previous.GroupRows[id] {
		if row.View.Since.IsZero() {
			continue
		}
		tr := schedQualityTransition{
			from:    schedquality.StateRow{AccountID: id, Model: row.Model, GroupID: row.GroupID, State: row.View.State, Level: row.View.Level, Reason: row.View.Reason, Since: row.View.Since, Until: row.View.Until},
			to:      schedquality.StateRow{AccountID: id, Model: row.Model, GroupID: row.GroupID, Since: at},
			summary: row.Summary, verdicts: row.View.Verdicts, fromTest: row.View.FromTest, test: row.Test, via: "reset",
		}
		tr.log(ctx)
	}
	notifications, err = s.rebuild(ctx)
	if err != nil {
		return at, err
	}
	return at, nil
}

func (s *SchedQualityEngineService) SetOverride(ctx context.Context, o SchedOverride) error {
	if o.AccountID <= 0 || o.GroupID < 0 || (o.Mode != schedquality.OverrideExempt && o.Mode != schedquality.OverrideDemote) {
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
	if _, err := s.attempts.DeleteBefore(ctx, s.now().Add(-schedquality.LedgerRetention)); err != nil {
		slog.ErrorContext(ctx, "sched_quality.cleanup", "error", err)
	}
}
