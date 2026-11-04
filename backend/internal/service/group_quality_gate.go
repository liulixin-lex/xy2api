package service

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

// GroupQualityGateTier orders candidates only within enabled OpenAI groups.
const (
	GroupQualityGateTierPass   = 0
	GroupQualityGateTierRetest = 1
	GroupQualityGateTierFail   = 2
)

const (
	groupQualityBadMemory         = 2 * time.Hour
	groupQualityAccountWindow     = 30 * time.Minute
	groupQualityBaselineWindow    = 2 * time.Hour
	groupQualityCompositionWindow = 6 * time.Hour
)

// GroupQualityGatePolicy preserves the legacy policy for version rollback.
// Only V3 is writable by this version; legacyFields also preserves absent/null
// fields, unknown fields, numeric encodings and the pinned ID order.
type GroupQualityGatePolicy struct {
	Enabled           bool                      `json:"enabled"`
	MaxErrorRate      float64                   `json:"max_error_rate"`
	MaxWarmTTFTP90Ms  int                       `json:"max_warm_ttft_p90_ms"`
	FastWarmTTFTP90Ms int                       `json:"fast_warm_ttft_p90_ms"`
	PinnedAccountIDs  []int64                   `json:"pinned_account_ids"`
	V3                *GroupQualityGateV3Policy `json:"v3,omitempty"`
	legacyFields      map[string]json.RawMessage
}

// GroupQualityGateV3Policy contains the independently stored v3 standards.
type GroupQualityGateV3Policy struct {
	Enabled           bool    `json:"enabled"`
	MaxTTFTP90Seconds int     `json:"max_ttft_p90_seconds"`
	MaxErrorRate      float64 `json:"max_error_rate"`
	MinCacheKeepRate  float64 `json:"min_cache_keep_rate"`
	Strict            bool    `json:"strict"`
}

func (p *GroupQualityGatePolicy) UnmarshalJSON(data []byte) error {
	type plain GroupQualityGatePolicy
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	delete(fields, "v3")
	*p = GroupQualityGatePolicy(decoded)
	p.legacyFields = fields
	return nil
}

func (p GroupQualityGatePolicy) MarshalJSON() ([]byte, error) {
	if p.legacyFields == nil {
		type plain GroupQualityGatePolicy
		return json.Marshal(plain(p))
	}
	fields := make(map[string]json.RawMessage, len(p.legacyFields)+1)
	for k, v := range p.legacyFields {
		fields[k] = v
	}
	if p.V3 != nil {
		raw, err := json.Marshal(p.V3)
		if err != nil {
			return nil, err
		}
		fields["v3"] = raw
	}
	return json.Marshal(fields)
}

// GroupQualityGatePolicies is the versioned setting payload.
type GroupQualityGatePolicies struct {
	Version int                               `json:"version"`
	Groups  map[string]GroupQualityGatePolicy `json:"groups"`
}

func defaultGroupQualityGatePolicy() GroupQualityGatePolicy {
	return normalizeGroupQualityGatePolicy(GroupQualityGatePolicy{})
}

func normalizeGroupQualityGatePolicy(p GroupQualityGatePolicy) GroupQualityGatePolicy {
	if p.V3 == nil {
		p.V3 = &GroupQualityGateV3Policy{Enabled: p.Enabled, MaxTTFTP90Seconds: 15, MaxErrorRate: .10, MinCacheKeepRate: .50}
	} else {
		value := *p.V3
		p.V3 = &value
	}
	return p
}

// ValidateGroupQualityGatePolicy validates only the writable v3 subobject.
func ValidateGroupQualityGatePolicy(p GroupQualityGatePolicy, group *Group, _ []int64) error {
	if p.V3 == nil {
		return invalidGroupQualityPolicy("v3", "policy.v3 is required")
	}
	v := p.V3
	if v.Enabled && (group == nil || group.Platform != PlatformOpenAI) {
		return invalidGroupQualityPolicy("v3.enabled", "quality gate can only be enabled for OpenAI groups")
	}
	if v.MaxTTFTP90Seconds < 5 || v.MaxTTFTP90Seconds > 60 {
		return invalidGroupQualityPolicy("v3.max_ttft_p90_seconds", "max_ttft_p90_seconds must be an integer from 5 to 60")
	}
	if math.IsNaN(v.MaxErrorRate) || math.IsInf(v.MaxErrorRate, 0) || v.MaxErrorRate < .05 || v.MaxErrorRate > .50 {
		return invalidGroupQualityPolicy("v3.max_error_rate", "max_error_rate must be from 0.05 to 0.50")
	}
	if math.IsNaN(v.MinCacheKeepRate) || math.IsInf(v.MinCacheKeepRate, 0) || v.MinCacheKeepRate < .50 || v.MinCacheKeepRate > .70 {
		return invalidGroupQualityPolicy("v3.min_cache_keep_rate", "min_cache_keep_rate must be from 0.50 to 0.70")
	}
	return nil
}

func invalidGroupQualityPolicy(field, message string) error {
	return infraerrors.BadRequest(ErrInvalidGroupQualityGatePolicy.Reason, message).WithMetadata(map[string]string{"field": field})
}

// GroupQualityObservation is a persisted usage row or upstream attempt.
// The legacy FirstOutputMs is used by recent health; v3 exclusively consumes
// AttemptFirstOutputMs. ResetAt truncates even baseline contributors outside
// currently enabled groups.
type GroupQualityObservation struct {
	ID                   int64
	AccountID            int64
	APIKeyID             int64
	GroupID              int64
	RequestID            string
	At                   time.Time
	PreviousAt           time.Time
	ResetAt              time.Time
	Success              bool
	Failed               bool
	SoftFailure          bool
	Kind                 string
	StatusCode           int
	Category             string
	Message              string
	SessionID            string
	StrictContinuation   bool
	Streaming            bool
	FirstOutputMs        int
	AttemptFirstOutputMs *int64
	Model                string
	ReasoningEffort      string
	PromptTokens         int64
	PreviousPromptTokens int64
	CacheReadTokens      int64
	CacheCreationTokens  int64
}

// IsGroupQualitySoftFailure is the shared legacy classifier also used by
// recent health. V3 excludes first-output cutoffs separately.
func IsGroupQualitySoftFailure(kind string, statusCode int, category, message string) bool {
	return shareSoftFailureAttempt(ShareSchedulingUpstreamAttempt{AccountID: 1, Kind: kind, StatusCode: statusCode, Category: category, Message: message})
}

func groupQualityV3SoftFailure(o GroupQualityObservation) bool {
	return o.SoftFailure && o.Kind != "first_output_timeout" && o.Category != "FIRST_OUTPUT_TIMEOUT"
}

// GroupQualityErrorRequestID aligns errors with usage billing request IDs.
func GroupQualityErrorRequestID(clientRequestID, requestID string) string {
	if id := strings.TrimSpace(clientRequestID); id != "" {
		return "client:" + id
	}
	if id := strings.TrimSpace(requestID); id != "" {
		return "local:" + id
	}
	return ""
}

// GroupQualityGateRepository persists policies and reads bounded observations.
// LoadObservations returns all OpenAI baseline/composition contributors, plus
// the requested members' attempt events; filtering members before LAG would
// silently turn an account switch into a strict continuation.
type GroupQualityGateRepository interface {
	LoadPolicies(context.Context) (GroupQualityGatePolicies, error)
	CompareAndSwapPolicies(context.Context, int, GroupQualityGatePolicies) (GroupQualityGatePolicies, error)
	LoadMembers(context.Context, []int64) (map[int64][]Account, error)
	LoadObservations(context.Context, []int64, time.Time, time.Time) ([]GroupQualityObservation, time.Time, error)
}

// GroupQualityGateStatus matches the v3 admin contract.
type GroupQualityGateStatus struct {
	GeneratedAt time.Time               `json:"generated_at"`
	Stale       bool                    `json:"stale"`
	Groups      []GroupQualityGroupView `json:"groups"`
}

type GroupQualityGroupView struct {
	GroupID         int64                     `json:"group_id"`
	Enabled         bool                      `json:"enabled"`
	Policy          GroupQualityGateV3Policy  `json:"policy"`
	ExpectedP90Ms   *float64                  `json:"expected_p90_ms"`
	ExpectedSamples int                       `json:"expected_samples"`
	Summary         GroupQualitySummaryView   `json:"summary"`
	Accounts        []GroupQualityAccountView `json:"accounts"`
}

type GroupQualitySummaryView struct {
	Passing int `json:"passing"`
	Unknown int `json:"unknown"`
	Retest  int `json:"retest"`
	Failing int `json:"failing"`
	Pinned  int `json:"pinned"`
}

type GroupQualityTTFTView struct {
	State    string     `json:"state"`
	Value    *float64   `json:"value"`
	Factor   *float64   `json:"factor"`
	Samples  int        `json:"samples"`
	Sessions int        `json:"sessions"`
	BadUntil *time.Time `json:"bad_until"`
}

type GroupQualityErrorView struct {
	State      string     `json:"state"`
	Value      *float64   `json:"value"`
	Samples    int        `json:"samples"`
	Failures   int        `json:"failures"`
	WilsonLow  *float64   `json:"wilson_low"`
	WilsonHigh *float64   `json:"wilson_high"`
	BadUntil   *time.Time `json:"bad_until"`
}

type GroupQualityCacheView struct {
	State      string     `json:"state"`
	Value      *float64   `json:"value"`
	Samples    int        `json:"samples"`
	WilsonLow  *float64   `json:"wilson_low"`
	WilsonHigh *float64   `json:"wilson_high"`
	BadUntil   *time.Time `json:"bad_until"`
}

type GroupQualityLatencyHoldView struct {
	Until  time.Time `json:"until"`
	Models []string  `json:"models"`
}

type GroupQualityAccountView struct {
	AccountID   int64                        `json:"account_id"`
	Status      string                       `json:"status"`
	Tier        int                          `json:"tier"`
	Pinned      bool                         `json:"pinned"`
	LatencyHold *GroupQualityLatencyHoldView `json:"latency_hold"`
	TTFT        GroupQualityTTFTView         `json:"ttft"`
	Error       GroupQualityErrorView        `json:"error"`
	Cache       GroupQualityCacheView        `json:"cache"`
}

type groupQualityDimensionState struct {
	State    string
	Vote     string
	Votes    int
	LastVote time.Time
	LastFail time.Time
}

func (s groupQualityDimensionState) state() string {
	if s.State == "" {
		return "none"
	}
	return s.State
}
func (s groupQualityDimensionState) hasBad(now time.Time) bool {
	return !s.LastFail.IsZero() && !now.After(s.LastFail.Add(groupQualityBadMemory))
}
func (s groupQualityDimensionState) badUntil() *time.Time {
	if s.LastFail.IsZero() {
		return nil
	}
	until := s.LastFail.Add(groupQualityBadMemory)
	return &until
}

// A missing observation sets none and breaks the vote streak, but keeps the
// dimension's bad memory. A stale evaluation must not call this function.
func voteGroupQualityDimension(s groupQualityDimensionState, now time.Time, valid, fail, pass, recovered bool) groupQualityDimensionState {
	minute := now.Truncate(time.Minute)
	if !s.LastVote.IsZero() && !minute.After(s.LastVote) {
		return s
	}
	s.LastVote = minute
	if !valid {
		s.State, s.Vote, s.Votes = "none", "", 0
		return s
	}
	vote := ""
	if fail {
		vote = "fail"
	} else if pass && (!(s.state() == "fail" || s.hasBad(now)) || recovered) {
		vote = "pass"
	}
	if vote == "" {
		s.Vote, s.Votes = "", 0
	} else {
		if s.Vote != vote {
			s.Vote, s.Votes = vote, 0
		}
		s.Votes = min(s.Votes+1, 2)
		if s.Votes == 2 {
			s.State = vote
			if vote == "pass" {
				s.LastFail = time.Time{}
			}
		}
	}
	if s.state() == "fail" {
		s.LastFail = now
	}
	return s
}

type groupQualityAccountState struct {
	TTFT    groupQualityDimensionState
	Error   groupQualityDimensionState
	Cache   groupQualityDimensionState
	ResetAt time.Time
}

func (s groupQualityAccountState) dimensions() [3]groupQualityDimensionState {
	return [3]groupQualityDimensionState{s.TTFT, s.Error, s.Cache}
}

func groupQualityTier(s groupQualityAccountState, p GroupQualityGateV3Policy, pinned bool, now time.Time) int {
	if !p.Enabled || pinned {
		return GroupQualityGateTierPass
	}
	tier := GroupQualityGateTierPass
	for _, d := range s.dimensions() {
		if d.state() == "fail" {
			return GroupQualityGateTierFail
		}
		if d.state() == "none" && (p.Strict || d.hasBad(now)) {
			tier = GroupQualityGateTierRetest
		}
	}
	return tier
}

func groupQualityStatus(s groupQualityAccountState, tier int, pinned bool) string {
	if pinned {
		return "pinned"
	}
	if tier == GroupQualityGateTierFail {
		return "failing"
	}
	if tier == GroupQualityGateTierRetest {
		return "retest"
	}
	for _, d := range s.dimensions() {
		if d.state() != "pass" {
			return "unknown"
		}
	}
	return "passing"
}

// GroupQualityGateView exposes the immutable decision view consumed by scheduling,
// fallback availability and operations availability.
type GroupQualityGateView interface {
	// Vetoed reports whether quality state currently excludes an account from a group.
	Vetoed(groupID, accountID int64) bool
	// OrderTier returns the account's ordering tier for a group.
	OrderTier(groupID, accountID int64) int
	// Enabled reports whether the quality gate is currently enabled for a group.
	Enabled(groupID int64) bool
	// SlowerThanFastLine reports a migration-eligible bound account and its metrics.
	SlowerThanFastLine(groupID, accountID int64) (warmTTFTP90Ms, fastWarmTTFTP90Ms int, ok bool)
	// MigrationTarget reports whether the account meets the quality part of the
	// v3 migration-target rule for the group. Pinned accounts get no exception.
	MigrationTarget(groupID, accountID int64) bool
	// ExpectedP90Ms returns the account's predicted first-output P90 for the
	// group (GM30 × e_G); ok is false when either factor is unavailable.
	ExpectedP90Ms(groupID, accountID int64) (ms int, ok bool)
}

type emptyGroupQualityGateView struct{}

func (emptyGroupQualityGateView) Vetoed(int64, int64) bool { return false }

func (emptyGroupQualityGateView) OrderTier(int64, int64) int { return GroupQualityGateTierPass }

func (emptyGroupQualityGateView) Enabled(int64) bool { return false }

func (emptyGroupQualityGateView) SlowerThanFastLine(int64, int64) (int, int, bool) {
	return 0, 0, false
}

func (emptyGroupQualityGateView) MigrationTarget(int64, int64) bool { return false }

func (emptyGroupQualityGateView) ExpectedP90Ms(int64, int64) (int, bool) { return 0, false }

// NewEmptyGroupQualityGateView creates the safe view used until the evaluator is installed.
func NewEmptyGroupQualityGateView() GroupQualityGateView {
	return emptyGroupQualityGateView{}
}
