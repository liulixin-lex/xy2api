package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"golang.org/x/sync/singleflight"
)

const (
	upstreamBalanceRoundInterval        = time.Minute
	upstreamBalanceRoundLockKey         = "upstream_balance:round"
	upstreamBalanceRoundLockTTL         = 58 * time.Second
	upstreamBalanceRoundBudget          = 50 * time.Second
	upstreamBalanceDueSlack             = 5 * time.Second
	upstreamBalanceConcurrency          = 4
	upstreamBalanceRequestTimeout       = 10 * time.Second
	upstreamBalanceMaxBodyBytes         = 64 * 1024
	upstreamBalanceStatusMaxBodyBytes   = 1 << 20
	upstreamBalanceOriginBudgetPerRound = 40
	upstreamBalanceStoreTimeout         = 2 * time.Second
	UpstreamBalanceMaxReadIDs           = 200
)

var errUpstreamBalanceBudgetExceeded = errors.New("upstream balance origin budget exceeded")
var errUpstreamBalanceAccountBudgetExceeded = errors.New("upstream balance account budget exceeded")

type upstreamBalanceStartedAtKey struct{}

type UpstreamBalanceItem struct {
	AccountID     int64      `json:"account_id"`
	State         string     `json:"state"`
	AmountUSD     *float64   `json:"amount_usd"`
	Unlimited     bool       `json:"unlimited"`
	Basis         *string    `json:"basis"`
	UpstreamKind  *string    `json:"upstream_kind"`
	Reason        *string    `json:"reason"`
	ObservedAt    *time.Time `json:"observed_at"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
}

type UpstreamBalanceService struct {
	accountRepo        AccountRepository
	accountTestService *AccountTestService
	store              UpstreamBalanceStore
	lockCache          LeaderLockCache
	now                func() time.Time
	instanceID         string
	parentCtx          context.Context
	parentCancel       context.CancelFunc
	wg                 sync.WaitGroup
	mu                 sync.Mutex
	started, stopped   bool
	roundMu            sync.Mutex
	siteFlight         singleflight.Group
	roundBudget        time.Duration
	accountBudget      time.Duration
	pausedLogAt        atomic.Int64
}
type upstreamBalanceRoundMemo struct {
	mu          sync.Mutex
	budget      map[string]int
	reserved    map[int64]int
	origins     map[int64]string
	storeFailed bool
	cooldowns   map[string]time.Time
	persisted   map[int64]bool
}

func (m *upstreamBalanceRoundMemo) reserveAccount(id int64, origin string, n int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.budget == nil {
		m.budget = make(map[string]int)
	}
	if m.reserved == nil {
		m.reserved = make(map[int64]int)
	}
	if m.origins == nil {
		m.origins = make(map[int64]string)
	}
	if m.budget[origin]+n > upstreamBalanceOriginBudgetPerRound {
		return false
	}
	m.budget[origin] += n
	m.reserved[id] += n
	m.origins[id] = origin
	return true
}
func (m *upstreamBalanceRoundMemo) reserveMore(id int64, origin string, n int) bool {
	return m.reserveAccount(id, origin, n)
}
func (m *upstreamBalanceRoundMemo) takeReservation(id int64, origin string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reserved == nil {
		m.reserved = make(map[int64]int)
	}
	if m.origins == nil {
		m.origins = make(map[int64]string)
	}
	if m.budget == nil {
		m.budget = make(map[string]int)
	}
	if m.reserved[id] > 0 && m.origins[id] == origin {
		m.reserved[id]--
		return true
	}
	if m.budget[origin] >= upstreamBalanceOriginBudgetPerRound {
		return false
	}
	m.budget[origin]++
	return true
}
func (m *upstreamBalanceRoundMemo) releaseAccount(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	remaining, origin := m.reserved[id], m.origins[id]
	if remaining > 0 {
		m.budget[origin] -= remaining
	}
	delete(m.reserved, id)
	delete(m.origins, id)
}
func (m *upstreamBalanceRoundMemo) releaseOne(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reserved[id] > 0 {
		m.reserved[id]--
		m.budget[m.origins[id]]--
	}
}
func (m *upstreamBalanceRoundMemo) refundUnconsumed(id int64) {
	m.releaseAccount(id)
}
func (m *upstreamBalanceRoundMemo) setCooldown(origin string, until time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cooldowns == nil {
		m.cooldowns = make(map[string]time.Time)
	}
	if until.After(m.cooldowns[origin]) {
		m.cooldowns[origin] = until
	}
}
func (m *upstreamBalanceRoundMemo) cooldownActive(origin string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return now.Before(m.cooldowns[origin])
}
func (m *upstreamBalanceRoundMemo) failed() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.storeFailed
}
func (m *upstreamBalanceRoundMemo) failStore(ctx context.Context) bool {
	// An account deadline says nothing about Redis health. Its worker records
	// a transient outcome using the still-live round context.
	if errors.Is(context.Cause(ctx), errUpstreamBalanceAccountBudgetExceeded) {
		return false
	}
	if m == nil {
		return false
	}
	m.mu.Lock()
	m.storeFailed = true
	m.mu.Unlock()
	return true
}
func (m *upstreamBalanceRoundMemo) markPersisted(id int64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.persisted == nil {
		m.persisted = make(map[int64]bool)
	}
	m.persisted[id] = true
}
func (m *upstreamBalanceRoundMemo) wasPersisted(id int64) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.persisted[id]
}
func NewUpstreamBalanceService(repo AccountRepository, ats *AccountTestService, store UpstreamBalanceStore, lock LeaderLockCache) *UpstreamBalanceService {
	ctx, cancel := context.WithCancel(context.Background())
	return &UpstreamBalanceService{accountRepo: repo, accountTestService: ats, store: store, lockCache: lock, now: time.Now, instanceID: uuid.NewString(), parentCtx: ctx, parentCancel: cancel, roundBudget: upstreamBalanceRoundBudget, accountBudget: 25 * time.Second}
}
func ProvideUpstreamBalanceService(repo AccountRepository, ats *AccountTestService, store UpstreamBalanceStore, lock LeaderLockCache) *UpstreamBalanceService {
	s := NewUpstreamBalanceService(repo, ats, store, lock)
	s.Start()
	return s
}
func (s *UpstreamBalanceService) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(upstreamBalanceRoundInterval)
		defer ticker.Stop()
		s.runLoop()
		for {
			select {
			case <-s.parentCtx.Done():
				return
			case <-ticker.C:
				s.runLoop()
			}
		}
	}()
}
func (s *UpstreamBalanceService) runLoop() {
	if err := s.RunRound(s.parentCtx); err != nil {
		_ = err
		slog.Warn("upstream_balance.round_failed", "stage", "round")
	}
}
func (s *UpstreamBalanceService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		s.wg.Wait()
		return
	}
	s.stopped = true
	s.parentCancel()
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *UpstreamBalanceService) RunRound(ctx context.Context) error {
	if s == nil || s.accountRepo == nil || s.store == nil || s.accountTestService == nil || s.accountTestService.httpUpstream == nil {
		return nil
	}
	if !s.roundMu.TryLock() {
		return nil
	}
	defer s.roundMu.Unlock()
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	if s.lockCache != nil {
		lockCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		ok, err := s.lockCache.TryAcquireLeaderLock(lockCtx, upstreamBalanceRoundLockKey, s.instanceID, upstreamBalanceRoundLockTTL)
		if err != nil {
			s.logPaused()
			return nil
		}
		if !ok {
			return nil
		}
	}
	roundStart := s.now().UTC()
	roundCtx, cancel := context.WithTimeout(ctx, s.roundBudget)
	defer cancel()
	accounts, err := s.accountRepo.ListAllWithFilters(roundCtx, "", AccountTypeAPIKey, "", "", 0, "")
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(accounts))
	byID := make(map[int64]*Account)
	for i := range accounts {
		a := &accounts[i]
		if a.Status == StatusActive || a.Status == StatusError {
			if _, ok := upstreamBalanceApplicable(a); ok {
				ids = append(ids, a.ID)
				byID[a.ID] = a
			}
		}
	}
	entries, err := s.store.Load(roundCtx, ids)
	if err != nil {
		s.logPaused()
		return nil
	}
	memo := &upstreamBalanceRoundMemo{budget: make(map[string]int), reserved: make(map[int64]int), origins: make(map[int64]string), cooldowns: make(map[string]time.Time)}
	sort.Slice(ids, func(i, j int) bool {
		a, b := entries.States[ids[i]], entries.States[ids[j]]
		if a == nil && b != nil {
			return true
		}
		if a != nil && b == nil {
			return false
		}
		if a == nil {
			return ids[i] < ids[j]
		}
		if !a.NextDueAt.Equal(b.NextDueAt) {
			return a.NextDueAt.Before(b.NextDueAt)
		}
		return ids[i] < ids[j]
	})
	var wg sync.WaitGroup
	var due, polled, skipped atomic.Int64
	roundStarted := s.now()
	sem := make(chan struct{}, upstreamBalanceConcurrency)
	for _, id := range ids {
		st := entries.States[id]
		fp := upstreamBalanceFingerprint(byID[id], upstreamBalanceEffectiveBaseURL(byID[id]))
		if !upstreamBalanceDue(st, fp, roundStart, s.now()) {
			continue
		}
		due.Add(1)
		sem <- struct{}{}
		if !upstreamBalanceCanStartAccount(roundStart.Add(s.roundBudget), s.now().UTC()) {
			<-sem
			skipped.Add(1)
			break
		}
		reserve := 1
		if k := entries.Kinds[id]; k != nil && k.Fingerprint == fp {
			switch k.Kind {
			case "sub2api":
				reserve = 1
			case "newapi":
				reserve = 2
			}
			if k.Kind == "newapi" && strings.TrimSpace(byID[id].GetCredential("newapi_access_token")) != "" {
				reserve++
			}
		}
		origin := mustOrigin(upstreamBalanceEffectiveBaseURL(byID[id]))
		if !memo.reserveAccount(id, origin, reserve) {
			<-sem
			skipped.Add(1)
			continue
		}
		wg.Add(1)
		go func(id int64, a *Account, st *UpstreamBalanceState) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					slog.Warn("upstream_balance.round_failed", "stage", "panic")
				}
			}()
			deadline, _ := roundCtx.Deadline()
			if roundCtx.Err() != nil || !upstreamBalanceCanStartAccount(deadline, s.now()) {
				memo.releaseAccount(id)
				return
			}
			if st == nil || st.Fingerprint != upstreamBalanceFingerprint(a, upstreamBalanceEffectiveBaseURL(a)) {
				st = &UpstreamBalanceState{Version: 1, Fingerprint: upstreamBalanceFingerprint(a, upstreamBalanceEffectiveBaseURL(a))}
			}
			started := s.now()
			accountCtx, accountCancel := context.WithTimeoutCause(roundCtx, s.accountBudget, errUpstreamBalanceAccountBudgetExceeded)
			accountCtx = context.WithValue(accountCtx, upstreamBalanceStartedAtKey{}, started)
			defer accountCancel()
			defer memo.releaseAccount(id)
			s.pollOne(accountCtx, roundStart, id, a, st, memo)
			if errors.Is(context.Cause(accountCtx), errUpstreamBalanceAccountBudgetExceeded) && roundCtx.Err() == nil && !memo.failed() && !memo.wasPersisted(id) {
				kind := ""
				if k := entries.Kinds[id]; k != nil {
					kind = k.Kind
				}
				budgetCtx := context.WithValue(roundCtx, upstreamBalanceStartedAtKey{}, started)
				s.persistOutcome(budgetCtx, roundStart, id, st, ubOutcome{reason: "upstream_error", kind: kind}, memo)
			}
			polled.Add(1)
		}(id, byID[id], st)
	}
	wg.Wait()
	slog.Debug("upstream_balance.round_done", "due", due.Load(), "polled", polled.Load(), "skipped", skipped.Load(), "duration_ms", s.now().Sub(roundStarted).Milliseconds())
	return nil
}

func upstreamBalanceDue(st *UpstreamBalanceState, fingerprint string, roundStart, now time.Time) bool {
	if st == nil || st.Fingerprint != fingerprint {
		return true
	}
	if st.RetryNotBefore != nil {
		return !now.Before(*st.RetryNotBefore)
	}
	return !roundStart.Before(st.NextDueAt.Add(-upstreamBalanceDueSlack))
}

func (s *UpstreamBalanceService) pollOne(ctx context.Context, roundStart time.Time, id int64, a *Account, old *UpstreamBalanceState, memo *upstreamBalanceRoundMemo) {
	if memo != nil {
		defer memo.releaseAccount(id)
	}
	if memo != nil && memo.failed() {
		return
	}
	base, ok := upstreamBalanceApplicable(a)
	if !ok {
		return
	}
	fp := upstreamBalanceFingerprint(a, base)
	st := old
	if st == nil || st.Fingerprint != fp {
		st = &UpstreamBalanceState{Version: 1, Fingerprint: fp}
	}
	if _, err := s.accountTestService.validateUpstreamBaseURL(base); err != nil {
		s.persistOutcome(ctx, roundStart, id, st, ubOutcome{reason: "upstream_error"}, memo)
		return
	}
	if out := s.newAPICooldown(ctx, mustOrigin(base), memo); out != nil {
		if !memo.failed() {
			s.persistOutcome(ctx, roundStart, id, st, *out, memo)
		}
		return
	}
	oldKind := s.kindForAccount(ctx, id, fp, memo)
	if memo.failed() {
		s.logPaused()
		return
	}
	if oldKind != nil {
		if oldKind.Kind == "unsupported" {
			return
		}
		if oldKind.Kind == "newapi" {
			s.pollKnownNewAPI(ctx, roundStart, id, a, base, st, memo, false)
			return
		}
		if oldKind.Kind == "sub2api" {
			probe := collectUpstreamBalance(upstreamBalanceAdapter(sub2APIUpstreamBalanceAdapter{service: s}), ctx, a, st, memo)
			switch probe.reason {
			case "unidentified":
				if probe.status == http.StatusNotFound || probe.status == http.StatusMethodNotAllowed || (probe.status >= 200 && probe.status < 300) {
					writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), upstreamBalanceStoreTimeout)
					err := s.store.DeleteKind(writeCtx, id)
					cancel()
					if err != nil {
						memo.failStore(writeCtx)
						return
					}
				}
				probe.reason, probe.kind, probe.deleteKind = "upstream_error", "sub2api", false
			case "auth_failed":
				probe.kind = "sub2api"
			}
			if probe.reason != "budget_deferred" {
				s.persistOutcome(ctx, roundStart, id, st, probe, memo)
			}
			return
		}
	}
	if strings.TrimSpace(a.GetCredential("newapi_access_token")) != "" && !st.TokenShortcutRejected {
		memo.refundUnconsumed(id)
		if err := s.store.SaveKind(ctx, id, &UpstreamBalanceKindRecord{Version: 1, Fingerprint: fp, Kind: "newapi", IdentifiedAt: s.now().UTC()}, 24*time.Hour); err != nil {
			memo.failStore(ctx)
			return
		}
		if !memo.reserveMore(id, mustOrigin(base), 2+tokenCallBudget(a)) {
			return
		}
		s.pollKnownNewAPI(ctx, roundStart, id, a, base, st, memo, true)
		return
	}
	probe := collectUpstreamBalance(upstreamBalanceAdapter(sub2APIUpstreamBalanceAdapter{service: s}), ctx, a, st, memo)
	if memo.failed() {
		s.logPaused()
		return
	}
	if probe.kind == "sub2api" && (probe.success || probe.reason == "no_data" || probe.reason == "key_ungrouped" || (probe.reason == "auth_failed" && probe.status == http.StatusOK)) {
		if err := s.store.SaveKind(ctx, id, &UpstreamBalanceKindRecord{Version: 1, Fingerprint: fp, Kind: "sub2api", IdentifiedAt: s.now().UTC()}, 24*time.Hour); err != nil {
			memo.failStore(ctx)
			return
		}
		s.persistOutcome(ctx, roundStart, id, st, probe, memo)
		return
	}
	if probe.reason == "budget_deferred" {
		return
	}
	if probe.reason == "rate_limited" || probe.reason == "upstream_error" {
		s.persistOutcome(ctx, roundStart, id, st, probe, memo)
		return
	}
	// Identification shares the site's cache and singleflight with known new-api accounts.
	origin := mustOrigin(base)
	site, siteOut := s.newAPISite(ctx, a, origin, memo)
	if siteOut != nil {
		if siteOut.notNewAPI {
			if strings.TrimSpace(a.GetCredential("newapi_access_token")) != "" {
				st.TokenShortcutRejected = true
			}
		} else if siteOut.reason != "budget_deferred" && !memo.failed() {
			s.persistOutcome(ctx, roundStart, id, st, *siteOut, memo)
			return
		} else {
			return
		}
	} else if site != nil {
		if err := s.store.SaveKind(ctx, id, &UpstreamBalanceKindRecord{Version: 1, Fingerprint: fp, Kind: "newapi", IdentifiedAt: s.now().UTC()}, 24*time.Hour); err != nil {
			memo.failStore(ctx)
			return
		}
		if !memo.reserveMore(id, origin, 2+tokenCallBudget(a)) {
			return
		}
		s.pollKnownNewAPI(ctx, roundStart, id, a, base, st, memo, false)
		return
	}
	if probe.reason == "auth_failed" {
		s.persistOutcome(ctx, roundStart, id, st, probe, memo)
		return
	}
	if strings.TrimSpace(a.GetCredential("newapi_access_token")) != "" {
		st.TokenShortcutRejected = true
	}
	st.UnsupportedStreak = min(st.UnsupportedStreak+1, 16)
	if err := s.store.SaveKind(ctx, id, &UpstreamBalanceKindRecord{Version: 1, Fingerprint: fp, Kind: "unsupported", IdentifiedAt: s.now().UTC()}, unsupportedTTL(st.UnsupportedStreak)); err != nil {
		memo.failStore(ctx)
		return
	}
	probe.reason = "unidentified"
	probe.kind = "unsupported"
	s.persistOutcome(ctx, roundStart, id, st, probe, memo)
}

func (s *UpstreamBalanceService) persistOutcome(ctx context.Context, roundStart time.Time, id int64, st *UpstreamBalanceState, out ubOutcome, memos ...*upstreamBalanceRoundMemo) {
	if ctx.Err() != nil {
		return
	}
	if len(memos) > 0 {
		memos[0].markPersisted(id)
	}
	now := s.now().UTC().Truncate(time.Second)
	st.LastAttemptAt = now
	st.LastReason = out.reason
	st.LastHTTPStatus = out.status
	if out.success {
		st.TransientStreak, st.AuthStreak, st.UnsupportedStreak = 0, 0, 0
	}
	delay := delayForOutcome(out, st)
	if out.reason == "rate_limited" || out.reason == "upstream_error" || out.reason == "auth_failed" {
		retryAt := s.now().UTC().Add(delay)
		if out.retryNotBefore != nil {
			retryAt = *out.retryNotBefore
		}
		st.RetryNotBefore = &retryAt
	} else {
		st.RetryNotBefore = nil
	}
	st.NextDueAt = roundStart.Add(delay)
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), upstreamBalanceStoreTimeout)
	defer cancel()
	var err error
	if out.success {
		err = s.store.SaveSuccess(writeCtx, id, &UpstreamBalanceRecord{Version: 1, Fingerprint: st.Fingerprint, Kind: out.kind, AmountUSD: out.amount, Unlimited: out.unlimited, Basis: out.basis, Reason: out.reason, ObservedAt: now}, st)
	} else {
		err = s.store.SaveAttempt(writeCtx, id, st)
	}
	if err != nil {
		if len(memos) > 0 && memos[0] != nil {
			memos[0].failStore(writeCtx)
		}
		s.logPaused()
	} else {
		started, _ := ctx.Value(upstreamBalanceStartedAtKey{}).(time.Time)
		duration := int64(0)
		if !started.IsZero() {
			duration = s.now().Sub(started).Milliseconds()
		}
		slog.Debug("upstream_balance.account_result", "account_id", id, "upstream_kind", out.kind, "reason", out.reason, "http_status", out.status, "duration_ms", duration)
	}
}

func (s *UpstreamBalanceService) logPaused() {
	now := s.now().UnixNano()
	previous := s.pausedLogAt.Load()
	if previous != 0 && now-previous < int64(10*time.Minute) {
		return
	}
	if s.pausedLogAt.CompareAndSwap(previous, now) {
		slog.Warn("upstream_balance.paused", "cause", "redis_unavailable")
	}
}

func upstreamBalanceCanStartAccount(roundDeadline, now time.Time) bool {
	return roundDeadline.Sub(now) >= 25*time.Second
}
func unsupportedTTL(streak int) time.Duration {
	return cappedBackoff(30*time.Minute, streak, 24*time.Hour)
}

func (s *UpstreamBalanceService) kindForAccount(ctx context.Context, id int64, fp string, memo *upstreamBalanceRoundMemo) *UpstreamBalanceKindRecord {
	e, err := s.store.Load(ctx, []int64{id})
	if err != nil || e == nil {
		if err != nil && memo != nil {
			memo.failStore(ctx)
		}
		return nil
	}
	k := e.Kinds[id]
	if k == nil || k.Fingerprint != fp {
		return nil
	}
	return k
}
func (s *UpstreamBalanceService) pollKnownNewAPI(ctx context.Context, roundStart time.Time, id int64, a *Account, base string, st *UpstreamBalanceState, memo *upstreamBalanceRoundMemo, tokenShortcut bool) {
	out := collectUpstreamBalance(upstreamBalanceAdapter(newAPIUpstreamBalanceAdapter{service: s}), ctx, a, st, memo)
	if memo.failed() {
		s.logPaused()
	}
	if ctx.Err() != nil || memo.failed() || out.reason == "budget_deferred" {
		return
	}
	if tokenShortcut && out.notNewAPI {
		st.TokenShortcutRejected = true
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), upstreamBalanceStoreTimeout)
		if err := s.store.DeleteKind(writeCtx, id); err != nil {
			cancel()
			memo.failStore(writeCtx)
			return
		}
		cancel()
		probe := collectUpstreamBalance(upstreamBalanceAdapter(sub2APIUpstreamBalanceAdapter{service: s}), ctx, a, st, memo)
		if memo.failed() {
			return
		}
		if probe.kind == "sub2api" && (probe.success || probe.reason == "no_data" || probe.reason == "key_ungrouped" || (probe.reason == "auth_failed" && probe.status == http.StatusOK)) {
			if err := s.store.SaveKind(ctx, id, &UpstreamBalanceKindRecord{Version: 1, Fingerprint: st.Fingerprint, Kind: "sub2api", IdentifiedAt: s.now().UTC()}, 24*time.Hour); err != nil {
				memo.failStore(ctx)
				return
			}
		} else if probe.reason == "budget_deferred" {
			return
		} else if probe.reason == "unidentified" {
			st.UnsupportedStreak = min(st.UnsupportedStreak+1, 16)
			probe.reason, probe.kind = "unidentified", "unsupported"
			if err := s.store.SaveKind(ctx, id, &UpstreamBalanceKindRecord{Version: 1, Fingerprint: st.Fingerprint, Kind: "unsupported", IdentifiedAt: s.now().UTC()}, unsupportedTTL(st.UnsupportedStreak)); err != nil {
				memo.failStore(ctx)
				return
			}
		}
		s.persistOutcome(ctx, roundStart, id, st, probe, memo)
		return
	}
	if out.deleteKind {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), upstreamBalanceStoreTimeout)
		err := s.store.DeleteKind(writeCtx, id)
		cancel()
		if err != nil {
			memo.failStore(writeCtx)
			return
		}
	}
	s.persistOutcome(ctx, roundStart, id, st, out, memo)
}

func tokenCallBudget(a *Account) int {
	if a != nil && strings.TrimSpace(a.GetCredential("newapi_access_token")) != "" {
		return 1
	}
	return 0
}

func (s *UpstreamBalanceService) ubRequestWithBudget(ctx context.Context, a *Account, rawURL, auth string, max int, memo *upstreamBalanceRoundMemo) (*http.Response, []byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	origin, err := upstreamBalanceOrigin(u.Scheme + "://" + u.Host)
	if err != nil {
		return nil, nil, err
	}
	if memo != nil && !memo.takeReservation(a.ID, origin) {
		return nil, nil, errUpstreamBalanceBudgetExceeded
	}
	return s.ubRequest(ctx, a, rawURL, auth, max)
}

func mustOrigin(base string) string {
	o, _ := upstreamBalanceOrigin(base)
	return o
}

func delayForOutcome(out ubOutcome, st *UpstreamBalanceState) time.Duration {
	const maxStreak = 16
	switch out.reason {
	case "rate_limited":
		if out.retryAfter > 0 {
			return out.retryAfter
		}
		return 5 * time.Minute
	case "auth_failed", "key_ungrouped":
		st.AuthStreak = min(st.AuthStreak+1, maxStreak)
		return cappedBackoff(5*time.Minute, st.AuthStreak, time.Hour)
	case "upstream_error":
		st.TransientStreak = min(st.TransientStreak+1, maxStreak)
		return cappedBackoff(time.Minute, st.TransientStreak, 10*time.Minute)
	case "unidentified":
		return unsupportedTTL(max(1, st.UnsupportedStreak))
	case "no_data":
		return 5 * time.Minute
	case "key_exhausted":
		return 5 * time.Minute
	default:
		if st.Slow {
			return 5 * time.Minute
		}
		return time.Minute
	}
}
func cappedBackoff(base time.Duration, streak int, cap time.Duration) time.Duration {
	if streak < 1 {
		streak = 1
	}
	shift := min(streak-1, 16)
	d := base << shift
	if d <= 0 || d > cap {
		return cap
	}
	return d
}

type ubOutcome struct {
	success        bool
	amount         *float64
	unlimited      bool
	basis          string
	reason         string
	kind           string
	status         int
	retryAfter     time.Duration
	retryNotBefore *time.Time
	deleteKind     bool
	notNewAPI      bool
}

// upstreamBalanceAdapter is the extension point for platform-specific collection.
type upstreamBalanceAdapter interface {
	Collect(context.Context, *Account, *UpstreamBalanceState, *upstreamBalanceRoundMemo) ubOutcome
}

func collectUpstreamBalance(adapter upstreamBalanceAdapter, ctx context.Context, account *Account, state *UpstreamBalanceState, memo *upstreamBalanceRoundMemo) ubOutcome {
	return adapter.Collect(ctx, account, state, memo)
}

type sub2APIUpstreamBalanceAdapter struct{ service *UpstreamBalanceService }
type newAPIUpstreamBalanceAdapter struct{ service *UpstreamBalanceService }

func (a sub2APIUpstreamBalanceAdapter) Collect(ctx context.Context, account *Account, state *UpstreamBalanceState, memo *upstreamBalanceRoundMemo) ubOutcome {
	base, ok := upstreamBalanceApplicable(account)
	if !ok {
		return ubOutcome{reason: "unidentified", kind: "unsupported"}
	}
	return a.service.collectSub2API(ctx, account, base, state, memo)
}
func (a newAPIUpstreamBalanceAdapter) Collect(ctx context.Context, account *Account, state *UpstreamBalanceState, memo *upstreamBalanceRoundMemo) ubOutcome {
	base, ok := upstreamBalanceApplicable(account)
	if !ok {
		return ubOutcome{reason: "unidentified", kind: "unsupported"}
	}
	if memo != nil && memo.failed() {
		return ubOutcome{reason: "redis_unavailable", kind: "newapi"}
	}
	return a.service.collectNewAPI(ctx, account, base, state, memo)
}

func readUBBody(resp *http.Response, max int) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, errors.New("nil upstream response")
	}
	defer func() { _ = resp.Body.Close() }()
	b, e := io.ReadAll(io.LimitReader(resp.Body, int64(max)+1))
	if e != nil {
		return nil, e
	}
	if len(b) > max {
		return nil, errors.New("upstream response too large")
	}
	return b, nil
}
func (s *UpstreamBalanceService) ubRequest(ctx context.Context, a *Account, rawURL, auth string, max int) (*http.Response, []byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, upstreamBalanceRequestTimeout)
	defer cancel()
	req, e := http.NewRequestWithContext(requestCtx, http.MethodGet, rawURL, nil)
	if e != nil {
		return nil, nil, e
	}
	req.Header.Set("Accept", "application/json")
	a.ApplyHeaderOverrides(req.Header)
	if auth != "" {
		setHeaderRaw(req.Header, "Authorization", auth)
	}
	req = req.WithContext(WithHTTPUpstreamSingleAttempt(WithHTTPUpstreamRedirectsDisabled(req.Context())))
	proxy := ""
	if a.ProxyID != nil {
		if a.Proxy == nil || a.Proxy.ID != *a.ProxyID {
			return nil, nil, errors.New("proxy unavailable")
		}
		proxy = a.Proxy.URL()
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileDefault))
	var profile *tlsfingerprint.Profile
	if a.Platform == PlatformOpenAI {
		req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	}
	if s.accountTestService.tlsFPProfileService != nil {
		profile = s.accountTestService.tlsFPProfileService.ResolveTLSProfile(a)
	}
	resp, e := s.accountTestService.httpUpstream.DoWithTLS(req, proxy, a.ID, a.Concurrency, profile)
	if e != nil {
		return nil, nil, e
	}
	b, e := readUBBody(resp, max)
	if resp != nil && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp, b, errors.New("redirect response")
	}
	return resp, b, e
}

var _ = json.Valid

func (s *UpstreamBalanceService) ReadBalances(ctx context.Context, accountIDs []int64) ([]UpstreamBalanceItem, error) {
	if len(accountIDs) > UpstreamBalanceMaxReadIDs {
		return nil, ErrUpstreamBalanceTooManyIDs
	}
	seen := map[int64]struct{}{}
	ids := make([]int64, 0, len(accountIDs))
	for _, id := range accountIDs {
		if id <= 0 {
			return nil, ErrUpstreamBalanceInvalidIDs
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return []UpstreamBalanceItem{}, nil
	}
	accounts, err := s.accountRepo.GetByIDs(ctx, ids)
	if err != nil {
		return nil, ErrUpstreamBalanceUnavailable
	}
	byID := map[int64]*Account{}
	for _, a := range accounts {
		if a != nil {
			byID[a.ID] = a
		}
	}
	entries := &UpstreamBalanceEntries{Records: map[int64]*UpstreamBalanceRecord{}, States: map[int64]*UpstreamBalanceState{}, Kinds: map[int64]*UpstreamBalanceKindRecord{}}
	if s.store == nil {
		return readUnavailableItems(ids, byID), nil
	}
	c, cancel := context.WithTimeout(ctx, upstreamBalanceStoreTimeout)
	defer cancel()
	entries, err = s.store.Load(c, ids)
	if err != nil {
		return readUnavailableItems(ids, byID), nil
	}
	items := make([]UpstreamBalanceItem, 0, len(ids))
	for _, id := range ids {
		a := byID[id]
		if a == nil {
			continue
		}
		item := UpstreamBalanceItem{AccountID: id, State: "unavailable"}
		base, ok := upstreamBalanceApplicable(a)
		if !ok {
			item.State = "not_applicable"
			items = append(items, item)
			continue
		}
		fp := upstreamBalanceFingerprint(a, base)
		rec, st, kind := entries.Records[id], entries.States[id], entries.Kinds[id]
		if rec != nil && rec.Fingerprint == fp && s.now().UTC().Sub(rec.ObservedAt) < 24*time.Hour {
			item.State = "ok"
			item.AmountUSD = rec.AmountUSD
			item.Unlimited = rec.Unlimited
			item.Basis = ubStrPtrOrNil(rec.Basis)
			item.UpstreamKind = ubStrPtrOrNil(rec.Kind)
			item.ObservedAt = ubTimePtr(rec.ObservedAt)
			item.LastAttemptAt = ubTimePtr(rec.ObservedAt)
			item.Reason = ubStrPtrOrNil(rec.Reason)
			if st != nil && st.Fingerprint == fp {
				item.LastAttemptAt = ubTimePtr(ubMaxTime(st.LastAttemptAt, rec.ObservedAt))
				if st.LastAttemptAt.After(rec.ObservedAt) && st.LastReason != "" {
					item.Reason = ubStrPtr(st.LastReason)
				}
			}
		} else if st != nil && st.Fingerprint == fp {
			item.LastAttemptAt = ubTimePtr(st.LastAttemptAt)
			if kind != nil && kind.Fingerprint == fp && (kind.Kind == "sub2api" || kind.Kind == "newapi") {
				item.UpstreamKind = ubStrPtr(kind.Kind)
			}
			if kind != nil && kind.Fingerprint == fp && kind.Kind == "unsupported" {
				item.Reason = ubStrPtr("unidentified")
			} else {
				item.Reason = ubStrPtrOrNil(st.LastReason)
			}
		}
		items = append(items, item)
	}
	return items, nil
}
func readUnavailableItems(ids []int64, byID map[int64]*Account) []UpstreamBalanceItem {
	items := make([]UpstreamBalanceItem, 0, len(ids))
	for _, id := range ids {
		a := byID[id]
		if a == nil {
			continue
		}
		_, ok := upstreamBalanceApplicable(a)
		it := UpstreamBalanceItem{AccountID: id, State: "unavailable"}
		if !ok {
			it.State = "not_applicable"
		} else {
			it.Reason = ubStrPtr("redis_unavailable")
		}
		items = append(items, it)
	}
	return items
}
func ubStrPtr(s string) *string { return &s }
func ubStrPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func ubTimePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	v := t
	return &v
}
func ubMaxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
