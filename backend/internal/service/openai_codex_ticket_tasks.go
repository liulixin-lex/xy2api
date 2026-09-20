package service

import (
	"errors"
	"time"
)

var errCodexExecutionSlot = errors.New("account concurrency unavailable")

type CodexTicketTask struct {
	ID         string     `json:"id"`
	RequestID  string     `json:"request_id,omitempty"`
	Source     string     `json:"source"`
	State      string     `json:"state"`
	WaitReason string     `json:"wait_reason,omitempty"`
	RetryAt    *time.Time `json:"retry_at,omitempty"`
	ProxyID    string     `json:"proxy_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Attempts   int        `json:"attempts"`
}

func (t *CodexTicketTask) active() bool { return t != nil && t.FinishedAt == nil }
func (t *CodexTicketTask) finish(state string, now time.Time) {
	if t == nil {
		return
	}
	t.State = state
	t.FinishedAt = &now
	t.WaitReason = ""
	t.RetryAt = nil
}
func codexTaskStatus(a *Account, rt codexTicketRuntime, now time.Time) *CodexTicketTask {
	if rt.PendingManual != nil {
		task := *rt.PendingManual
		task.State = "queued"
		task.WaitReason = "execution_slot"
		return &task
	}
	if rt.Task == nil {
		return nil
	}
	task := *rt.Task
	if !task.active() {
		return &task
	}
	if cooldown := codexTicketAccountRetryAfter(a, now); cooldown != nil {
		task.State = "waiting"
		task.WaitReason = "account_cooldown"
		task.RetryAt = cooldown
		return &task
	}
	if rt.HardRetryAfter != nil && rt.HardRetryAfter.After(now) {
		task.State = "waiting"
		task.WaitReason = "upstream_capacity"
		task.RetryAt = rt.HardRetryAfter
		return &task
	}
	if reason, until := iqHealth(a, now); reason != "" {
		task.State = "waiting"
		task.WaitReason = "account_health"
		if !until.IsZero() {
			task.RetryAt = &until
		}
		return &task
	}
	if rt.LeaseUntil != nil && rt.LeaseUntil.After(now) {
		return &task
	}
	if rt.NextAttemptAt != nil && rt.NextAttemptAt.After(now) {
		task.State = "waiting"
		if task.WaitReason == "" {
			task.WaitReason = "attempt_interval"
		}
		task.RetryAt = rt.NextAttemptAt
		return &task
	}
	task.State = "queued"
	task.WaitReason = "execution_slot"
	return &task
}

func activateCodexPendingManual(rt *codexTicketRuntime) {
	if rt.PendingManual == nil {
		return
	}
	rt.Task = rt.PendingManual
	rt.PendingManual = nil
	rt.RequestedProxyID = rt.PendingProxyID
	rt.PendingProxyID = ""
	rt.Requested = true
	rt.NextAttemptAt = nil
	rt.RetryAfter = nil
}
