package service

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
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
	SchedAdminDecisions(context.Context, int64, int, int64) ([]SchedAdminDecision, error)
	SchedAdminRearEntries24h(context.Context, []int64, time.Time) (map[int64]SchedAdminRearEntries, error)
	SchedAdminGroupLast60(context.Context, []int64, time.Time) (map[int64]SchedAdminGroupLast60, error)
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
	ClearAccountCapabilityMarks(context.Context, int64) error
	ClearAccountStreamOnlyLearning(context.Context, int64) error
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
type schedAdminStatsEntry struct {
	at    time.Time
	value SchedAdminGroupLast60
}
type schedAdminStatsFlight struct {
	done  chan struct{}
	value SchedAdminGroupLast60
	err   error
}
type SchedAdminService struct {
	d            SchedAdminDependencies
	statsMu      sync.Mutex
	stats        map[int64]schedAdminStatsEntry
	statsFlights map[int64]*schedAdminStatsFlight
}

func NewSchedAdminService(d SchedAdminDependencies) *SchedAdminService {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &SchedAdminService{d: d, stats: map[int64]schedAdminStatsEntry{}, statsFlights: map[int64]*schedAdminStatsFlight{}}
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
	limit := schedquality.EffectiveErrorRate(r.Policy.MaxErrorRate)
	examples := make([]SchedAdminErrorTripExample, 0, 2)
	for _, known := range []int{50, 100} {
		for failed := 1; failed <= known; failed++ {
			if schedquality.WilsonLower(failed, known) > limit {
				examples = append(examples, SchedAdminErrorTripExample{Known: known, Failed: failed})
				break
			}
		}
	}
	return SchedAdminPolicy{GroupID: r.GroupID, Version: r.Version, Policy: schedAdminPolicyFields(r.Policy),
		EffectiveMaxErrorRate: limit, ErrorTripExamples: examples}
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
func (s *SchedAdminService) UpdatePolicy(ctx context.Context, g, version int64, p schedquality.Policy, op int64) (SchedAdminPolicySaved, error) {
	group, err := s.group(ctx, g)
	if err != nil {
		return SchedAdminPolicySaved{}, err
	}
	if group.Platform != PlatformOpenAI {
		return SchedAdminPolicySaved{}, infraerrors.BadRequest("INVALID_GROUP_PLATFORM", "Only OpenAI groups support scheduling policies")
	}
	if version < 0 {
		return SchedAdminPolicySaved{}, infraerrors.BadRequest("INVALID_SCHED_POLICY", "expected_version must be nonnegative")
	}
	if err := ValidateSchedQualityPolicy(p); err != nil {
		return SchedAdminPolicySaved{}, err
	}
	old, err := s.d.Engine.Policy(ctx, g)
	if err != nil {
		return SchedAdminPolicySaved{}, err
	}
	r, err := s.d.Engine.UpdatePolicy(ctx, g, version, p)
	if err != nil {
		return SchedAdminPolicySaved{}, err
	}
	lines := []schedquality.Dimension{}
	if old.Policy.Enabled != p.Enabled || old.Policy.TTFTSeconds != p.TTFTSeconds {
		lines = append(lines, schedquality.DimensionTTFO)
	}
	if old.Policy.Enabled != p.Enabled || old.Policy.MaxErrorRate != p.MaxErrorRate {
		lines = append(lines, schedquality.DimensionError)
	}
	if old.Policy.Enabled != p.Enabled || old.Policy.MinCacheRatio != p.MinCacheRatio {
		lines = append(lines, schedquality.DimensionCache)
	}
	if old.Policy != p {
		slog.InfoContext(ctx, "sched_quality.policy_changed", "component", "sched_quality", "group_id", g, "operator_id", op, "old", schedAdminPolicyFields(old.Policy), "new", schedAdminPolicyFields(p), "affected_lines", lines, "cleared", r.Cleared)
	}
	return SchedAdminPolicySaved{SchedAdminPolicy: schedAdminPolicy(r.SchedPolicyRecord), RelaxedLines: append([]schedquality.Dimension{}, r.RelaxedLines...), Cleared: r.Cleared}, nil
}
func schedAdminRule(r SchedTTFTRuleRecord) SchedAdminTTFTRule {
	return SchedAdminTTFTRule{Version: r.Version, RuleBParams: r.Params, Defaults: schedquality.DefaultRuleBParams(), Replayed: map[string][2]float64{"confidence": {.8, .95}, "tolerance": {.2, .25}, "peer_margin": {.05, .15}}}
}
func (s *SchedAdminService) TTFTRule(ctx context.Context) (SchedAdminTTFTRule, error) {
	r, err := s.d.Engine.TTFTRule(ctx)
	return schedAdminRule(r), err
}
func (s *SchedAdminService) UpdateTTFTRule(ctx context.Context, version int64, p schedquality.RuleBParams, op int64) (SchedAdminTTFTRuleSaved, error) {
	if version < 0 {
		return SchedAdminTTFTRuleSaved{}, infraerrors.BadRequest("INVALID_TTFT_RULE", "expected_version must be nonnegative")
	}
	if err := p.Validate(); err != nil {
		return SchedAdminTTFTRuleSaved{}, infraerrors.BadRequest("INVALID_TTFT_RULE", err.Error())
	}
	old, err := s.d.Engine.TTFTRule(ctx)
	if err != nil {
		return SchedAdminTTFTRuleSaved{}, err
	}
	r, err := s.d.Engine.UpdateTTFTRule(ctx, version, p)
	if err != nil {
		return SchedAdminTTFTRuleSaved{}, err
	}
	change := map[schedquality.RuleBParamChange]string{schedquality.RuleBUnchanged: "none", schedquality.RuleBRelaxed: "relax", schedquality.RuleBTightened: "tighten", schedquality.RuleBMixed: "mixed"}[r.Change]
	if old.Params != p {
		slog.InfoContext(ctx, "sched_quality.policy_changed", "component", "sched_quality", "operator_id", op, "old", old.Params, "new", p, "affected_lines", []string{"ttfo"}, "cleared", r.Cleared)
	}
	return SchedAdminTTFTRuleSaved{SchedAdminTTFTRule: schedAdminRule(r.SchedTTFTRuleRecord), Change: change, Cleared: r.Cleared}, nil
}
func (s *SchedAdminService) Decisions(ctx context.Context, id int64, limit int, before int64) ([]SchedAdminDecision, error) {
	if limit < 1 || limit > 200 || before < 0 {
		return nil, infraerrors.BadRequest("INVALID_DECISION_PAGE", "limit must be 1..200 and before_id nonnegative")
	}
	if _, err := s.account(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.d.Accounts.SchedAdminDecisions(ctx, id, limit, before)
	return append([]SchedAdminDecision{}, rows...), err
}
func SchedAdminDecisionBasis(b schedquality.Basis) *SchedAdminBasis {
	out := &SchedAdminBasis{SampleCounts: b.SampleCounts, TrialSeats: b.TrialSeats, RuleVersion: b.RuleVersion, Line: b.Line, Kind: b.Kind, Cut: schedAdminTime(b.Cut), Threshold: b.Threshold, SourceGroups: append([]int64{}, b.SourceGroups...)}
	if b.TTFT != nil {
		e := *b.TTFT
		e.Causes = append([]schedquality.RuleBUnknownReason{}, e.Causes...)
		out.TTFT = &e
	}
	if b.Error != nil {
		e := schedAdminErrorEvidence(*b.Error)
		out.Error = &e
	}
	if b.Cache != nil {
		e := schedAdminCacheEvidence(*b.Cache)
		out.Cache = &e
	}
	return out
}
