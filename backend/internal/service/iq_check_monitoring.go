package service

import (
	"context"
	"math/rand/v2"
	"os"
	"strconv"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
)

type iqMonitoringRepository interface {
	StartIQCheck(context.Context, IQCheckClaim, time.Time) (bool, error)
	DeferIQCheck(context.Context, IQCheckClaim, string, time.Time) error
}

type IQQueueResult struct {
	Queued         bool       `json:"queued"`
	ExecutionState string     `json:"execution_state"`
	TaskID         string     `json:"task_id"`
	NextEligibleAt *time.Time `json:"next_eligible_at,omitempty"`
	Reason         string     `json:"reason,omitempty"`
}

func (s *IQCheckService) QueueStatus(ctx context.Context, id int64) (IQQueueResult, error) {
	if err := s.Queue(ctx, id); err != nil {
		return IQQueueResult{}, err
	}
	a, err := s.accounts.GetByID(ctx, id)
	if err != nil {
		return IQQueueResult{}, err
	}
	q := a.IQCheck.Summary()
	return IQQueueResult{true, q.ExecutionState, q.TaskID, q.NextEligibleAt, q.ExecutionReason}, nil
}

func (s *IQCheckService) Status(ctx context.Context, id int64) (domain.IQCheck, error) {
	if s == nil || s.accounts == nil || id <= 0 {
		return domain.IQCheck{}, ErrIQCheckInvalid
	}
	a, err := s.accounts.GetByID(ctx, id)
	if err != nil {
		return domain.IQCheck{}, err
	}
	if a.Platform != PlatformOpenAI {
		return domain.IQCheck{}, ErrIQCheckInvalid
	}
	return a.IQCheck.Summary(), nil
}

func iqMonitoringConfig() int {
	n := 2
	if v, e := strconv.Atoi(os.Getenv("IQ_CHECK_MAX_CONCURRENCY")); e == nil && v >= 1 && v <= 10 {
		n = v
	}
	return n
}

func iqHealth(a *Account, now time.Time) (string, time.Time) {
	next := now.Add(time.Minute)
	if !a.IsActive() || !a.Schedulable {
		return "account_disabled", next
	}
	if a.AutoPauseOnExpired && a.ExpiresAt != nil && !a.ExpiresAt.After(now) {
		return "account_expired", next
	}
	cooldownUntil := now
	for _, t := range []*time.Time{a.OverloadUntil, a.RateLimitResetAt, a.TempUnschedulableUntil} {
		if t != nil && t.After(cooldownUntil) {
			cooldownUntil = *t
		}
	}
	if cooldownUntil.After(now) {
		return "account_cooldown", cooldownUntil
	}
	if a.IsAPIKeyOrBedrock() && a.IsQuotaExceeded() {
		return "account_quota", next
	}
	return "", next
}

// Brief contention gets a bounded wait before becoming a persisted deferral.
// Each attempt uses the shared atomic limiter, so business capacity remains authoritative.
func (s *IQCheckService) acquireIQSlot(ctx context.Context, accountID int64, limit int) (*AcquireResult, error) {
	for attempt := 0; ; attempt++ {
		slot, err := s.concurrency.AcquireAccountSlot(ctx, accountID, limit)
		if err != nil || slot == nil || slot.Acquired || attempt == 3 {
			return slot, err
		}
		timer := time.NewTimer((250 * time.Millisecond) << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *IQCheckService) execute(ctx context.Context, c IQCheckClaim) {
	defer s.notify()
	repo, ok := s.repo.(iqMonitoringRepository)
	if !ok {
		return
	}
	now := time.Now().UTC()
	deferUnsent := func(reason string, next time.Time) {
		if err := repo.DeferIQCheck(ctx, c, reason, next); err != nil {
			logger.LegacyPrintf("iq_check", "defer failed: account=%d err=%v", c.AccountID, err)
		}
	}
	a, err := s.accounts.GetByID(ctx, c.AccountID)
	if err != nil {
		deferUnsent("account_unavailable", now.Add(time.Minute))
		return
	}
	if reason, next := iqHealth(a, now); reason != "" {
		deferUnsent(reason, next)
		return
	}
	if next, reason := a.IQCheck.Eligibility(now); reason != "" {
		deferUnsent(reason, next)
		return
	}
	if s.concurrency == nil || s.concurrency.cache == nil {
		deferUnsent("concurrency_unavailable", now.Add(time.Minute))
		return
	}
	// Use any free slot immediately; a separate occupancy read can only become stale.
	slot, err := s.acquireIQSlot(ctx, a.ID, a.Concurrency)
	now = time.Now().UTC()
	if err != nil || slot == nil {
		if ctx.Err() != nil {
			return
		}
		deferUnsent("concurrency_unavailable", now.Add(time.Minute))
		return
	}
	if !slot.Acquired {
		deferUnsent("account_busy", now.Add(time.Duration(15+rand.IntN(16))*time.Second))
		return
	}
	defer slot.ReleaseFunc()
	now = time.Now().UTC()
	started, err := repo.StartIQCheck(ctx, c, now)
	if err != nil {
		deferUnsent("start_unavailable", now.Add(time.Minute))
		return
	}
	if !started {
		return
	}
	c.StartedAt = now
	timeout := c.TimeoutSeconds
	if timeout == 0 {
		timeout = 120
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	if c.RoundDeadline != nil && c.RoundDeadline.Before(deadline) {
		deadline = *c.RoundDeadline
	}
	probeCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	result := s.probe(probeCtx, c.AccountID, c)
	if ctx.Err() != nil {
		return
	}
	if err := s.repo.CompleteIQCheck(ctx, c, result, time.Now().UTC()); err != nil {
		logger.LegacyPrintf("iq_check", "complete failed: account=%d err=%v", c.AccountID, err)
	}
}

type IQCheckMetric struct {
	Bucket    time.Time `json:"bucket"`
	Event     string    `json:"event"`
	Reason    string    `json:"reason"`
	Count     int64     `json:"count"`
	LatencyMS int64     `json:"latency_ms"`
}

// Download contains no account identity or raw responses. Record retention bounds it to three rounds.
func (s *IQCheckService) Diagnostics(ctx context.Context, id int64) (any, error) {
	records, err := s.Records(ctx, id)
	if err != nil {
		return nil, err
	}
	type item struct {
		StartedAt  time.Time           `json:"started_at"`
		FinishedAt *time.Time          `json:"finished_at"`
		Status     string              `json:"status"`
		Reason     string              `json:"reason"`
		Profile    domain.IQProfile    `json:"profile"`
		Diagnostic *iqcheck.Diagnostic `json:"diagnostic,omitempty"`
		Attempts   []IQCheckAttempt    `json:"attempts"`
	}
	out := make([]item, 0, len(records))
	for _, r := range records {
		out = append(out, item{r.StartedAt, r.FinishedAt, r.Status, r.Reason, domain.IQProfile{Model: r.Model, ReasoningEffort: r.Effort, OutputMode: r.OutputMode}, r.Diagnostic.Bounded(), r.Attempts})
	}
	hourly := []IQCheckMetric{}
	if source, ok := s.repo.(interface {
		IQCheckMetrics(context.Context, int64) ([]IQCheckMetric, error)
	}); ok {
		hourly, err = source.IQCheckMetrics(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	return struct {
		Version int             `json:"version"`
		Rounds  any             `json:"rounds"`
		Hourly  []IQCheckMetric `json:"hourly"`
	}{2, out, hourly}, nil
}
