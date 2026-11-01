package service

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
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
	ids := append([]int64{}, p.PinnedAccountIDs...)
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
		return invalidGroupQualityPolicy("enabled", "quality gate can only be enabled for OpenAI groups")
	}
	if policy.MaxErrorRate <= 0 || policy.MaxErrorRate > 1 || math.IsNaN(policy.MaxErrorRate) || math.IsInf(policy.MaxErrorRate, 0) {
		return invalidGroupQualityPolicy("max_error_rate", "max_error_rate must be in (0,1]")
	}
	if policy.FastWarmTTFTP90Ms < 1000 {
		return invalidGroupQualityPolicy("fast_warm_ttft_p90_ms", "fast_warm_ttft_p90_ms must be at least 1000 ms")
	}
	if policy.MaxWarmTTFTP90Ms > 600000 {
		return invalidGroupQualityPolicy("max_warm_ttft_p90_ms", "max_warm_ttft_p90_ms must not exceed 600000 ms")
	}
	if policy.FastWarmTTFTP90Ms >= policy.MaxWarmTTFTP90Ms {
		return invalidGroupQualityPolicy("max_warm_ttft_p90_ms", "max_warm_ttft_p90_ms must be greater than fast_warm_ttft_p90_ms")
	}
	members := map[int64]bool{}
	for _, id := range memberIDs {
		members[id] = true
	}
	for _, id := range policy.PinnedAccountIDs {
		if !members[id] {
			return invalidGroupQualityPolicy("pinned_account_ids", fmt.Sprintf("pinned account %d is not a member of group %d", id, group.ID))
		}
	}
	return nil
}

func invalidGroupQualityPolicy(field, message string) error {
	return infraerrors.BadRequest(ErrInvalidGroupQualityGatePolicy.Reason, message).WithMetadata(map[string]string{"field": field})
}

// GroupQualityMetrics is the account-level metric snapshot used by the evaluator.
type GroupQualityMetrics struct {
	Calls             int64
	SoftFailures      int64
	WarmSamples       int64
	WarmTTFTP90Ms     int
	ZeroCacheSamples  int64
	WarmZeroCacheRate float64
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

// GroupQualityErrorRequestID maps an ops error row's IDs to the usage_logs
// request ID written by resolveUsageBillingRequestID, so a request's usage row
// and its upstream failures share one identity.
func GroupQualityErrorRequestID(clientRequestID, requestID string) string {
	if id := strings.TrimSpace(clientRequestID); id != "" {
		return "client:" + id
	}
	if id := strings.TrimSpace(requestID); id != "" {
		return "local:" + id
	}
	return ""
}

// GroupQualityGateRepository persists the policy setting and reads bounded metric history.
type GroupQualityGateRepository interface {
	LoadPolicies(context.Context) (GroupQualityGatePolicies, error)
	CompareAndSwapPolicies(context.Context, int, GroupQualityGatePolicies) (GroupQualityGatePolicies, error)
	LoadMembers(context.Context, []int64) (map[int64][]Account, error)
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

// GroupQualityDecisionMetrics contains effective metrics used for a group decision.
// Counts remain visible; derived metrics are nil until their decision thresholds are met.
type GroupQualityDecisionMetrics struct {
	Calls         int64    `json:"calls"`
	SoftFailures  int64    `json:"soft_failures"`
	ErrorRate     *float64 `json:"error_rate"`
	WarmSamples   int64    `json:"warm_samples"`
	WarmTTFTP90Ms *int     `json:"warm_ttft_p90_ms"`
}

// GroupQualityAccountView contains the quality decision for a group member.
type GroupQualityAccountView struct {
	AccountID int64                       `json:"account_id"`
	State     string                      `json:"state"`
	Until     *time.Time                  `json:"until,omitempty"`
	Reasons   []string                    `json:"reasons"`
	Strikes   int                         `json:"strikes"`
	Usable    bool                        `json:"usable"`
	Passing   bool                        `json:"passing"`
	GuardHeld bool                        `json:"guard_held"`
	Pinned    bool                        `json:"pinned"`
	Vetoed    bool                        `json:"vetoed"`
	Tier      int                         `json:"tier"`
	Headroom  *float64                    `json:"headroom"`
	Metrics   GroupQualityDecisionMetrics `json:"metrics"`
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

type groupQualityTrialFailure struct {
	RequestID string
	AccountID int64
	AtUnixMs  int64
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
	LastEjectionEnd time.Time
	ResetAt         time.Time
	Severity        float64
	TrialRequests   []string // The first 20 distinct request IDs, retained across observation windows.
	TrialFailures   []groupQualityTrialFailure
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

func evaluateGroupQualityMember(now time.Time, policy GroupQualityGatePolicy, metric GroupQualityMetrics, state groupQualityAccountState, fresh bool) groupQualityAccountState {
	end := state.LastEjectionEnd
	if state.State == "ejected" {
		end = state.Until
	}
	if !end.IsZero() && now.Sub(end) >= 2*time.Hour {
		state.Strike = 0
	}
	if state.State == "ejected" && now.Before(state.Until) {
		return state
	}
	expired := state.State == "ejected"
	if expired {
		state.LastEjectionEnd = state.Until
	}
	trialFailed := state.State == "trial" && len(state.TrialFailures) >= 2
	if fresh && (metric.EjectionReady(policy) || trialFailed) {
		state.Strike++
		state.State = "ejected"
		state.Reasons = groupQualityReasons(policy, metric)
		state.Severity = 0
		if metric.Calls >= 20 && metric.SoftFailures >= 5 {
			state.Severity = metric.ErrorRate() / policy.MaxErrorRate
		}
		if metric.WarmSamples >= 10 {
			state.Severity = math.Max(state.Severity, float64(metric.WarmTTFTP90Ms)/float64(policy.MaxWarmTTFTP90Ms))
		}
		if trialFailed {
			state.Severity = math.Max(state.Severity, float64(len(state.TrialFailures))/float64(len(state.TrialRequests))/policy.MaxErrorRate)
			if !slices.Contains(state.Reasons, "error_rate") {
				state.Reasons = append(state.Reasons, "error_rate")
			}
		}
		state.Tier = GroupQualityGateTierOther
		state.EjectedAt = now
		state.Until = now.Add(groupQualityBackoff(state.Strike))
		state.TierAt = time.Time{}
		state.TrialRequests, state.TrialFailures = nil, nil
		state.TrialSince = time.Time{}
		return state
	}
	if expired {
		state.Until = time.Time{}
		state.TierAt = time.Time{}
		if fresh && metric.RecoveryReady(policy, state.Reasons) {
			state.State, state.Tier = "eligible", GroupQualityGateTierFast
		} else {
			state.State, state.Tier = "trial", GroupQualityGateTierTrial
			state.TrialSince = state.LastEjectionEnd
			state.TrialRequests, state.TrialFailures = nil, nil
		}
		state.Reasons = nil
		return state
	}
	if state.State == "trial" {
		if !fresh || len(state.TrialRequests) < 20 {
			return state
		}
		state.TrialRequests, state.TrialFailures = nil, nil
		state.TrialSince = time.Time{}
	}
	state.State = "eligible"
	state.Reasons = nil
	state.Until = time.Time{}
	return state
}

func (m GroupQualityMetrics) RecoveryReady(p GroupQualityGatePolicy, reasons []string) bool {
	errorReady, ttftReady := m.Calls >= 20, m.WarmSamples >= 10
	if !errorReady && !ttftReady || slices.Contains(reasons, "error_rate") && !errorReady || slices.Contains(reasons, "warm_ttft") && !ttftReady {
		return false
	}
	return (!errorReady || m.ErrorRate() <= p.MaxErrorRate*0.8) &&
		(!ttftReady || float64(m.WarmTTFTP90Ms) <= float64(p.MaxWarmTTFTP90Ms)*0.8)
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
