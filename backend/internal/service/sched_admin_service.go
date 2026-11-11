package service

import (
	"context"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

// SchedAdminAccountRepository adds conditional, narrowly scoped mutations to account reads.
type SchedAdminAccountRepository interface {
	GetByID(context.Context, int64) (*Account, error)
	GetByIDs(context.Context, []int64) ([]*Account, error)
	SchedAdminClearError(context.Context, int64, string) (bool, error)
	SchedAdminClear429(context.Context, int64, *time.Time, *time.Time) (bool, error)
	SchedAdminClearOverload(context.Context, int64, *time.Time) (bool, error)
	SchedAdminClearTemp(context.Context, int64, string, *time.Time) (bool, error)
	SchedAdminClearModelLimit(context.Context, int64, string, any) (bool, error)
	SchedAdminProxyPeers(context.Context, int64, int64) (int, error)
}
type SchedAdminGroupRepository interface {
	GetByID(context.Context, int64) (*Group, error)
}
type SchedAdminAudit interface{ Record(*AuditLog) }
type SchedAdminCapability interface {
	ClearAccountCapabilityLearning(context.Context, int64) error
}

type SchedAdminDependencies struct {
	Engine     SchedQualityEngine
	Board      SchedSelectionBoard
	Transient  SchedModelTransientAdmin
	Accounts   SchedAdminAccountRepository
	Groups     SchedAdminGroupRepository
	Attempts   SchedAttemptStore
	RateLimit  *RateLimitService
	Resilience *ResilienceService
	Gateway    *OpenAIGatewayService
	Capability SchedAdminCapability
	Tests      SchedTestRunner
	Audit      SchedAdminAudit
	InFlight   func(context.Context, int64) (int, error)
	Thresholds func(context.Context) map[string]int
	Now        func() time.Time
}
type SchedAdminService struct{ d SchedAdminDependencies }

func NewSchedAdminService(d SchedAdminDependencies) *SchedAdminService {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &SchedAdminService{d: d}
}
func (s *SchedAdminService) snapshot() *SchedQualitySnapshot {
	if p := s.d.Engine.Snapshot(); p != nil {
		return p
	}
	return &SchedQualitySnapshot{}
}
func schedAdminTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := t.UTC().Format(time.RFC3339Nano)
	return &v
}
func schedAdminStamp(t time.Time) string {
	if v := schedAdminTime(t); v != nil {
		return *v
	}
	return ""
}
func schedAdminEnough(n, sessions int) bool {
	return n >= schedquality.MinSamples && sessions >= schedquality.MinSessions
}
func schedAdminInt(v int) *int           { return &v }
func schedAdminFloat(v float64) *float64 { return &v }
func schedAdminString(v string) *string  { return &v }
func (s *SchedAdminService) account(ctx context.Context, id int64) (*Account, error) {
	if id <= 0 {
		return nil, infraerrors.BadRequest("INVALID_ACCOUNT_ID", "Invalid account id")
	}
	a, e := s.d.Accounts.GetByID(ctx, id)
	if e != nil {
		return nil, e
	}
	if a == nil {
		return nil, ErrAccountNotFound
	}
	return a, nil
}
func (s *SchedAdminService) group(ctx context.Context, id int64) (*Group, error) {
	if id <= 0 {
		return nil, infraerrors.BadRequest("INVALID_GROUP_ID", "Invalid group id")
	}
	g, e := s.d.Groups.GetByID(ctx, id)
	if e != nil {
		return nil, e
	}
	if g == nil {
		return nil, ErrGroupNotFound
	}
	return g, nil
}
func (s *SchedAdminService) audit(op int64, action string, extra map[string]any) {
	if s.d.Audit != nil {
		s.d.Audit.Record(&AuditLog{ActorUserID: &op, Action: action, CreatedAt: s.d.Now().UTC(), StatusCode: 200, Extra: extra})
	}
}
func schedAdminMember(a *Account, g int64) bool {
	if g == 0 {
		return true
	}
	for _, id := range a.GroupIDs {
		if id == g {
			return true
		}
	}
	for _, x := range a.Groups {
		if x != nil && x.ID == g {
			return true
		}
	}
	return false
}
func (s *SchedAdminService) SetOverride(ctx context.Context, o SchedOverride) error {
	a, e := s.account(ctx, o.AccountID)
	if e != nil {
		return e
	}
	if o.GroupID > 0 {
		if _, e := s.group(ctx, o.GroupID); e != nil {
			return e
		}
	}
	if o.GroupID < 0 || !schedAdminMember(a, o.GroupID) {
		return infraerrors.BadRequest("INVALID_GROUP_MEMBERSHIP", "Account does not belong to this group")
	}
	if o.Mode != schedquality.OverrideExempt && o.Mode != schedquality.OverrideDemote {
		return infraerrors.BadRequest("INVALID_OVERRIDE", "Invalid override mode")
	}
	o.Reason = strings.TrimSpace(o.Reason)
	if o.Reason == "" || (o.ExpiresAt != nil && !o.ExpiresAt.After(s.d.Now())) {
		return infraerrors.BadRequest("INVALID_OVERRIDE", "Reason and a future expiry are required")
	}
	if e = s.d.Engine.SetOverride(ctx, o); e != nil {
		return e
	}
	s.audit(o.OperatorID, "sched_quality.override", map[string]any{"account_id": o.AccountID, "group_id": o.GroupID, "mode": schedAdminOverrideMode(o.Mode), "expires_at": o.ExpiresAt, "reason": o.Reason})
	return nil
}
func (s *SchedAdminService) ClearOverride(ctx context.Context, a, g, op int64) error {
	account, e := s.account(ctx, a)
	if e != nil {
		return e
	}
	if g > 0 {
		if _, e := s.group(ctx, g); e != nil {
			return e
		}
	}
	if g < 0 || !schedAdminMember(account, g) {
		return infraerrors.BadRequest("INVALID_GROUP_MEMBERSHIP", "Account does not belong to this group")
	}
	if e = s.d.Engine.ClearOverride(ctx, a, g); e != nil {
		return e
	}
	s.audit(op, "sched_quality.clear_override", map[string]any{"account_id": a, "group_id": g})
	return nil
}
func schedAdminOverrideMode(m schedquality.Override) string {
	if m == schedquality.OverrideExempt {
		return "exempt"
	}
	if m == schedquality.OverrideDemote {
		return "demote"
	}
	return ""
}
func (s *SchedAdminService) override(p *SchedQualitySnapshot, a, g int64, now time.Time) *SchedAdminOverride {
	var all, group *SchedOverride
	for i := range p.Overrides {
		o := &p.Overrides[i]
		if o.AccountID != a || (o.ExpiresAt != nil && !now.Before(*o.ExpiresAt)) {
			continue
		}
		if o.GroupID == g {
			group = o
		}
		if o.GroupID == 0 {
			all = o
		}
	}
	o := group
	if o == nil {
		o = all
	}
	if o == nil {
		return nil
	}
	var expiry *string
	if o.ExpiresAt != nil {
		expiry = schedAdminTime(*o.ExpiresAt)
	}
	return &SchedAdminOverride{Mode: schedAdminOverrideMode(o.Mode), AllGroups: o.GroupID == 0, ExpiresAt: expiry, Reason: o.Reason}
}
func schedAdminModels(p *SchedQualitySnapshot, id int64, group bool) []string {
	counts := map[string]int{}
	if !group {
		for k, v := range p.Evidence {
			if k.AccountID == id && v.Samples > 0 {
				counts[k.Model] += v.Samples
			}
		}
	} else {
		for a, rows := range p.GroupRows {
			seen := map[string]bool{}
			for _, r := range rows {
				if r.GroupID == id && !seen[r.Model] {
					seen[r.Model] = true
					v := p.Evidence[SchedAccountModel{a, r.Model}]
					if v.Samples > 0 {
						counts[r.Model] += v.Samples
					}
				}
			}
		}
	}
	out := make([]string, 0, len(counts))
	for m := range counts {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if counts[out[i]] == counts[out[j]] {
			return out[i] < out[j]
		}
		return counts[out[i]] > counts[out[j]]
	})
	return out
}
func (s *SchedAdminService) Test(ctx context.Context, id int64, model string, op int64) (SchedAdminTest, error) {
	out := SchedAdminTest{Tests: []SchedAdminTestTests{}}
	a, e := s.account(ctx, id)
	if e != nil {
		return out, e
	}
	if s.d.Tests == nil {
		return out, infraerrors.ServiceUnavailable("SCHED_TEST_UNAVAILABLE", "Test runner is unavailable")
	}
	p := s.snapshot()
	models := []string{}
	if strings.TrimSpace(model) != "" {
		models = append(models, strings.TrimSpace(model))
	} else {
		models = schedAdminModels(p, id, false)
		if len(models) > schedquality.ManualTestMaxModels {
			models = models[:schedquality.ManualTestMaxModels]
		}
		if len(models) == 0 {
			counts := map[string]int{}
			for account, rows := range p.GroupRows {
				seen := map[string]bool{}
				for _, r := range rows {
					if schedAdminMember(a, r.GroupID) && !seen[r.Model] {
						seen[r.Model] = true
						counts[r.Model] += p.Evidence[SchedAccountModel{account, r.Model}].Samples
					}
				}
			}
			best := ""
			for m, n := range counts {
				if n > 0 && (best == "" || n > counts[best] || (n == counts[best] && m < best)) {
					best = m
				}
			}
			if best != "" {
				models = append(models, best)
			}
		}
	}
	for _, m := range models {
		t, e := s.d.Tests.Request(ctx, id, m, false, op)
		if e != nil {
			return out, e
		}
		out.Tests = append(out.Tests, SchedAdminTestTests{Model: t.Model, TestID: t.TestID, Started: t.Started, Why: t.Why})
	}
	s.audit(op, "sched_quality.test", map[string]any{"account_id": id, "models": models, "tickets": out.Tests})
	return out, nil
}
func schedAdminPolicy(r SchedPolicyRecord) SchedAdminPolicy {
	p := r.Policy
	out := SchedAdminPolicy{GroupID: r.GroupID, Version: r.Version, Policy: SchedAdminPolicyPolicy{Enabled: p.Enabled, TTFTSeconds: p.TTFTSeconds, MaxErrorRate: p.MaxErrorRate, MinCacheRatio: p.MinCacheRatio, TakeoverIdleSeconds: p.TakeoverIdleSeconds}, ClassLimitsMs: map[string]SchedAdminClassLimits{}}
	for model, v := range schedquality.ClassScaleTable {
		out.ClassLimitsMs[model] = SchedAdminClassLimits{Light: int(math.Round(p.TTFTSeconds * 1000 * v[0])), Regular: int(math.Round(p.TTFTSeconds * 1000 * v[1])), Heavy: int(math.Round(p.TTFTSeconds * 1000 * v[2])), Xheavy: int(math.Round(p.TTFTSeconds * 1000 * v[3]))}
	}
	return out
}
func (s *SchedAdminService) Policy(ctx context.Context, g int64) (SchedAdminPolicy, error) {
	if _, e := s.group(ctx, g); e != nil {
		return SchedAdminPolicy{}, e
	}
	r, e := s.d.Engine.Policy(ctx, g)
	return schedAdminPolicy(r), e
}
func (s *SchedAdminService) UpdatePolicy(ctx context.Context, g, version int64, p schedquality.Policy, op int64) (SchedAdminPolicy, error) {
	group, e := s.group(ctx, g)
	if e != nil {
		return SchedAdminPolicy{}, e
	}
	if group.Platform != PlatformOpenAI {
		return SchedAdminPolicy{}, infraerrors.BadRequest("INVALID_GROUP_PLATFORM", "Only OpenAI groups support scheduling policies")
	}
	if version < 0 || math.IsNaN(p.TTFTSeconds) || math.IsInf(p.TTFTSeconds, 0) || math.IsNaN(p.MaxErrorRate) || math.IsInf(p.MaxErrorRate, 0) || math.IsNaN(p.MinCacheRatio) || math.IsInf(p.MinCacheRatio, 0) || p.TTFTSeconds < schedquality.PolicyTTFTMinSeconds || p.TTFTSeconds > schedquality.PolicyTTFTMaxSeconds || p.MaxErrorRate < 0 || p.MaxErrorRate > 1 || p.MinCacheRatio < 0 || p.MinCacheRatio > 1 || !slices.Contains(schedquality.TakeoverIdleChoices[:], p.TakeoverIdleSeconds) {
		return SchedAdminPolicy{}, infraerrors.BadRequest("INVALID_POLICY", "Invalid scheduling policy")
	}
	r, e := s.d.Engine.UpdatePolicy(ctx, g, version, p)
	if e != nil {
		return SchedAdminPolicy{}, e
	}
	s.audit(op, "sched_quality.policy", map[string]any{"group_id": g, "version": r.Version, "policy": p})
	return schedAdminPolicy(r), nil
}
