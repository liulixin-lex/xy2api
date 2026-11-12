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
	if !valid(p.TTFTSeconds, schedquality.PolicyTTFTMinSeconds, schedquality.PolicyTTFTMaxSeconds) {
		return infraerrors.BadRequest("INVALID_SCHED_POLICY", "ttft_seconds must be between 0.5 and 600")
	}
	if !valid(p.MaxErrorRate, 0, 1) {
		return infraerrors.BadRequest("INVALID_SCHED_POLICY", "max_error_rate must be between 0 and 1")
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
	p := s.policies[groupID]
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
