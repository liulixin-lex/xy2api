package service

type SchedAdminOverride struct {
	Mode      string  `json:"mode"`
	AllGroups bool    `json:"all_groups"`
	ExpiresAt *string `json:"expires_at"`
	Reason    string  `json:"reason"`
}
type SchedAdminClassLimits struct {
	Light   int `json:"light"`
	Regular int `json:"regular"`
	Heavy   int `json:"heavy"`
	Xheavy  int `json:"xheavy"`
}
type SchedAdminSummary struct {
	At            string                     `json:"at"`
	Stale         bool                       `json:"stale"`
	GlobalEnabled bool                       `json:"global_enabled"`
	Accounts      []SchedAdminAccountSummary `json:"accounts"` /* Empty lists must be []. */
}

type SchedAdminAccountSummary struct {
	AccountID   int64                             `json:"account_id"`
	Model       *string                           `json:"model"`
	Samples     int                               `json:"samples"`
	TTFO        SchedAdminAccountSummaryTTFO      `json:"ttfo"`
	Error       SchedAdminAccountSummaryError     `json:"error"`
	Cache       SchedAdminAccountSummaryCache     `json:"cache"`
	Groups      SchedAdminAccountSummaryGroups    `json:"groups"`
	Cooling     []SchedAdminAccountSummaryCooling `json:"cooling"` // Empty lists must be [].
	CoolingMore int                               `json:"cooling_more"`
}

type SchedAdminAccountSummaryTTFO struct {
	P90Ms   *int `json:"p90_ms"`
	Timeout bool `json:"timeout"`
	N       int  `json:"n"`
}

type SchedAdminAccountSummaryError struct {
	Rate *float64 `json:"rate"`
	N    int      `json:"n"`
}

type SchedAdminAccountSummaryCache struct {
	Ratio *float64 `json:"ratio"`
	N     int      `json:"n"`
}

type SchedAdminAccountSummaryGroups struct {
	Total   int `json:"total"`
	Normal  int `json:"normal"`
	Cooling int `json:"cooling"`
	Nodata  int `json:"nodata"`
}

type SchedAdminAccountSummaryCooling struct {
	GroupID     int64   `json:"group_id"`
	Model       string  `json:"model"`
	Reason      string  `json:"reason"`
	Until       *string `json:"until"`
	WaitingTest bool    `json:"waiting_test"`
	Testing     bool    `json:"testing"`
}

type SchedAdminAccountDetail struct {
	Account        SchedAdminAccountDetailAccount          `json:"account"`
	At             string                                  `json:"at"`
	Stale          bool                                    `json:"stale"`
	GlobalEnabled  bool                                    `json:"global_enabled"`
	TestsEnabled   bool                                    `json:"tests_enabled"`
	ResetAt        *string                                 `json:"reset_at"`
	Models         []SchedAdminAccountDetailModels         `json:"models"`          // Empty lists must be [].
	Groups         []SchedAdminAccountDetailGroups         `json:"groups"`          // Empty lists must be [].
	AutoStates     []SchedAdminAccountDetailAutoStates     `json:"auto_states"`     // Empty lists must be [].
	RecentAttempts []SchedAdminAccountDetailRecentAttempts `json:"recent_attempts"` // Empty lists must be [].
}

type SchedAdminAccountDetailAccount struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Priority    int     `json:"priority"`
	Rate        float64 `json:"rate"`
	Concurrency int     `json:"concurrency"`
	InFlight    int     `json:"in_flight"`
}

type SchedAdminAccountDetailModels struct {
	Model    string                             `json:"model"`
	Samples  int                                `json:"samples"`
	Sessions int                                `json:"sessions"`
	From     *string                            `json:"from"`
	To       *string                            `json:"to"`
	TTFO     SchedAdminAccountDetailModelsTTFO  `json:"ttfo"`
	Error    SchedAdminAccountDetailModelsError `json:"error"`
	Cache    SchedAdminAccountDetailModelsCache `json:"cache"`
	Test     *SchedAdminAccountDetailModelsTest `json:"test"`
}

type SchedAdminAccountDetailModelsTTFO struct {
	N                int                                        `json:"n"`
	Sessions         int                                        `json:"sessions"`
	Timeouts         int                                        `json:"timeouts"`
	P90Ms            *int                                       `json:"p90_ms"`
	P90Timeout       bool                                       `json:"p90_timeout"`
	FailBoundMs      *int                                       `json:"fail_bound_ms"`
	FailBoundTimeout bool                                       `json:"fail_bound_timeout"`
	ByClass          []SchedAdminAccountDetailModelsTTFOByClass `json:"by_class"` // Empty lists must be [].
}

type SchedAdminAccountDetailModelsTTFOByClass struct {
	Class string `json:"class"`
	N     int    `json:"n"`
	P90Ms *int   `json:"p90_ms"`
}

type SchedAdminAccountDetailModelsError struct {
	N        int      `json:"n"`
	Sessions int      `json:"sessions"`
	Bad      int      `json:"bad"`
	Rate     *float64 `json:"rate"`
	Lcb      *float64 `json:"lcb"`
}

type SchedAdminAccountDetailModelsCache struct {
	Turns    int      `json:"turns"`
	Sessions int      `json:"sessions"`
	Ratio    *float64 `json:"ratio"`
}

type SchedAdminAccountDetailModelsTest struct {
	ID           string                                   `json:"id"`
	At           string                                   `json:"at"`
	Running      bool                                     `json:"running"`
	Done         bool                                     `json:"done"`
	AbortReason  string                                   `json:"abort_reason"`
	Auto         bool                                     `json:"auto"`
	Tiers        []SchedAdminAccountDetailModelsTestTiers `json:"tiers"` // Empty lists must be [].
	InputTokens  int                                      `json:"input_tokens"`
	CachedTokens int                                      `json:"cached_tokens"`
	OutputTokens int                                      `json:"output_tokens"`
}

type SchedAdminAccountDetailModelsTestTiers struct {
	Tier string `json:"tier"`
	// TTFOMs lists only warm turns actually sent, in order, at most two.
	// A sent turn without first output (first-output timeout, fault, or other) is null;
	// unsent turns have no entry. Use Timeouts to identify timeouts.
	TTFOMs     []*int   `json:"ttfo_ms"` // Empty lists must be [].
	CacheRatio *float64 `json:"cache_ratio"`
	Faults     int      `json:"faults"`
	Timeouts   int      `json:"timeouts"`
}

type SchedAdminAccountDetailGroups struct {
	GroupID     int64                                  `json:"group_id"`
	Name        string                                 `json:"name"`
	GateEnabled bool                                   `json:"gate_enabled"`
	Policy      SchedAdminAccountDetailGroupsPolicy    `json:"policy"`
	Override    *SchedAdminOverride                    `json:"override"`
	ByModel     []SchedAdminAccountDetailGroupsByModel `json:"by_model"` // Empty lists must be [].
}

type SchedAdminAccountDetailGroupsPolicy struct {
	TTFTSeconds         float64 `json:"ttft_seconds"`
	MaxErrorRate        float64 `json:"max_error_rate"`
	MinCacheRatio       float64 `json:"min_cache_ratio"`
	TakeoverIdleSeconds int     `json:"takeover_idle_seconds"`
}

type SchedAdminAccountDetailGroupsByModel struct {
	Model       string                                    `json:"model"`
	State       string                                    `json:"state"`
	Level       int                                       `json:"level"`
	Reason      string                                    `json:"reason"`
	Since       *string                                   `json:"since"`
	Until       *string                                   `json:"until"`
	WaitingTest bool                                      `json:"waiting_test"`
	Testing     bool                                      `json:"testing"`
	TTFO        SchedAdminAccountDetailGroupsByModelTTFO  `json:"ttfo"`
	Error       SchedAdminAccountDetailGroupsByModelError `json:"error"`
	Cache       SchedAdminAccountDetailGroupsByModelCache `json:"cache"`
}

type SchedAdminAccountDetailGroupsByModelTTFO struct {
	Verdict    string  `json:"verdict"`
	From       *string `json:"from"`
	N          int     `json:"n"`
	P90Ms      *int    `json:"p90_ms"`
	P90Timeout bool    `json:"p90_timeout"`
	LimitMs    int     `json:"limit_ms"`
}

type SchedAdminAccountDetailGroupsByModelError struct {
	Verdict string   `json:"verdict"`
	From    *string  `json:"from"`
	N       int      `json:"n"`
	Rate    *float64 `json:"rate"`
	Limit   float64  `json:"limit"`
}

type SchedAdminAccountDetailGroupsByModelCache struct {
	Verdict string   `json:"verdict"`
	From    *string  `json:"from"`
	N       int      `json:"n"`
	Ratio   *float64 `json:"ratio"`
	Limit   float64  `json:"limit"`
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

type SchedAdminAccountDetailRecentAttempts struct {
	At      string `json:"at"`
	GroupID int64  `json:"group_id"`
	Model   string `json:"model"`
	Class   string `json:"class"`
	Stream  bool   `json:"stream"`
	Warm    bool   `json:"warm"`
	TTFOMs  *int   `json:"ttfo_ms"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
}

type SchedAdminReset struct {
	ResetAt   *string                    `json:"reset_at"`
	Cleared   []SchedAdminResetCleared   `json:"cleared"`   // Empty lists must be [].
	Preserved []SchedAdminResetPreserved `json:"preserved"` // Empty lists must be [].
	Failed    []SchedAdminResetFailed    `json:"failed"`    // Empty lists must be [].
}

type SchedAdminResetCleared struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
}

type SchedAdminResetPreserved struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
	Why  string `json:"why"`
}

type SchedAdminResetFailed struct {
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Error string `json:"error"`
}

type SchedAdminTest struct {
	Tests []SchedAdminTestTests `json:"tests"` // Empty lists must be [].
}

type SchedAdminTestTests struct {
	Model   string `json:"model"`
	TestID  string `json:"test_id"`
	Started bool   `json:"started"`
	Why     string `json:"why"`
}

type SchedAdminClearAutoState struct {
	Cleared bool `json:"cleared"`
}

type SchedAdminAttention struct {
	Cooling []int64 `json:"cooling"` // Empty lists must be [].
}

type SchedAdminPolicy struct {
	GroupID       int64                            `json:"group_id"`
	Version       int64                            `json:"version"`
	Policy        SchedAdminPolicyPolicy           `json:"policy"`
	ClassLimitsMs map[string]SchedAdminClassLimits `json:"class_limits_ms"`
}

type SchedAdminPolicyPolicy struct {
	Enabled             bool    `json:"enabled"`
	TTFTSeconds         float64 `json:"ttft_seconds"`
	MaxErrorRate        float64 `json:"max_error_rate"`
	MinCacheRatio       float64 `json:"min_cache_ratio"`
	TakeoverIdleSeconds int     `json:"takeover_idle_seconds"`
}

type SchedAdminBoard struct {
	GroupID       int64                     `json:"group_id"`
	Model         string                    `json:"model"`
	Models        []string                  `json:"models"` // Empty lists must be [].
	At            string                    `json:"at"`
	Stale         bool                      `json:"stale"`
	GlobalEnabled bool                      `json:"global_enabled"`
	Accounts      []SchedAdminBoardAccounts `json:"accounts"` // Empty lists must be [].
}

type SchedAdminBoardAccounts struct {
	AccountID           int64                           `json:"account_id"`
	Name                string                          `json:"name"`
	Rank                int                             `json:"rank"`
	Eligible            bool                            `json:"eligible"`
	Reject              string                          `json:"reject"`
	RejectUntil         *string                         `json:"reject_until"`
	Standing            string                          `json:"standing"`
	State               string                          `json:"state"`
	Level               int                             `json:"level"`
	Until               *string                         `json:"until"`
	WaitingTest         bool                            `json:"waiting_test"`
	Testing             bool                            `json:"testing"`
	Override            *SchedAdminOverride             `json:"override"`
	Priority            int                             `json:"priority"`
	Rate                float64                         `json:"rate"`
	InFlight            int                             `json:"in_flight"`
	Concurrency         int                             `json:"concurrency"`
	RecentlyFullSeconds int                             `json:"recently_full_seconds"`
	CanTakeOver         bool                            `json:"can_take_over"`
	TTFO                SchedAdminBoardAccountsTTFO     `json:"ttfo"`
	Error               SchedAdminBoardAccountsError    `json:"error"`
	Cache               SchedAdminBoardAccountsCache    `json:"cache"`
	Verdicts            SchedAdminBoardAccountsVerdicts `json:"verdicts"`
}

type SchedAdminBoardAccountsTTFO struct {
	N                int  `json:"n"`
	Sessions         int  `json:"sessions"`
	P90Ms            *int `json:"p90_ms"`
	P90Timeout       bool `json:"p90_timeout"`
	FailBoundMs      *int `json:"fail_bound_ms"`
	FailBoundTimeout bool `json:"fail_bound_timeout"`
}

type SchedAdminBoardAccountsError struct {
	N        int      `json:"n"`
	Sessions int      `json:"sessions"`
	Rate     *float64 `json:"rate"`
	Lcb      *float64 `json:"lcb"`
}

type SchedAdminBoardAccountsCache struct {
	N        int      `json:"n"`
	Sessions int      `json:"sessions"`
	Ratio    *float64 `json:"ratio"`
}

type SchedAdminBoardAccountsVerdicts struct {
	TTFO  string `json:"ttfo"`
	Error string `json:"error"`
	Cache string `json:"cache"`
}
