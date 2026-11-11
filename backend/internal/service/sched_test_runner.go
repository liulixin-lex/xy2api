package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/pkg/schedtest"
)

var ErrSchedTestRunning = errors.New("sched test already running")

type SchedTestStore interface {
	Insert(context.Context, SchedTestRecord) error
	Finish(context.Context, SchedTestRecord) error
	Latest(context.Context, int64, string) (*SchedTestRecord, error)
	Since(context.Context, time.Time) ([]SchedTestRecord, error)
	Recent(context.Context, int64, int) ([]SchedTestRecord, error)
	ReapStale(context.Context, time.Time, time.Time) error
	DeleteBefore(context.Context, time.Time) error
}

type schedTestAccountReader interface {
	GetByID(context.Context, int64) (*Account, error)
}

type SchedTestRunnerService struct {
	forward     SchedTestForwarder
	admission   SchedTestAdmission
	concurrency *ConcurrencyService
	accounts    schedTestAccountReader
	store       SchedTestStore
	now         func() time.Time
	count       func([]byte) int
	wait        func(context.Context, time.Duration) bool
	acquire     func(context.Context, *Account, schedquality.TestTier) (*AcquireResult, error)
	lockCache   LeaderLockCache
	db          *sql.DB
	owner       string
	mu          sync.Mutex
	tierLimits  map[int64]*schedTestTierLimit
	cancel      context.CancelFunc
	ctx         context.Context
	wg          sync.WaitGroup
}

type schedTestTierLimit struct {
	slots chan struct{}
	tests int
}

func NewSchedTestRunnerService(f SchedTestForwarder, a SchedTestAdmission, c *ConcurrencyService, accounts schedTestAccountReader, store SchedTestStore, now func() time.Time, lock LeaderLockCache, db *sql.DB) *SchedTestRunnerService {
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &SchedTestRunnerService{forward: f, admission: a, concurrency: c, accounts: accounts, store: store, now: now, count: schedTestCount, wait: schedTestWait, lockCache: lock, db: db, owner: uuid.NewString(), ctx: ctx, cancel: cancel, tierLimits: make(map[int64]*schedTestTierLimit)}
	s.acquire = func(ctx context.Context, account *Account, _ schedquality.TestTier) (*AcquireResult, error) {
		return s.concurrency.AcquireAccountSlot(ctx, account.ID, account.Concurrency)
	}
	return s
}

func schedTestWait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *SchedTestRunnerService) Start(ctx context.Context) {
	s.mu.Lock()
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(schedquality.CleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				release, ok := tryAcquireSingletonLeaderLock(s.ctx, s.lockCache, s.db, "sched-test-retention", s.owner, 5*time.Minute)
				if ok {
					_ = s.store.DeleteBefore(s.ctx, s.now().Add(-schedquality.TestRetention))
					release()
				}
			}
		}
	}()
}
func (s *SchedTestRunnerService) Stop() { s.mu.Lock(); s.cancel(); s.mu.Unlock(); s.wg.Wait() }

func (s *SchedTestRunnerService) Request(ctx context.Context, accountID int64, model string, auto bool, operatorID int64) (SchedTestTicket, error) {
	ticket := SchedTestTicket{Model: model}
	// Serialize local check-and-insert; the database unique index handles other instances.
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return ticket, err
	}
	last, err := s.store.Latest(ctx, accountID, model)
	if err != nil {
		return ticket, err
	}
	if last != nil && last.Running {
		ticket.Why = "already_running"
		return ticket, nil
	}
	if last != nil && s.now().Sub(last.StartedAt) < schedquality.TestRetryEvery {
		ticket.Why = "too_soon"
		return ticket, nil
	}
	account, err := s.accounts.GetByID(ctx, accountID)
	if err != nil {
		return ticket, err
	}
	if account == nil {
		return ticket, errors.New("sched test account not found")
	}
	requestModel, ok, _ := s.admission.TestRequestModel(ctx, account, model)
	record := SchedTestRecord{AccountID: accountID, Model: model, Auto: auto, OperatorID: operatorID, Running: ok, TestResult: schedquality.TestResult{ID: uuid.NewString(), StartedAt: s.now()}}
	if !ok || requestModel == "" {
		record.Running = false
		record.AbortReason = "not_schedulable"
		record.FinishedAt = s.now()
		ticket.Why = "not_schedulable"
	}
	if err = s.store.Insert(ctx, record); errors.Is(err, ErrSchedTestRunning) {
		ticket.Why = "already_running"
		return ticket, nil
	} else if err != nil {
		return ticket, err
	}
	ticket.TestID = record.ID
	if !record.Running {
		return ticket, nil
	}
	ticket.Started = true
	limit := s.tierLimits[accountID]
	if limit == nil {
		parallel := schedquality.TestTierParallelMax
		if account.Concurrency > 0 {
			parallel = min(parallel, max(1, account.Concurrency-1))
		}
		limit = &schedTestTierLimit{slots: make(chan struct{}, parallel)}
		s.tierLimits[accountID] = limit
	}
	limit.tests++
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			limit.tests--
			if limit.tests == 0 {
				delete(s.tierLimits, accountID)
			}
			s.mu.Unlock()
			if recovered := recover(); recovered != nil {
				logger.LegacyPrintf("service.sched_test", "runner_panic: test_id=%s panic=%v", record.ID, recovered)
			}
		}()
		s.run(record, limit.slots)
	}()
	return ticket, nil
}

func (s *SchedTestRunnerService) Fresh(ctx context.Context) (map[SchedAccountModel][]SchedTestRecord, error) {
	rows, err := s.store.Since(ctx, s.now().Add(-schedquality.TestFreshFor))
	if err != nil {
		return nil, err
	}
	result := make(map[SchedAccountModel][]SchedTestRecord)
	for _, r := range rows {
		key := SchedAccountModel{AccountID: r.AccountID, Model: r.Model}
		result[key] = append(result[key], r)
	}
	return result, nil
}
func (s *SchedTestRunnerService) Recent(ctx context.Context, id int64, limit int) ([]SchedTestRecord, error) {
	return s.store.Recent(ctx, id, limit)
}
func (s *SchedTestRunnerService) ReapStale(ctx context.Context) error {
	now := s.now()
	return s.store.ReapStale(ctx, now.Add(-schedquality.TestStaleAfter), now)
}

func schedTestCount(body []byte) int {
	var req openAIInputTokensCountRequest
	if json.Unmarshal(body, &req) != nil {
		return 0
	}
	n, err := estimateOpenAIInputTokens(req)
	if err != nil {
		return 0
	}
	return n
}

func schedTestEffort(account *Account, model string, tier schedquality.TestTier) string {
	wanted := schedquality.TestTiers[tier].Effort
	metadata, _ := account.GetUpstreamModelMetadata(model)
	levels := normalizeReasoningLevels(metadata.SupportedReasoningLevels)
	if len(levels) == 0 && metadata.Reasoning != nil && !*metadata.Reasoning {
		levels = []string{"none"}
	}
	best, bestRank := "", -1
	for _, level := range levels {
		if level == wanted {
			return wanted
		}
		rank, valid := reasoningEffortRank(level)
		if level == "none" {
			rank, valid = 0, true
		} else if level == "ultra" {
			rank, valid = 7, true
		}
		if valid && rank > bestRank {
			best, bestRank = level, rank
		}
	}
	if best != "" {
		return best
	}
	return wanted
}

func (s *SchedTestRunnerService) run(record SchedTestRecord, slots chan struct{}) {
	ctx, cancel := context.WithTimeout(s.ctx, schedquality.TestTimeout)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	failed, busy, notSchedulable := false, false, false
	base := record
	for tier := schedquality.TierSmall; tier <= schedquality.TierLarge; tier++ {
		wg.Add(1)
		go func(tier schedquality.TestTier) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			partial := s.runTier(ctx, cancel, base, tier)
			mu.Lock()
			defer mu.Unlock()
			record.Turns = append(record.Turns, partial.Turns...)
			record.InputTokens += partial.InputTokens
			record.CachedTokens += partial.CachedTokens
			record.OutputTokens += partial.OutputTokens
			failed = failed || partial.AbortReason == "error"
			busy = busy || partial.AbortReason == "busy"
			notSchedulable = notSchedulable || partial.AbortReason == "not_schedulable"
		}(tier)
	}
	wg.Wait()
	record.Running = false
	record.FinishedAt = s.now()
	if len(record.Turns) > 0 {
		record.Done = true
	} else if failed {
		record.AbortReason = "error"
	} else if notSchedulable {
		record.AbortReason = "not_schedulable"
	} else if busy {
		record.AbortReason = "busy"
	} else if ctx.Err() != nil {
		record.AbortReason = "timeout"
	}
	writeCtx, writeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer writeCancel()
	if err := s.store.Finish(writeCtx, record); err != nil {
		logger.LegacyPrintf("service.sched_test", "finish_failed: test_id=%s err=%v", record.ID, err)
	}
}

func (s *SchedTestRunnerService) runTier(ctx context.Context, cancel context.CancelFunc, record SchedTestRecord, tier schedquality.TestTier) (partial SchedTestRecord) {
	defer func() {
		if recovered := recover(); recovered != nil {
			partial.AbortReason = "error"
			logger.LegacyPrintf("service.sched_test", "tier_panic: test_id=%s tier=%s panic=%v", record.ID, tier, recovered)
		}
	}()
	session := schedtest.Session{ID: record.ID}
	var prev *int
	for turn := 1; turn <= schedquality.TestTurnsPerTier; turn++ {
		if ctx.Err() != nil || turn > 1 && !s.wait(ctx, schedquality.TestTurnGap) {
			return partial
		}
		// Scope the slot and its cancellation to this round, including panic exits.
		keepGoing := func() bool {
			account, err := s.accounts.GetByID(ctx, record.AccountID)
			if err != nil || account == nil {
				partial.AbortReason = "error"
				return false
			}
			mapped, ok, _ := s.admission.TestRequestModel(ctx, account, record.Model)
			if !ok || mapped == "" {
				partial.AbortReason = "not_schedulable"
				cancel()
				return false
			}
			slotCtx, slotCancel := context.WithTimeout(ctx, schedquality.TestSlotWait)
			defer slotCancel()
			var slot *AcquireResult
			for slotCtx.Err() == nil {
				slot, err = s.acquire(slotCtx, account, tier)
				if err != nil || slot == nil || slot.Acquired {
					break
				}
				if !s.wait(slotCtx, schedquality.TestSlotRetryInterval) {
					break
				}
			}
			slotCancel()
			if err != nil || slot == nil || !slot.Acquired {
				if err != nil && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
					partial.AbortReason = "error"
				} else if ctx.Err() == nil {
					partial.AbortReason = "busy"
				}
				return false
			}
			defer slot.ReleaseFunc()
			if ctx.Err() != nil {
				return false
			}
			// Slot waiting can outlive an account update; read again before forwarding.
			account, err = s.accounts.GetByID(ctx, record.AccountID)
			if err != nil || account == nil {
				partial.AbortReason = "error"
				return false
			}
			mapped, ok, _ = s.admission.TestRequestModel(ctx, account, record.Model)
			if !ok || mapped == "" {
				partial.AbortReason = "not_schedulable"
				cancel()
				return false
			}
			session.Model = mapped
			session.Effort = schedTestEffort(account, record.Model, tier)
			body := schedtest.Build(tier, turn, session, s.count)
			if len(body) == 0 {
				partial.AbortReason = "error"
				return false
			}
			if ctx.Err() != nil {
				return false
			}
			prevTokens := 0
			if prev != nil {
				prevTokens = *prev
			}
			turnCtx, turnCancel := context.WithTimeout(ctx, schedquality.TestTurnTimeout)
			defer turnCancel()
			result, samples, answer, _ := schedTestForward(turnCtx, s.forward, account, record.Model, body, turn > 1, prevTokens)
			// Capture begins at transport send. Credential/build failures have no sample.
			if len(samples) == 0 {
				partial.AbortReason = "error"
				return false
			}
			selected := samples[len(samples)-1]
			for _, sample := range samples {
				if sample.Outcome == schedquality.OutcomeTimeout || sample.Outcome == schedquality.OutcomeAccountFault {
					selected = sample
					break
				}
			}
			if turnCtx.Err() != nil {
				firstOutput := false
				for _, sample := range samples {
					if sample.TTFOMs != nil {
						firstOutput = true
						break
					}
				}
				selected.Outcome = schedquality.OutcomeOther
				selected.Detail = "cancelled"
				if ctx.Err() == nil && errors.Is(turnCtx.Err(), context.DeadlineExceeded) && !firstOutput {
					selected.Outcome = schedquality.OutcomeTimeout
				}
			}
			partial.Turns = append(partial.Turns, schedquality.TestTurn{
				Tier: tier, Turn: turn, Class: selected.Class, Warm: turn > 1,
				TTFOMs: selected.TTFOMs, Outcome: selected.Outcome, Detail: selected.Detail,
				PromptTokens: selected.PromptTokens, PrevPromptTokens: prev, CacheReadTokens: selected.CacheReadTokens,
			})
			if result != nil {
				partial.InputTokens += int64(result.Usage.InputTokens)
				partial.CachedTokens += int64(result.Usage.CacheReadInputTokens)
				partial.OutputTokens += int64(result.Usage.OutputTokens)
			}
			// Unknown usage must not reuse zero or an earlier round's prompt count.
			prev = selected.PromptTokens
			if result != nil && result.Usage.InputTokens > 0 {
				prompt := result.Usage.InputTokens
				prev = &prompt
			}
			var parsed struct {
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(body, &parsed)
			session.Input, session.Answer = parsed.Input, answer
			return selected.Outcome == schedquality.OutcomeOK
		}()
		if !keepGoing {
			return partial
		}
	}
	return partial
}

var _ SchedTestRunner = (*SchedTestRunnerService)(nil)
