package service

import "github.com/liulixin-lex/xy2api/internal/domain/schedquality"

type SchedAdminSummary struct {
	At            string                     `json:"at"`
	Stale         bool                       `json:"stale"`
	GlobalEnabled bool                       `json:"global_enabled"`
	Accounts      []SchedAdminAccountSummary `json:"accounts"`
}
type SchedAdminStop struct {
	Source string  `json:"source"`
	Reason string  `json:"reason"`
	Until  *string `json:"until"`
}
type SchedAdminExemption struct {
	GroupID   int64   `json:"group_id"`
	ExpiresAt *string `json:"expires_at"`
	Reason    string  `json:"reason"`
}
type SchedAdminMark struct {
	N     int  `json:"n"`
	Short bool `json:"short"`
	Red   bool `json:"red"`
}
type SchedAdminAccountSummaryTTFO struct {
	FirstEventP50Ms  *int `json:"first_event_p50_ms"`
	FirstOutputP50Ms *int `json:"first_output_p50_ms"`
	GapP50Ms         *int `json:"gap_p50_ms"`
	N                int  `json:"n"`
}
type SchedAdminAccountSummaryError struct {
	SchedAdminMark
	Rate *float64 `json:"rate"`
}
type SchedAdminAccountSummaryCache struct {
	Ratio       *float64 `json:"ratio"`
	Turns       int      `json:"turns"`
	AllRatio    *float64 `json:"all_ratio"`
	AllRequests int      `json:"all_requests"`
}
type SchedAdminRear struct {
	GroupID   int64                    `json:"group_id"`
	Model     string                   `json:"model"`
	Lines     []schedquality.Dimension `json:"lines"`
	Until     string                   `json:"until"`
	Exempt    bool                     `json:"exempt"`
	Effective bool                     `json:"effective"`
}
type SchedAdminTrial struct {
	GroupID int64  `json:"group_id"`
	Model   string `json:"model"`
	Until   string `json:"until"`
}
type SchedAdminAccountSummary struct {
	AccountID   int64                         `json:"account_id"`
	GroupStatus map[string]string             `json:"group_status"`
	ResetAt     *string                       `json:"reset_at"`
	Stop        *SchedAdminStop               `json:"stop"`
	TTFO        SchedAdminAccountSummaryTTFO  `json:"ttfo"`
	Error       SchedAdminAccountSummaryError `json:"error"`
	Cache       SchedAdminAccountSummaryCache `json:"cache"`
	Rear        []SchedAdminRear              `json:"rear"`
	Trial       []SchedAdminTrial             `json:"trial"`
	Exemptions  []SchedAdminExemption         `json:"exemptions"`
}
type SchedAdminAccountDetail struct {
	Account       SchedAdminAccountDetailAccount      `json:"account"`
	At            string                              `json:"at"`
	Stale         bool                                `json:"stale"`
	GlobalEnabled bool                                `json:"global_enabled"`
	Stop          *SchedAdminStop                     `json:"stop"`
	ResetAt       *string                             `json:"reset_at"`
	Groups        []SchedAdminAccountDetailGroups     `json:"groups"`
	AutoStates    []SchedAdminAccountDetailAutoStates `json:"auto_states"`
	Exemptions    []SchedAdminExemption               `json:"exemptions"`
}
type SchedAdminAccountDetailAccount struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Priority    int     `json:"priority"`
	Rate        float64 `json:"rate"`
	Concurrency int     `json:"concurrency"`
	InFlight    int     `json:"in_flight"`
}
type SchedAdminAccountDetailGroups struct {
	GroupID   int64                                  `json:"group_id"`
	Name      string                                 `json:"name"`
	Policy    SchedAdminPolicyPolicy                 `json:"policy"`
	Exemption *SchedAdminExemption                   `json:"exemption"`
	ByModel   []SchedAdminAccountDetailGroupsByModel `json:"by_model"`
}

// Evidence DTOs deliberately omit assessment internals (verdict and token totals).
type SchedAdminErrorEvidence struct {
	Limit     float64 `json:"limit"`
	Clear     int     `json:"clear"`
	Failed    int     `json:"failed"`
	Unknown   int     `json:"unknown"`
	Padded    int     `json:"padded"`
	LowerFail float64 `json:"lower_fail"`
	LowerPass float64 `json:"lower_pass"`
}
type SchedAdminCacheEvidence struct {
	MinRatio    float64 `json:"min_ratio"`
	Turns       int     `json:"turns"`
	Ratio       float64 `json:"ratio"`
	Sessions    int     `json:"sessions"`
	LowSessions int     `json:"low_sessions"`
}
type SchedAdminLine[E any] struct {
	Identity     string  `json:"identity"`
	Level        int     `json:"level"`
	Until        *string `json:"until"`
	TrialUntil   *string `json:"trial_until"`
	Verdict      string  `json:"verdict"`
	Evidence     E       `json:"evidence"`
	SourceGroups []int64 `json:"source_groups"`
}
type SchedAdminLines struct {
	TTFO  SchedAdminLine[schedquality.TTFTEvidence] `json:"ttfo"`
	Error SchedAdminLine[SchedAdminErrorEvidence]   `json:"error"`
	Cache SchedAdminLine[SchedAdminCacheEvidence]   `json:"cache"`
}
type SchedAdminAccountDetailGroupsByModel struct {
	Model     string          `json:"model"`
	Placement string          `json:"placement"`
	Exempt    bool            `json:"exempt"`
	Lines     SchedAdminLines `json:"lines"`
}
type SchedAdminAccountDetailAutoStates struct {
	Key       string  `json:"key"`
	Kind      string  `json:"kind"`
	Scope     string  `json:"scope"`
	Model     string  `json:"model"`
	Until     *string `json:"until"`
	Count     *[2]int `json:"count"`
	Shared    int     `json:"shared"`
	Clearable bool    `json:"clearable"`
	Why       string  `json:"why"`
}
type SchedAdminReset struct {
	AccountID int64                   `json:"account_id"`
	ResetAt   *string                 `json:"reset_at"`
	Cleared   []string                `json:"cleared"`
	Kept      []SchedAdminResetKept   `json:"kept"`
	Failed    []SchedAdminResetFailed `json:"failed"`
}
type SchedAdminResetKept struct {
	Kind  string  `json:"kind"`
	Until *string `json:"until"`
}
type SchedAdminResetFailed struct {
	Kind string `json:"kind"`
}
type SchedAdminClearAutoState struct {
	Cleared bool `json:"cleared"`
}
type SchedAdminAttention struct {
	Rear    []int64 `json:"rear"`
	Trial   []int64 `json:"trial"`
	Unknown []int64 `json:"unknown"`
	Exempt  []int64 `json:"exempt"`
}
type SchedAdminPolicy struct {
	GroupID int64                  `json:"group_id"`
	Version int64                  `json:"version"`
	Policy  SchedAdminPolicyPolicy `json:"policy"`
}
type SchedAdminPolicyPolicy struct {
	Enabled             bool    `json:"enabled"`
	TTFTSeconds         float64 `json:"ttft_seconds"`
	MaxErrorRate        float64 `json:"max_error_rate"`
	MinCacheRatio       float64 `json:"min_cache_ratio"`
	TakeoverIdleSeconds int     `json:"takeover_idle_seconds"`
}
type SchedAdminOrder struct {
	GroupID       int64                `json:"group_id"`
	Model         string               `json:"model"`
	Models        []string             `json:"models"`
	At            string               `json:"at"`
	Stale         bool                 `json:"stale"`
	GlobalEnabled bool                 `json:"global_enabled"`
	Rows          []SchedAdminOrderRow `json:"rows"`
}
type SchedAdminOrderRow struct {
	Rank        int               `json:"rank"`
	AccountID   int64             `json:"account_id"`
	Name        string            `json:"name"`
	Layer       string            `json:"layer"`
	Lines       map[string]string `json:"lines"`
	Pinned      bool              `json:"pinned"`
	Excluded    *string           `json:"excluded"`
	Until       *string           `json:"until"`
	Exempt      bool              `json:"exempt"`
	Priority    int               `json:"priority"`
	Rate        float64           `json:"rate"`
	LoadRate    int               `json:"load_rate"`
	InFlight    int               `json:"in_flight"`
	Concurrency int               `json:"concurrency"`
	LastUsedAt  *string           `json:"last_used_at"`
}
type SchedAdminCounts struct {
	Pass    int `json:"pass"`
	Unknown int `json:"unknown"`
	Trial   int `json:"trial"`
	Rear    int `json:"rear"`
}
type SchedAdminGroupLast60 struct {
	E2EFirstOutputP50Ms  *float64 `json:"e2e_first_output_p50_ms"`
	E2EFirstOutputP90Ms  *float64 `json:"e2e_first_output_p90_ms"`
	BodyFirstOutputP50Ms *float64 `json:"body_first_output_p50_ms"`
	BodyFirstOutputP90Ms *float64 `json:"body_first_output_p90_ms"`
	Requests             *int64   `json:"requests"`
	Migration400         *int64   `json:"migration_400"`
}
type SchedAdminGroupSummary struct {
	GroupID  int64                 `json:"group_id"`
	Accounts SchedAdminCounts      `json:"accounts"`
	Last60   SchedAdminGroupLast60 `json:"last60"`
}
type SchedAdminGroupsSummary struct {
	At     string                   `json:"at"`
	Stale  bool                     `json:"stale"`
	Groups []SchedAdminGroupSummary `json:"groups"`
}
type SchedAdminVerdictCounts struct {
	Pass    int `json:"pass"`
	Unknown int `json:"unknown"`
	Fail    int `json:"fail"`
}
type SchedAdminPreviewLine struct {
	Now   SchedAdminVerdictCounts `json:"now"`
	After SchedAdminVerdictCounts `json:"after"`
}
type SchedAdminPolicyPreview struct {
	RelaxedLines []schedquality.Dimension                         `json:"relaxed_lines"`
	Lines        map[schedquality.Dimension]SchedAdminPreviewLine `json:"lines"`
	Changed      []SchedAdminPolicyChange                         `json:"changed"`
}
type SchedAdminPolicyChange struct {
	AccountID int64                  `json:"account_id"`
	Name      string                 `json:"name"`
	Model     string                 `json:"model"`
	Line      schedquality.Dimension `json:"line"`
	From      string                 `json:"from"`
	To        string                 `json:"to"`
}
type SchedAdminPolicySaved struct {
	SchedAdminPolicy
	RelaxedLines []schedquality.Dimension `json:"relaxed_lines"`
	Cleared      int                      `json:"cleared"`
}
type SchedAdminTTFTRule struct {
	Version int64 `json:"version"`
	schedquality.RuleBParams
	Defaults schedquality.RuleBParams `json:"defaults"`
	Replayed map[string][2]float64    `json:"replayed"`
}
type SchedAdminTTFTRuleSaved struct {
	SchedAdminTTFTRule
	Change  string `json:"change"`
	Cleared int    `json:"cleared"`
}
type SchedAdminDecision struct {
	ID      int64                  `json:"id"`
	At      string                 `json:"at"`
	GroupID int64                  `json:"group_id"`
	Model   string                 `json:"model"`
	Line    schedquality.Dimension `json:"line"`
	Action  string                 `json:"action"`
	Level   int                    `json:"level"`
	Until   *string                `json:"until"`
	Basis   *SchedAdminBasis       `json:"basis,omitempty"`
}
type SchedAdminBasis struct {
	RuleVersion  string                     `json:"rule_version"`
	Line         schedquality.Dimension     `json:"line"`
	Kind         string                     `json:"kind"`
	Cut          *string                    `json:"cut,omitempty"`
	Threshold    float64                    `json:"threshold"`
	SourceGroups []int64                    `json:"source_groups"`
	TTFT         *schedquality.TTFTEvidence `json:"ttft,omitempty"`
	Error        *SchedAdminErrorEvidence   `json:"error,omitempty"`
	Cache        *SchedAdminCacheEvidence   `json:"cache,omitempty"`
}
