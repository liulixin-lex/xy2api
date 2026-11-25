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
type SchedAdminAccountSummaryError struct {
	SchedAdminMark
	Failed int      `json:"failed"`
	Rate   *float64 `json:"rate"`
}
type SchedAdminAccountSummaryCache struct {
	Ratio *float64 `json:"ratio"`
	Turns int      `json:"turns"`
}
type SchedAdminRear struct {
	Level     int                      `json:"level"`
	UntilLine schedquality.Dimension   `json:"until_line"`
	GroupID   int64                    `json:"group_id"`
	Model     string                   `json:"model"`
	Lines     []schedquality.Dimension `json:"lines"`
	Until     string                   `json:"until"`
	Exempt    bool                     `json:"exempt"`
	Effective bool                     `json:"effective"`
}
type SchedAdminTrial struct {
	Level          int                    `json:"level"`
	UntilLine      schedquality.Dimension `json:"until_line"`
	TrialStartedAt *string                `json:"trial_started_at"`
	GroupID        int64                  `json:"group_id"`
	Model          string                 `json:"model"`
	Until          string                 `json:"until"`
}
type SchedAdminAccountSummary struct {
	AccountID   int64                           `json:"account_id"`
	GroupStatus map[string]string               `json:"group_status"`
	ResetAt     *string                         `json:"reset_at"`
	Stop        *SchedAdminStop                 `json:"stop"`
	RecentSince *string                         `json:"recent_since"`
	FirstOutput schedquality.FirstOutputSummary `json:"first_output"`
	Slow10s     schedquality.Slow10sSummary     `json:"slow_10s"`
	Error       SchedAdminAccountSummaryError   `json:"error"`
	Cache       SchedAdminAccountSummaryCache   `json:"cache"`
	Rear        []SchedAdminRear                `json:"rear"`
	Trial       []SchedAdminTrial               `json:"trial"`
	Exemptions  []SchedAdminExemption           `json:"exemptions"`
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
	RecoveryOverLimit bool    `json:"recovery_over_limit,omitempty"`
	Limit             float64 `json:"limit"`
	Clear             int     `json:"clear"`
	Failed            int     `json:"failed"`
	Unknown           int     `json:"unknown"`
	Padded            int     `json:"padded"`
	LowerFail         float64 `json:"lower_fail"`
	LowerPass         float64 `json:"lower_pass"`
}
type SchedAdminCacheEvidence struct {
	Path          string  `json:"path,omitempty"`
	LowSessions10 *int    `json:"low_sessions_10,omitempty"`
	WindowFrom    *string `json:"window_from,omitempty"`
	WindowTo      *string `json:"window_to,omitempty"`
	MinRatio      float64 `json:"min_ratio"`
	Turns         int     `json:"turns"`
	Ratio         float64 `json:"ratio"`
	Sessions      int     `json:"sessions"`
	LowSessions   int     `json:"low_sessions"`
}
type SchedAdminLastEval struct {
	At     string                        `json:"at"`
	Acted  bool                          `json:"acted"`
	Action schedquality.Action           `json:"action"`
	Reason schedquality.AssessmentReason `json:"reason"`
	Causes []string                      `json:"causes"`
	Have   *float64                      `json:"have"`
	Need   *int                          `json:"need"`
	Since  string                        `json:"since"`
	Count  int64                         `json:"count"`
}

// SchedAdminLastEvalFromState returns only the last committed assessment at the
// line's current watermark. Old rows and mixed-version writes can lack one.
func SchedAdminLastEvalFromState(r schedquality.LineState) *SchedAdminLastEval {
	if len(r.LastAssessment) == 0 {
		return nil
	}
	last := r.LastAssessment[len(r.LastAssessment)-1]
	if !last.MatchesWatermark(r.EvaluatedAt) {
		return nil
	}
	return &SchedAdminLastEval{
		At: schedAdminStamp(last.LastAt), Since: schedAdminStamp(last.FirstAt), Count: last.Count,
		Acted: last.Action != "", Action: last.Action, Reason: last.Reason,
		Causes: last.CauseNames(), Have: last.Have, Need: last.Need,
	}
}

type SchedAdminLine[E any] struct {
	LastEval *SchedAdminLastEval `json:"last_eval"`
	schedquality.SampleCounts
	Identity       string  `json:"identity"`
	Level          int     `json:"level"`
	Until          *string `json:"until"`
	TrialUntil     *string `json:"trial_until"`
	TrialStartedAt *string `json:"trial_started_at"`
	Verdict        string  `json:"verdict"`
	Evidence       E       `json:"evidence"`
	SourceGroups   []int64 `json:"source_groups"`
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
type SchedAdminErrorTripExample struct {
	Known  int `json:"known"`
	Failed int `json:"failed"`
}

type SchedAdminPolicy struct {
	EffectiveMaxErrorRate float64                      `json:"effective_max_error_rate"`
	ErrorTripExamples     []SchedAdminErrorTripExample `json:"error_trip_examples"`
	GroupID               int64                        `json:"group_id"`
	Version               int64                        `json:"version"`
	Policy                SchedAdminPolicyPolicy       `json:"policy"`
}
type SchedAdminPolicyPolicy struct {
	Enabled             bool    `json:"enabled"`
	TTFTSeconds         float64 `json:"ttft_seconds"`
	MaxErrorRate        float64 `json:"max_error_rate"`
	MinCacheRatio       float64 `json:"min_cache_ratio"`
	TakeoverIdleSeconds int     `json:"takeover_idle_seconds"`
}
type SchedAdminSupply struct {
	Declared  int    `json:"declared"`
	Enabled   int    `json:"enabled"`
	Available int    `json:"available"`
	At        string `json:"at"`
}

type SchedAdminOrder struct {
	Supply        *SchedAdminSupply    `json:"supply"`
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
type SchedAdminRearEntries struct {
	Decisions     int64 `json:"decisions"`
	AccountModels int64 `json:"account_models"`
}

type SchedAdminGroupSummary struct {
	RearEntries24h *SchedAdminRearEntries `json:"rear_entries_24h"`
	GroupID        int64                  `json:"group_id"`
	Accounts       SchedAdminCounts       `json:"accounts"`
	Last60         SchedAdminGroupLast60  `json:"last60"`
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
	OwnSampleAt60        *bool  `json:"own_sample_at_60,omitempty"`
	ReleaseReason        string `json:"release_reason,omitempty"`
	TrialDurationSeconds int64  `json:"trial_duration_seconds,omitempty"`
	HistoryMissing       bool   `json:"history_missing,omitempty"`
	MissingDeadline      bool   `json:"missing_deadline,omitempty"`
	schedquality.SampleCounts
	TrialSeats   *string                    `json:"trial_seats"`
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
