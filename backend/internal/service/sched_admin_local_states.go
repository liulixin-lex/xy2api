package service

import (
	"context"
	"time"
)

func (r *ResilienceService) schedAdminResetLocalStates(id int64) []SchedAutoState {
	out := []SchedAutoState{}
	if r == nil {
		return out
	}
	r.sameSiteHold.mu.RLock()
	_, sameSite := r.sameSiteHold.accounts[id]
	r.sameSiteHold.mu.RUnlock()
	if sameSite {
		out = append(out, SchedAutoState{Key: "same_site", Kind: "same_site"})
	}
	r.postOutput.mu.Lock()
	_, postOutput := r.postOutput.failures[id]
	r.postOutput.mu.Unlock()
	if postOutput {
		out = append(out, SchedAutoState{Key: "post_output", Kind: "post_output"})
	}
	return out
}

func (r *ResilienceService) schedAdminLocalStates(a *Account, now time.Time) []SchedAutoState {
	out := []SchedAutoState{}
	if r == nil {
		return out
	}
	id := a.ID
	if until := r.sameSiteHold.until(a, now); !until.IsZero() {
		out = append(out, SchedAutoState{Key: "same_site", Kind: "same_site", Scope: "account", Until: &until, Clearable: true})
	}
	r.postOutput.mu.Lock()
	count := 0
	for _, at := range r.postOutput.failures[id] {
		if now.Sub(at) < postOutputCooldownWindow {
			count++
		}
	}
	r.postOutput.mu.Unlock()
	if count > 0 {
		out = append(out, SchedAutoState{Key: "post_output", Kind: "post_output", Scope: "account", Count: &[2]int{count, postOutputCooldownThreshold}, Clearable: true})
	}
	return out
}
func (r *ResilienceService) schedAdminClearSameSite(id int64) bool {
	if r == nil {
		return false
	}
	r.sameSiteHold.mu.Lock()
	defer r.sameSiteHold.mu.Unlock()
	_, ok := r.sameSiteHold.accounts[id]
	delete(r.sameSiteHold.accounts, id)
	return ok
}
func (r *ResilienceService) schedAdminClearPostOutput(id int64) bool {
	if r == nil {
		return false
	}
	r.postOutput.mu.Lock()
	defer r.postOutput.mu.Unlock()
	_, ok := r.postOutput.failures[id]
	delete(r.postOutput.failures, id)
	return ok
}
func (s *SchedAdminService) ClearLocal(id int64, at time.Time) {
	if s.d.Transient != nil {
		s.d.Transient.ClearAll(id)
	}
	if r := s.d.Resilience; r != nil {
		r.schedAdminClearSameSite(id)
		r.schedAdminClearPostOutput(id)
	}
	if s.d.Gateway != nil {
		s.d.Gateway.ClearAccountSchedulingBlock(id)
	}
	if s.d.RateLimit != nil {
		s.d.RateLimit.notifyAccountSchedulingBlockCleared(id)
	}
}
func (s *SchedAdminService) schedAdminCounters(ctx context.Context, id int64, source string) error {
	if source == "post_output" && s.d.Resilience != nil {
		s.d.Resilience.schedAdminClearPostOutput(id)
	}
	rl := s.d.RateLimit
	if rl == nil {
		return nil
	}
	switch source {
	case "403":
		if rl.openAI403CounterCache != nil {
			return rl.openAI403CounterCache.ResetOpenAI403Count(ctx, id)
		}
	case "stream_timeout":
		if rl.timeoutCounterCache != nil {
			return rl.timeoutCounterCache.ResetTimeoutCount(ctx, id)
		}
	}
	return nil
}
