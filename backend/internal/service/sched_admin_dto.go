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
	SchedAdminMark
	P50Ms *int `json:"p50_ms"`
}
type SchedAdminAccountSummaryError struct {
	SchedAdminMark
	Rate *float64 `json:"rate"`
}
type SchedAdminAccountSummaryCache struct {
	SchedAdminMark
	Ratio *float64 `json:"ratio"`
}
type SchedAdminSubstandard struct {
	GroupID    int64                    `json:"group_id"`
	Model      string                   `json:"model"`
	Dims       []schedquality.Dimension `json:"dims"`
	Until      string                   `json:"until"`
	OtherGroup bool                     `json:"other_group"`
	Exempt     bool                     `json:"exempt"`
}
type SchedAdminAccountSummary struct {
	AccountID   int64                         `json:"account_id"`
	Status      string                        `json:"status"`
	ResetAt     *string                       `json:"reset_at"`
	Stop        *SchedAdminStop               `json:"stop"`
	TTFO        SchedAdminAccountSummaryTTFO  `json:"ttfo"`
	Error       SchedAdminAccountSummaryError `json:"error"`
	Cache       SchedAdminAccountSummaryCache `json:"cache"`
	Substandard []SchedAdminSubstandard       `json:"substandard"`
	Exemptions  []SchedAdminExemption         `json:"exemptions"`
}
type SchedAdminAccountDetail struct {
	Account       SchedAdminAccountDetailAccount      `json:"account"`
	At            string                              `json:"at"`
	Stale         bool                                `json:"stale"`
	GlobalEnabled bool                                `json:"global_enabled"`
	Status        string                              `json:"status"`
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
type SchedAdminDimDetail struct {
	Scope     string   `json:"scope"`
	WindowMin int      `json:"window_min"`
	N         int      `json:"n"`
	LCB       *float64 `json:"lcb"`
	Verdict   string   `json:"verdict"`
}
type SchedAdminCountDetail struct {
	SchedAdminDimDetail
	Bad int `json:"bad"`
}
type SchedAdminCacheDetail struct {
	SchedAdminDimDetail
	Ratio *float64 `json:"ratio"`
}
type SchedAdminAccountDetailGroupsByModel struct {
	Model         string                   `json:"model"`
	Failing       bool                     `json:"failing"`
	Dims          []schedquality.Dimension `json:"dims"`
	Level         int                      `json:"level"`
	Since         *string                  `json:"since"`
	Until         *string                  `json:"until"`
	OtherGroupIDs []int64                  `json:"other_group_ids"`
	Exempt        bool                     `json:"exempt"`
	TTFO          SchedAdminCountDetail    `json:"ttfo"`
	Error         SchedAdminCountDetail    `json:"error"`
	Cache         SchedAdminCacheDetail    `json:"cache"`
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
	Substandard []int64 `json:"substandard"`
	Exempt      []int64 `json:"exempt"`
	Observing   []int64 `json:"observing"`
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
	Rank        int                      `json:"rank"`
	AccountID   int64                    `json:"account_id"`
	Name        string                   `json:"name"`
	Layer       string                   `json:"layer"`
	Dims        []schedquality.Dimension `json:"dims"`
	Until       *string                  `json:"until"`
	Exempt      bool                     `json:"exempt"`
	Priority    int                      `json:"priority"`
	Rate        float64                  `json:"rate"`
	LoadRate    int                      `json:"load_rate"`
	InFlight    int                      `json:"in_flight"`
	Concurrency int                      `json:"concurrency"`
	LastUsedAt  *string                  `json:"last_used_at"`
}
type SchedAdminCounts struct {
	Normal  int `json:"normal"`
	Below   int `json:"below"`
	Stopped int `json:"stopped"`
}
type SchedAdminGroupLast60 struct {
	TTFOP50Ms  *float64 `json:"ttfo_p50_ms"`
	FailedRate *float64 `json:"failed_rate"`
	CacheRatio *float64 `json:"cache_ratio"`
	Requests   int64    `json:"requests"`
}
type SchedAdminGroupSummary struct {
	GroupID int64 `json:"group_id"`
	SchedAdminPolicyPolicy
	Accounts SchedAdminCounts      `json:"accounts"`
	Last60   SchedAdminGroupLast60 `json:"last60"`
}
type SchedAdminGroupsSummary struct {
	At     string                   `json:"at"`
	Stale  bool                     `json:"stale"`
	Groups []SchedAdminGroupSummary `json:"groups"`
}
type SchedAdminPreviewCounts struct {
	Normal int `json:"normal"`
	Below  int `json:"below"`
}
type SchedAdminPolicyPreview struct {
	Now     SchedAdminPreviewCounts  `json:"now"`
	After   SchedAdminPreviewCounts  `json:"after"`
	Changed []SchedAdminPolicyChange `json:"changed"`
}
type SchedAdminPolicyChange struct {
	AccountID int64                    `json:"account_id"`
	Name      string                   `json:"name"`
	Model     string                   `json:"model"`
	Dims      []schedquality.Dimension `json:"dims"`
}
