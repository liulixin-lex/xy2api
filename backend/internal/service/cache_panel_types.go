package service

import "time"

type CachePanelBuildInfo struct {
	Version  string
	Revision string
}
type CachePanelWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}
type CachePanelMeta struct {
	PolicyVersion string           `json:"policy_version"`
	AsOf          time.Time        `json:"as_of"`
	GeneratedAt   time.Time        `json:"generated_at"`
	Window        CachePanelWindow `json:"window"`
}
type CachePanelQuery struct {
	Window      string
	Granularity string
	Platform    string
	CostView    string
	Dimension   string
	Sort        string
	Limit       int
}
type CachePanelMetrics struct {
	Requests          int64    `json:"requests"`
	PromptTokens      int64    `json:"prompt_tokens"`
	ReadTokens        int64    `json:"read_tokens"`
	HitUpper          *float64 `json:"token_hit_upper"`
	HitLower          *float64 `json:"token_hit_lower"`
	RequestHit        *float64 `json:"request_hit"`
	OpportunityTokens int64    `json:"opportunity_recorded_tokens"`
}
type CacheNorthstarPrevious struct {
	HitUpper   *float64 `json:"token_hit_upper"`
	HitLower   *float64 `json:"token_hit_lower"`
	RequestHit *float64 `json:"request_hit"`
}
type CacheNorthstarSummary struct {
	CachePanelMetrics
	BusinessCoverage           *float64                `json:"business_coverage"`
	StandardizedHit            *float64                `json:"standardized_hit"`
	StandardizedWeightCoverage *float64                `json:"standardized_weight_coverage"`
	Previous                   *CacheNorthstarPrevious `json:"previous"`
	Cost                       *CacheNorthstarCost     `json:"cost"`
}
type CacheNorthstarPoint struct {
	BucketStart       time.Time `json:"bucket_start"`
	Requests          int64     `json:"requests"`
	PromptTokens      int64     `json:"prompt_tokens"`
	HitUpper          *float64  `json:"token_hit_upper"`
	HitLower          *float64  `json:"token_hit_lower"`
	RequestHit        *float64  `json:"request_hit"`
	OpportunityTokens int64     `json:"opportunity_recorded_tokens"`
	Partial           bool      `json:"partial"`
	SavedAmount       *float64  `json:"saved_amount"`
	OpportunityAmount *float64  `json:"opportunity_amount"`
	HitCostUpper      *float64  `json:"token_hit_cost_weighted_upper"`
}
type CacheNorthstarScope struct {
	Name                 string           `json:"name"`
	BusinessPromptTokens int64            `json:"business_prompt_tokens"`
	Excluded             map[string]int64 `json:"excluded"`
}
type CacheNorthstarResponse struct {
	CachePanelMeta
	Granularity string                `json:"granularity"`
	Platform    string                `json:"platform"`
	Scope       CacheNorthstarScope   `json:"scope"`
	Summary     CacheNorthstarSummary `json:"summary"`
	Series      []CacheNorthstarPoint `json:"series"`
	Events      []CachePanelEvent     `json:"events"`
}
type CacheBreakdownCost struct {
	Currency               string   `json:"currency"`
	View                   string   `json:"view"`
	PricingSource          string   `json:"pricing_source"`
	PricingChangedInWindow bool     `json:"pricing_changed_in_window"`
	Notes                  []string `json:"notes"`
}
type CacheBreakdownMetrics struct {
	CachePanelMetrics
	OpportunityShare       *float64 `json:"opportunity_share"`
	SavedAmount            *float64 `json:"saved_amount"`
	OpportunityAmount      *float64 `json:"opportunity_amount"`
	OpportunityAmountShare *float64 `json:"opportunity_amount_share"`
	HitCostUpper           *float64 `json:"token_hit_cost_weighted_upper"`
}
type CacheBreakdownRow struct {
	Key   string         `json:"key"`
	Label string         `json:"label"`
	Meta  map[string]any `json:"meta"`
	CacheBreakdownMetrics
}
type CacheBreakdownOther struct {
	Count int `json:"count"`
	CacheBreakdownMetrics
}
type CacheBreakdownTotal struct {
	Requests          int64 `json:"requests"`
	PromptTokens      int64 `json:"prompt_tokens"`
	ReadTokens        int64 `json:"read_tokens"`
	OpportunityTokens int64 `json:"opportunity_recorded_tokens"`
}
type CacheBreakdownResponse struct {
	CachePanelMeta
	Dimension string               `json:"dimension"`
	Sort      string               `json:"sort"`
	Platform  string               `json:"platform"`
	Cost      *CacheBreakdownCost  `json:"cost"`
	Total     CacheBreakdownTotal  `json:"total"`
	Rows      []CacheBreakdownRow  `json:"rows"`
	Other     *CacheBreakdownOther `json:"other"`
}
type CacheExperimentsResponse struct {
	CachePanelMeta
	Experiments []any `json:"experiments"`
}

func cachePanelMetrics(rows []CacheNorthstarRow) CachePanelMetrics {
	var total CacheNorthstarTotals
	for _, row := range rows {
		total.Add(row.CacheNorthstarTotals)
	}
	return CachePanelMetrics{
		Requests: total.Requests, PromptTokens: total.PromptTokens, ReadTokens: total.ReadTokens, OpportunityTokens: total.OpportunityTokens,
		HitUpper:   northstarRatio(float64(total.ReadTokens), float64(total.PromptTokens)),
		HitLower:   northstarRatio(float64(total.ReadTokens-total.ReadMaybeRewritten), float64(total.PromptTokens)),
		RequestHit: northstarRatio(float64(total.RequestsHit), float64(total.Requests)),
	}
}
