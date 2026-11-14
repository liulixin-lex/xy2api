package service

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
)

// SchedAdminAccountRepository adds conditional, narrowly scoped mutations to account reads.
type SchedAdminAccountRepository interface {
	GetByID(context.Context, int64) (*Account, error)
	GetByIDs(context.Context, []int64) ([]*Account, error)
	SchedAdminAccountIDs(context.Context) ([]int64, error)
	SchedAdminClearError(context.Context, int64, string) (bool, error)
	SchedAdminClear429(context.Context, int64, *time.Time, *time.Time) (bool, error)
	SchedAdminClearOverload(context.Context, int64, *time.Time) (bool, error)
	SchedAdminClearTemp(context.Context, int64, string, *time.Time) (bool, error)
	SchedAdminClearModelLimit(context.Context, int64, string, any) (bool, error)
	SchedAdminProxyPeers(context.Context, int64, int64) (int, error)
}
type SchedAdminGroupRepository interface {
	GetByID(context.Context, int64) (*Group, error)
	GetAccountIDsByGroupIDs(context.Context, []int64) ([]int64, error)
}
type SchedAdminCapability interface {
	ClearAccountCapabilityLearning(context.Context, int64) error
}

type SchedAdminDependencies struct {
	Engine    SchedQualityEngine
	Board     SchedSelectionBoard
	Transient SchedModelTransientAdmin
	Accounts  SchedAdminAccountRepository
	Groups    SchedAdminGroupRepository
	Usage     interface {
		GroupLast60(context.Context, []int64, time.Time) (map[int64]usagestats.GroupLast60, error)
	}
	RateLimit  *RateLimitService
	Resilience *ResilienceService
	Gateway    *OpenAIGatewayService
	Capability SchedAdminCapability
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
func schedAdminFloat(v float64) *float64 { return &v }
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
	if o.Mode != schedquality.OverrideExempt {
		return infraerrors.BadRequest("INVALID_OVERRIDE", "Invalid override mode")
	}
	o.Reason = operatoraccess.StripURLUserinfo(strings.TrimSpace(o.Reason))
	if o.ExpiresAt != nil && !o.ExpiresAt.After(s.d.Now()) {
		return infraerrors.BadRequest("INVALID_OVERRIDE", "Expiry must be in the future")
	}
	if e = s.d.Engine.SetOverride(ctx, o); e != nil {
		return e
	}
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
	return nil
}
func schedAdminPolicy(r SchedPolicyRecord) SchedAdminPolicy {
	return SchedAdminPolicy{GroupID: r.GroupID, Version: r.Version, Policy: schedAdminPolicyFields(r.Policy)}
}
func schedAdminGroupPolicy(p *SchedQualitySnapshot, group int64) schedquality.Policy {
	if record, ok := p.Policies[group]; ok {
		return record.Policy
	}
	return schedquality.DefaultPolicy()
}
func schedAdminPolicyFields(p schedquality.Policy) SchedAdminPolicyPolicy {
	return SchedAdminPolicyPolicy{Enabled: p.Enabled, TTFTSeconds: p.TTFTSeconds, MaxErrorRate: p.MaxErrorRate, MinCacheRatio: p.MinCacheRatio, TakeoverIdleSeconds: p.TakeoverIdleSeconds}
}
func (s *SchedAdminService) exemptions(ctx context.Context, p *SchedQualitySnapshot, a *Account, now time.Time) []SchedAdminExemption {
	out := []SchedAdminExemption{}
	for _, o := range p.Overrides {
		if o.AccountID != a.ID || o.Mode != schedquality.OverrideExempt || !schedAdminMember(a, o.GroupID) || (o.ExpiresAt != nil && !now.Before(*o.ExpiresAt)) {
			continue
		}
		reason := operatoraccess.StripURLUserinfo(o.Reason)
		if scope := operatoraccess.ScopeFrom(ctx); scope != nil && !scope.Has(operatoraccess.FlagAccountsUpstreamAddress) {
			reason = operatoraccess.ScrubURLs(reason)
		}
		x := SchedAdminExemption{GroupID: o.GroupID, Reason: reason}
		if o.ExpiresAt != nil {
			x.ExpiresAt = schedAdminTime(*o.ExpiresAt)
		}
		out = append(out, x)
	}
	slices.SortFunc(out, func(a, b SchedAdminExemption) int { return cmp.Compare(a.GroupID, b.GroupID) })
	return out
}
func schedAdminExemption(rows []SchedAdminExemption, g int64) *SchedAdminExemption {
	var all *SchedAdminExemption
	for _, o := range rows {
		if o.GroupID == g {
			return &o
		}
		if o.GroupID == 0 {
			all = &o
		}
	}
	return all
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
	if version < 0 {
		return SchedAdminPolicy{}, infraerrors.BadRequest("INVALID_SCHED_POLICY", "Invalid expected version")
	}
	if e := ValidateSchedQualityPolicy(p); e != nil {
		return SchedAdminPolicy{}, e
	}
	r, e := s.d.Engine.UpdatePolicy(ctx, g, version, p)
	if e != nil {
		return SchedAdminPolicy{}, e
	}
	return schedAdminPolicy(r), nil
}
