package service

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
)

type IQQuotaPolicy struct {
	DailyLimit         int `json:"daily_limit"`
	MinIntervalSeconds int `json:"min_interval_seconds"`
}
type iqMonitoringRepository interface {
	StartIQCheck(context.Context, IQCheckClaim, time.Time, map[string]IQQuotaPolicy) (bool, error)
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

func iqMonitoringConfig() (int, map[string]IQQuotaPolicy) {
	n := 2
	if v, e := strconv.Atoi(os.Getenv("IQ_CHECK_MAX_CONCURRENCY")); e == nil && v >= 1 && v <= 10 {
		n = v
	}
	groups := map[string]IQQuotaPolicy{}
	// Invalid quota configuration must not silently permit any grouped requests.
	raw := os.Getenv("IQ_CHECK_QUOTA_GROUPS")
	if len(raw) <= 65536 && raw != "" {
		var parsed map[string]IQQuotaPolicy
		if json.Unmarshal([]byte(raw), &parsed) == nil {
			for k, v := range parsed {
				if v.DailyLimit > 0 && v.DailyLimit <= 1000000 && v.MinIntervalSeconds >= 1 && v.MinIntervalSeconds <= 86400 {
					groups[k] = v
				}
			}
		}
	}
	return n, groups
}

func iqHealth(a *Account, now time.Time) (string, time.Time) {
	next := now.Add(time.Minute)
	if !a.IsActive() || !a.Schedulable {
		return "account_disabled", next
	}
	if a.AutoPauseOnExpired && a.ExpiresAt != nil && !a.ExpiresAt.After(now) {
		return "account_expired", next
	}
	for _, t := range []*time.Time{a.OverloadUntil, a.RateLimitResetAt, a.TempUnschedulableUntil} {
		if t != nil && t.After(now) {
			if t.After(next) {
				next = *t
			}
			return "account_cooldown", next
		}
	}
	if a.IsAPIKeyOrBedrock() && a.IsQuotaExceeded() {
		return "account_quota", next
	}
	return "", next
}

func (s *IQCheckService) execute(ctx context.Context, c IQCheckClaim) {
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
	if s.concurrency == nil {
		deferUnsent("concurrency_unavailable", now.Add(time.Minute))
		return
	}
	// Existing traffic takes priority. The subsequent atomic acquisition enforces the actual cap.
	busy, err := s.concurrency.cache.GetAccountConcurrency(ctx, a.ID)
	if err != nil || busy > 0 {
		deferUnsent("account_busy", now.Add(time.Minute))
		return
	}
	slot, err := s.concurrency.AcquireAccountSlot(ctx, a.ID, a.Concurrency)
	if err != nil || slot == nil || !slot.Acquired {
		deferUnsent("account_busy", now.Add(time.Minute))
		return
	}
	defer slot.ReleaseFunc()
	started, err := repo.StartIQCheck(ctx, c, now, s.quotaGroups)
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
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	result := s.probe(probeCtx, c.AccountID, c)
	if ctx.Err() != nil {
		return
	}
	if err := s.repo.CompleteIQCheck(ctx, c, result, time.Now().UTC()); err != nil {
		logger.LegacyPrintf("iq_check", "complete failed: account=%d err=%v", c.AccountID, err)
	}
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
	}
	out := make([]item, 0, len(records))
	for _, r := range records {
		out = append(out, item{r.StartedAt, r.FinishedAt, r.Status, r.Reason, domain.IQProfile{Model: r.Model, ReasoningEffort: r.Effort, OutputMode: r.OutputMode}, r.Diagnostic.Bounded()})
	}
	return out, nil
}
