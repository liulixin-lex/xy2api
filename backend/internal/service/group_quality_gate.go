package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// GroupQualityGateTier values are used when low-upstream-rate priority is enabled.
const (
	GroupQualityGateTierFast = iota
	GroupQualityGateTierTrial
	GroupQualityGateTierOther
)

const (
	groupQualityDefaultErrorRate = 0.10
	groupQualityDefaultEjectMs   = 20000
	groupQualityDefaultFastMs    = 10000
)

// GroupQualityGatePolicy configures quality evaluation for one group.
type GroupQualityGatePolicy struct {
	Enabled           bool    `json:"enabled"`
	MaxErrorRate      float64 `json:"max_error_rate"`
	MaxWarmTTFTP90Ms  int     `json:"max_warm_ttft_p90_ms"`
	FastWarmTTFTP90Ms int     `json:"fast_warm_ttft_p90_ms"`
	PinnedAccountIDs  []int64 `json:"pinned_account_ids"`
}

// GroupQualityGatePolicies is the versioned setting payload.
type GroupQualityGatePolicies struct {
	Version int                               `json:"version"`
	Groups  map[string]GroupQualityGatePolicy `json:"groups"`
}

func defaultGroupQualityGatePolicy() GroupQualityGatePolicy {
	return GroupQualityGatePolicy{MaxErrorRate: groupQualityDefaultErrorRate, MaxWarmTTFTP90Ms: groupQualityDefaultEjectMs, FastWarmTTFTP90Ms: groupQualityDefaultFastMs}
}

func normalizeGroupQualityGatePolicy(p GroupQualityGatePolicy) GroupQualityGatePolicy {
	d := defaultGroupQualityGatePolicy()
	if p.MaxErrorRate == 0 {
		p.MaxErrorRate = d.MaxErrorRate
	}
	if p.MaxWarmTTFTP90Ms == 0 {
		p.MaxWarmTTFTP90Ms = d.MaxWarmTTFTP90Ms
	}
	if p.FastWarmTTFTP90Ms == 0 {
		p.FastWarmTTFTP90Ms = d.FastWarmTTFTP90Ms
	}
	ids := append([]int64(nil), p.PinnedAccountIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	p.PinnedAccountIDs = ids[:0]
	for _, id := range ids {
		if len(p.PinnedAccountIDs) == 0 || p.PinnedAccountIDs[len(p.PinnedAccountIDs)-1] != id {
			p.PinnedAccountIDs = append(p.PinnedAccountIDs, id)
		}
	}
	return p
}

// ValidateGroupQualityGatePolicy checks the setting independent of HTTP.
func ValidateGroupQualityGatePolicy(policy GroupQualityGatePolicy, group *Group, memberIDs []int64) error {
	policy = normalizeGroupQualityGatePolicy(policy)
	if policy.Enabled && (group == nil || group.Platform != PlatformOpenAI) {
		return errors.New("quality gate can only be enabled for OpenAI groups")
	}
	if policy.MaxErrorRate <= 0 || policy.MaxErrorRate > 1 || math.IsNaN(policy.MaxErrorRate) || math.IsInf(policy.MaxErrorRate, 0) {
		return errors.New("max_error_rate must be in (0,1]")
	}
	if policy.FastWarmTTFTP90Ms < 1000 || policy.FastWarmTTFTP90Ms >= policy.MaxWarmTTFTP90Ms || policy.MaxWarmTTFTP90Ms > 600000 {
		return errors.New("warm TTFT thresholds are invalid")
	}
	members := map[int64]bool{}
	for _, id := range memberIDs {
		members[id] = true
	}
	for _, id := range policy.PinnedAccountIDs {
		if !members[id] {
			return fmt.Errorf("pinned account %d is not a group member", id)
		}
	}
	return nil
}

// GroupQualityMetrics is the account-level metric snapshot used by the evaluator.
type GroupQualityMetrics struct {
	Calls             int64
	SoftFailures      int64
	WarmSamples       int64
	WarmTTFTP90Ms     int
	ZeroCacheSamples  int64
	WarmZeroCacheRate float64
	TrialRequests     int64
	TrialSoftFailures int64
}

// GroupQualityObservation is a persisted success or upstream-attempt event.
type GroupQualityObservation struct {
	AccountID           int64
	RequestID           string
	At                  time.Time
	Success             bool
	SoftFailure         bool
	Kind                string
	StatusCode          int
	Category            string
	Message             string
	SessionID           string
	Warm                bool
	Streaming           bool
	FirstTokenMs        int
	PromptTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
}

// IsGroupQualitySoftFailure keeps the health classifier aligned with share scheduling.
func IsGroupQualitySoftFailure(kind string, statusCode int, category, message string) bool {
	return shareSoftFailureAttempt(ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: kind, StatusCode: statusCode, Category: category, Message: message})
}

// GroupQualityGateRepository persists the policy setting and reads bounded metric history.
type GroupQualityGateRepository interface {
	LoadPolicies(context.Context) (GroupQualityGatePolicies, error)
	CompareAndSwapPolicies(context.Context, int, GroupQualityGatePolicies) (GroupQualityGatePolicies, error)
	LoadObservations(context.Context, []int64, time.Time, time.Time) ([]GroupQualityObservation, time.Time, error)
}

// GroupQualityGateStatus is the admin API response for one evaluation snapshot.
type GroupQualityGateStatus struct {
	GeneratedAt    time.Time                         `json:"generated_at"`
	Stale          bool                              `json:"stale"`
	AccountMetrics map[string]GroupQualityMetricView `json:"account_metrics"`
	Groups         []GroupQualityGroupView           `json:"groups"`
}

// GroupQualityMetricView contains display metrics for one account.
type GroupQualityMetricView struct {
	Calls             int64   `json:"calls"`
	SoftFailures      int64   `json:"soft_failures"`
	ErrorRate         float64 `json:"error_rate"`
	WarmSamples       int64   `json:"warm_samples"`
	WarmTTFTP90Ms     int     `json:"warm_ttft_p90_ms"`
	ZeroCacheSamples  int64   `json:"zero_cache_samples"`
	WarmZeroCacheRate float64 `json:"warm_zero_cache_rate"`
}

// GroupQualityGroupView is one group's status row.
type GroupQualityGroupView struct {
	GroupID           int64                     `json:"group_id"`
	Enabled           bool                      `json:"enabled"`
	FastWarmTTFTP90Ms int                       `json:"fast_warm_ttft_p90_ms"`
	Summary           GroupQualitySummaryView   `json:"summary"`
	Accounts          []GroupQualityAccountView `json:"accounts"`
}

// GroupQualitySummaryView summarizes the selected group members.
type GroupQualitySummaryView struct {
	Eligible int `json:"eligible"`
	Ejected  int `json:"ejected"`
	Trial    int `json:"trial"`
}

// GroupQualityAccountView contains the quality decision for a group member.
type GroupQualityAccountView struct {
	AccountID int64      `json:"account_id"`
	State     string     `json:"state"`
	Until     *time.Time `json:"until,omitempty"`
	Reasons   []string   `json:"reasons"`
	Strikes   int        `json:"strikes"`
	Usable    bool       `json:"usable"`
	Passing   bool       `json:"passing"`
	GuardHeld bool       `json:"guard_held"`
	Pinned    bool       `json:"pinned"`
	Vetoed    bool       `json:"vetoed"`
	Tier      int        `json:"tier"`
	Headroom  float64    `json:"headroom"`
}

func (m GroupQualityMetrics) ErrorRate() float64 {
	if m.Calls <= 0 {
		return 0
	}
	return float64(m.SoftFailures) / float64(m.Calls)
}

func (m GroupQualityMetrics) EjectionReady(p GroupQualityGatePolicy) bool {
	return (m.Calls >= 20 && m.SoftFailures >= 5 && m.ErrorRate() > p.MaxErrorRate) ||
		(m.WarmSamples >= 10 && m.WarmTTFTP90Ms > p.MaxWarmTTFTP90Ms)
}

func groupQualityReasons(p GroupQualityGatePolicy, m GroupQualityMetrics) []string {
	reasons := make([]string, 0, 2)
	if m.Calls >= 20 && m.SoftFailures >= 5 && m.ErrorRate() > p.MaxErrorRate {
		reasons = append(reasons, "error_rate")
	}
	if m.WarmSamples >= 10 && m.WarmTTFTP90Ms > p.MaxWarmTTFTP90Ms {
		reasons = append(reasons, "warm_ttft")
	}
	return reasons
}

func (m GroupQualityMetrics) Passing(p GroupQualityGatePolicy) bool {
	if m.Calls >= 20 && m.SoftFailures >= 5 && m.ErrorRate() > p.MaxErrorRate {
		return false
	}
	if m.WarmSamples >= 10 && m.WarmTTFTP90Ms > p.MaxWarmTTFTP90Ms {
		return false
	}
	return true
}

type groupQualityAccountState struct {
	State           string
	Reasons         []string
	Strike          int
	EjectedAt       time.Time
	Until           time.Time
	TrialSince      time.Time
	Tier            int
	TierAt          time.Time
	LastEjected     time.Time
	LastEjectionEnd time.Time
	ResetAt         time.Time
}

func groupQualityBackoff(strike int) time.Duration {
	switch strike {
	case 1:
		return 5 * time.Minute
	case 2:
		return 10 * time.Minute
	case 3:
		return 20 * time.Minute
	default:
		return 30 * time.Minute
	}
}

func evaluateGroupQualityMember(now time.Time, policy GroupQualityGatePolicy, metric GroupQualityMetrics, previous groupQualityAccountState, _ bool) (groupQualityAccountState, bool) {
	strikeWindowElapsed := previous.State == "ejected" && !previous.Until.IsZero() && !now.Before(previous.Until) && now.Sub(previous.Until) >= 2*time.Hour
	strikeWindowElapsed = strikeWindowElapsed || previous.State != "ejected" && !previous.LastEjectionEnd.IsZero() && now.Sub(previous.LastEjectionEnd) >= 2*time.Hour
	if previous.Strike > 0 && strikeWindowElapsed {
		previous.Strike = 0
	}
	if previous.State == "ejected" && now.Before(previous.Until) {
		return previous, false
	}
	if previous.State == "ejected" && !now.Before(previous.Until) {
		previous.LastEjectionEnd = previous.Until
		if metric.EjectionReady(policy) {
			previous.Strike++
			previous.State = "ejected"
			previous.Reasons = groupQualityReasons(policy, metric)
			previous.Tier = GroupQualityGateTierOther
			previous.EjectedAt = now
			previous.LastEjected = now
			previous.Until = now.Add(groupQualityBackoff(previous.Strike))
			previous.TierAt = time.Time{}
			return previous, false
		}
		if metric.RecoveryReady(policy) {
			previous.State = "eligible"
			previous.Reasons = nil
			previous.TrialSince = time.Time{}
			previous.Until = time.Time{}
			previous.Tier = GroupQualityGateTierFast
			previous.TierAt = now
			return previous, true
		}
		previous.State = "trial"
		previous.Reasons = nil
		previous.TrialSince = now
		previous.Until = time.Time{}
		previous.Tier = GroupQualityGateTierTrial
		previous.TierAt = now
		return previous, true
	}
	if previous.State == "trial" {
		if metric.TrialSoftFailures >= 2 || metric.EjectionReady(policy) {
			previous.Strike++
			previous.State = "ejected"
			previous.Reasons = groupQualityReasons(policy, metric)
			previous.Tier = GroupQualityGateTierOther
			previous.EjectedAt = now
			previous.LastEjected = now
			previous.Until = now.Add(groupQualityBackoff(previous.Strike))
			previous.TierAt = time.Time{}
			return previous, false
		}
		if metric.TrialRequests >= 20 {
			previous.State = "eligible"
			previous.Reasons = nil
			previous.TrialSince = time.Time{}
			previous.Until = time.Time{}
			previous.Tier = GroupQualityGateTierFast
			previous.TierAt = now
		}
		return previous, true
	}
	if metric.EjectionReady(policy) {
		previous.Strike++
		previous.State = "ejected"
		previous.Reasons = groupQualityReasons(policy, metric)
		previous.Tier = GroupQualityGateTierOther
		previous.EjectedAt = now
		previous.LastEjected = now
		previous.Until = now.Add(groupQualityBackoff(previous.Strike))
		previous.TierAt = time.Time{}
		return previous, false
	}
	previous.State = "eligible"
	previous.Reasons = nil
	previous.Until = time.Time{}
	return previous, true
}

func (m GroupQualityMetrics) RecoveryReady(p GroupQualityGatePolicy) bool {
	// Recovery needs a full denominator, but it does not require the
	// ejection soft-failure minimum again.  An account with 20 fresh calls and
	// a rate at or below 80% of the limit is healthy enough to leave ejection.
	errorReady := m.Calls >= 20
	ttftReady := m.WarmSamples >= 10
	if !errorReady && !ttftReady {
		return false
	}
	if errorReady && m.ErrorRate() > p.MaxErrorRate*0.8 {
		return false
	}
	if ttftReady && float64(m.WarmTTFTP90Ms) > float64(p.MaxWarmTTFTP90Ms)*0.8 {
		return false
	}
	return true
}

// GroupQualityMemberResult keeps the independent quality and availability facts for a group member.
type GroupQualityMemberResult struct {
	Usable    bool
	Passing   bool
	Pinned    bool
	GuardHeld bool
	State     string
	Tier      int
}

// GroupQualityGateView exposes the immutable decision view consumed by scheduling,
// fallback availability and operations availability.
type GroupQualityGateView interface {
	// Vetoed reports whether quality state currently excludes an account from a group.
	Vetoed(groupID, accountID int64) bool
	// OrderTier returns the account's ordering tier for a group.
	OrderTier(groupID, accountID int64) int
}

type emptyGroupQualityGateView struct{}

func (emptyGroupQualityGateView) Vetoed(int64, int64) bool { return false }

func (emptyGroupQualityGateView) OrderTier(int64, int64) int { return GroupQualityGateTierFast }

// NewEmptyGroupQualityGateView creates the safe view used until the evaluator is installed.
func NewEmptyGroupQualityGateView() GroupQualityGateView {
	return emptyGroupQualityGateView{}
}
