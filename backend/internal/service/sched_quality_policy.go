package service

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

// ValidateSchedQualityPolicy validates the persisted v4 fields without normalizing them.
func ValidateSchedQualityPolicy(p schedquality.Policy) error {
	valid := func(v, lo, hi float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= lo && v <= hi }
	if math.IsNaN(p.TTFTSeconds) || math.IsInf(p.TTFTSeconds, 0) || p.TTFTSeconds <= 0 {
		return infraerrors.BadRequest("INVALID_SCHED_POLICY", "ttft_seconds must be positive and finite")
	}
	if math.IsNaN(p.MaxErrorRate) || math.IsInf(p.MaxErrorRate, 0) || p.MaxErrorRate > 1 {
		return infraerrors.BadRequest("INVALID_SCHED_POLICY", "max_error_rate must be finite and no greater than 1")
	}
	if p.MaxErrorRate < .05 {
		return infraerrors.BadRequest("SCHED_ERROR_RATE_TOO_LOW", "max_error_rate must be at least 0.05")
	}
	if !valid(p.MinCacheRatio, 0, 1) {
		return infraerrors.BadRequest("INVALID_SCHED_POLICY", "min_cache_ratio must be between 0 and 1")
	}
	if !slices.Contains(schedquality.TakeoverIdleChoices[:], p.TakeoverIdleSeconds) {
		return infraerrors.BadRequest("INVALID_SCHED_POLICY", "invalid takeover_idle_seconds")
	}
	return nil
}

func (s *SchedQualityEngineService) Policy(ctx context.Context, groupID int64) (SchedPolicyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshConfig(ctx, s.now()); err != nil {
		return SchedPolicyRecord{}, err
	}
	if _, ok := s.members[groupID]; !ok {
		return SchedPolicyRecord{}, infraerrors.BadRequest("INVALID_SCHED_GROUP", "quality policies require an OpenAI group")
	}
	p, ok := s.policies[groupID]
	if !ok {
		p.Policy = schedquality.DefaultPolicy()
	}
	p.GroupID = groupID
	return p, nil
}

func (s *SchedQualityEngineService) UpdatePolicy(ctx context.Context, groupID, expectedVersion int64, p schedquality.Policy) (SchedPolicyRecord, error) {
	if err := ValidateSchedQualityPolicy(p); err != nil {
		return SchedPolicyRecord{}, err
	}
	s.mu.Lock()
	var notifications map[int64]time.Time
	defer func() { s.mu.Unlock(); s.notify(notifications) }()
	// The store verifies the group platform before saving.
	r, err := s.store.SavePolicy(ctx, groupID, expectedVersion, p)
	if err != nil {
		return r, err
	}
	s.configAt = time.Time{}
	notifications, err = s.rebuild(ctx)
	if err != nil {
		return r, err
	}
	return r, nil
}

func (s *SchedQualityEngineService) refreshConfig(ctx context.Context, now time.Time) error {
	if !s.configAt.IsZero() && now.Sub(s.configAt) < min(schedquality.MembersCacheTTL, schedquality.PoliciesCacheTTL) {
		return nil
	}
	policies, err := s.store.LoadPolicies(ctx)
	if err != nil {
		return err
	}
	members, err := s.store.LoadMembers(ctx)
	if err != nil {
		return err
	}
	s.policies, s.members, s.configAt = policies, members, now
	return nil
}

// PreviewPolicy evaluates only ordinary rules at the last processed UTC minute.
// The admin service adds account names, hard admission and exemption counts.
func (s *SchedQualityEngineService) PreviewPolicy(ctx context.Context, group int64, p schedquality.Policy) ([]SchedPolicyChange, error) {
	if err := ValidateSchedQualityPolicy(p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshConfig(ctx, s.now()); err != nil {
		return nil, err
	}
	if _, ok := s.members[group]; !ok {
		return nil, infraerrors.BadRequest("INVALID_SCHED_GROUP", "quality policies require an OpenAI group")
	}
	snapshot := s.snapshot.Load()
	at := s.processedAt
	if at.IsZero() {
		at = snapshot.At.Add(-10 * time.Second)
	}
	at = at.Truncate(time.Minute)
	out := make([]SchedPolicyChange, 0)
	for _, account := range s.members[group] {
		for _, row := range snapshot.RowsForAccount(account) {
			if row.GroupID != group || row.View.Failing {
				continue
			}
			evaluation := schedquality.EvaluateAt(s.window.samples[SchedAccountModel{account, row.Model}], schedquality.Scope{AccountID: account, GroupID: group, Model: row.Model}, p, row.View.EvidenceSince, at, schedquality.EvaluateRegular)
			if dims := evaluation.Dimensions(); len(dims) > 0 {
				out = append(out, SchedPolicyChange{AccountID: account, Model: row.Model, Dims: dims})
			}
		}
	}
	return out, nil
}
