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
	cancel      context.CancelFunc
	ctx         context.Context
	wg          sync.WaitGroup
}

func NewSchedTestRunnerService(f SchedTestForwarder, a SchedTestAdmission, c *ConcurrencyService, accounts schedTestAccountReader, store SchedTestStore, now func() time.Time, lock LeaderLockCache, db *sql.DB) *SchedTestRunnerService {
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &SchedTestRunnerService{forward: f, admission: a, concurrency: c, accounts: accounts, store: store, now: now, count: schedTestCount, wait: schedTestWait, lockCache: lock, db: db, owner: uuid.NewString(), ctx: ctx, cancel: cancel}
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
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.run(record, account, requestModel) }()
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

func (s *SchedTestRunnerService) run(record SchedTestRecord, account *Account, requestModel string) {
	ctx, cancel := context.WithTimeout(s.ctx, schedquality.TestTimeout)
	defer cancel()
	parallel := schedquality.TestTierParallelMax
	if account.Concurrency > 0 {
		parallel = min(parallel, max(1, account.Concurrency-1))
	}
	sem := make(chan struct{}, parallel)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sent := 0
	busy := 0
	failed := false
	notSchedulable := false
	for tier := schedquality.TierSmall; tier <= schedquality.TierLarge; tier++ {
		wg.Add(1)
		go func(tier schedquality.TestTier) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			session := schedtest.Session{ID: record.ID, Model: requestModel}
			session.Effort = schedTestEffort(account, record.Model, tier)
			prev := 0
			for turn := 1; turn <= schedquality.TestTurnsPerTier; turn++ {
				if ctx.Err() != nil {
					return
				}
				if turn > 1 && !s.wait(ctx, schedquality.TestTurnGap) {
					return
				}
				mapped, ok, _ := s.admission.TestRequestModel(ctx, account, record.Model)
				if !ok || mapped == "" {
					mu.Lock()
					notSchedulable = true
					mu.Unlock()
					cancel()
					return
				}
				session.Model = mapped
				body := schedtest.Build(tier, turn, session, s.count)
				if len(body) == 0 {
					mu.Lock()
					failed = true
					mu.Unlock()
					return
				}
				slotCtx, slotCancel := context.WithTimeout(ctx, schedquality.TestSlotWait)
				var slot *AcquireResult
				var err error
				for {
					slot, err = s.acquire(slotCtx, account, tier)
					if err != nil || slot == nil || slot.Acquired {
						break
					}
					if !s.wait(slotCtx, 100*time.Millisecond) {
						break
					}
				}
				slotCancel()
				if err != nil || slot == nil || !slot.Acquired {
					mu.Lock()
					if err != nil && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
						failed = true
					} else {
						busy++
					}
					mu.Unlock()
					return
				}
				mapped, ok, _ = s.admission.TestRequestModel(ctx, account, record.Model)
				if !ok || mapped != session.Model {
					slot.ReleaseFunc()
					mu.Lock()
					notSchedulable = true
					mu.Unlock()
					cancel()
					return
				}
				if ctx.Err() != nil {
					slot.ReleaseFunc()
					return
				}
				turnCtx, turnCancel := context.WithTimeout(ctx, schedquality.TestTurnTimeout)
				result, samples, answer, _ := schedTestForward(turnCtx, s.forward, account, record.Model, record.ID+"/"+tier.String(), body, turn > 1, prev)
				deadline := errors.Is(turnCtx.Err(), context.DeadlineExceeded) && !errors.Is(ctx.Err(), context.DeadlineExceeded)
				turnCancel()
				slot.ReleaseFunc()
				// Capture begins at transport send. Credential/build failures have no sample.
				if len(samples) == 0 {
					mu.Lock()
					failed = true
					mu.Unlock()
					return
				}
				selected := samples[len(samples)-1]
				for _, sample := range samples {
					if sample.Outcome == schedquality.OutcomeTimeout || sample.Outcome == schedquality.OutcomeAccountFault {
						selected = sample
						break
					}
				}
				if deadline && selected.Outcome != schedquality.OutcomeTimeout && selected.Outcome != schedquality.OutcomeAccountFault {
					firstOutput := false
					for _, sample := range samples {
						if sample.TTFOMs != nil {
							firstOutput = true
							break
						}
					}
					if !firstOutput {
						selected.Outcome = schedquality.OutcomeTimeout
					} else {
						selected.Outcome = schedquality.OutcomeOther
					}
				}
				var p *int
				if turn > 1 {
					previous := prev
					p = &previous
				}
				row := schedquality.TestTurn{Tier: tier, Turn: turn, Class: selected.Class, Warm: turn > 1, TTFOMs: selected.TTFOMs, Outcome: selected.Outcome, Detail: selected.Detail, PromptTokens: selected.PromptTokens, PrevPromptTokens: p, CacheReadTokens: selected.CacheReadTokens}
				mu.Lock()
				sent += len(samples)
				record.Turns = append(record.Turns, row)
				if result != nil {
					record.InputTokens += int64(result.Usage.InputTokens)
					record.CachedTokens += int64(result.Usage.CacheReadInputTokens)
					record.OutputTokens += int64(result.Usage.OutputTokens)
				}
				mu.Unlock()
				if result != nil && result.Usage.InputTokens > 0 {
					prev = result.Usage.InputTokens
				} else if selected.PromptTokens != nil {
					prev = *selected.PromptTokens
				}
				var parsed struct {
					Input json.RawMessage `json:"input"`
				}
				_ = json.Unmarshal(body, &parsed)
				session.Input = parsed.Input
				session.Answer = answer
				if selected.Outcome != schedquality.OutcomeOK {
					return
				}
			}
		}(tier)
	}
	wg.Wait()
	record.Running = false
	record.FinishedAt = s.now()
	if sent > 0 {
		record.Done = true
	} else if failed {
		record.AbortReason = "error"
	} else if notSchedulable {
		record.AbortReason = "not_schedulable"
	} else if busy > 0 {
		record.AbortReason = "busy"
	} else if ctx.Err() != nil {
		record.AbortReason = "timeout"
	} else {
		record.AbortReason = "not_schedulable"
	}
	writeCtx, writeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer writeCancel()
	if err := s.store.Finish(writeCtx, record); err != nil {
		logger.LegacyPrintf("service.sched_test", "finish_failed: test_id=%s err=%v", record.ID, err)
	}
}

var _ SchedTestRunner = (*SchedTestRunnerService)(nil)
