package service

import (
	"math"
	"time"
)

// CacheNorthstarTotals contains additive counters only. Rates are computed after
// summing, so a collapsed "other" group never averages percentages.
type CacheNorthstarTotals struct {
	Requests                    int64
	RequestsHit                 int64
	PromptTokens                int64
	ReadTokens                  int64
	ReadMaybeRewritten          int64
	OpportunityTokens           int64
	InputTokens                 int64
	CreationTokens              int64
	InputCostMicros             int64
	ReadCostMicros              int64
	CreationCostMicros          int64
	ReadChargedMicro            int64
	OpportunityChargedMicro     int64
	ReadUpstreamMicro           int64
	OpportunityUpstreamMicro    int64
	UpstreamCoveredPromptTokens int64
	ComponentMismatchRequests   int64
}

func (t *CacheNorthstarTotals) Add(v CacheNorthstarTotals) {
	t.Requests += v.Requests
	t.RequestsHit += v.RequestsHit
	t.PromptTokens += v.PromptTokens
	t.ReadTokens += v.ReadTokens
	t.ReadMaybeRewritten += v.ReadMaybeRewritten
	t.OpportunityTokens += v.OpportunityTokens
	t.InputTokens += v.InputTokens
	t.CreationTokens += v.CreationTokens
	t.InputCostMicros += v.InputCostMicros
	t.ReadCostMicros += v.ReadCostMicros
	t.CreationCostMicros += v.CreationCostMicros
	t.ReadChargedMicro += v.ReadChargedMicro
	t.OpportunityChargedMicro += v.OpportunityChargedMicro
	t.ReadUpstreamMicro += v.ReadUpstreamMicro
	t.OpportunityUpstreamMicro += v.OpportunityUpstreamMicro
	t.UpstreamCoveredPromptTokens += v.UpstreamCoveredPromptTokens
	t.ComponentMismatchRequests += v.ComponentMismatchRequests
}

type CacheNorthstarRow struct {
	BucketStart   time.Time
	Platform      string
	GroupID       int64
	GroupLabel    string
	AccountID     int64
	AccountLabel  string
	ClientFamily  string
	Protocol      string
	Model         string
	ThresholdBand int
	HasMedia      bool
	Dimension     string
	Key           string
	Label         string
	UpstreamHost  string
	CacheNorthstarTotals
}

// A model's name is scoped to its platform, as in the offline R-5 calculation.
type cacheNorthstarModel struct{ platform, model string }
type cacheNorthstarPrice struct {
	weight             float64
	priced             bool
	prompt             int64
	minInput, maxInput float64
	minRead, maxRead   float64
}
type cacheNorthstarPrices map[cacheNorthstarModel]cacheNorthstarPrice

func northstarRatio(n, d float64) *float64 {
	if d <= 0 {
		return nil
	}
	v := n / d
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}
func northstarMoney(micros int64) float64 { return float64(micros) / 1e6 }
func northstarPriceRange(lo, hi, value float64) (float64, float64) {
	if value < 0 {
		return lo, hi
	}
	if lo < 0 || value < lo {
		lo = value
	}
	if hi < 0 || value > hi {
		hi = value
	}
	return lo, hi
}

func northstarPrices(rows []CacheNorthstarRow) cacheNorthstarPrices {
	totals := map[cacheNorthstarModel]CacheNorthstarTotals{}
	// Prices within one hour are weighted across all groups and size bands.
	hours := map[cacheNorthstarModel]map[time.Time]CacheNorthstarTotals{}
	for _, row := range rows {
		key := cacheNorthstarModel{row.Platform, row.Model}
		total := totals[key]
		total.Add(row.CacheNorthstarTotals)
		totals[key] = total
		if hours[key] == nil {
			hours[key] = map[time.Time]CacheNorthstarTotals{}
		}
		bucket := row.BucketStart.UTC().Truncate(time.Hour)
		hour := hours[key][bucket]
		hour.Add(row.CacheNorthstarTotals)
		hours[key][bucket] = hour
	}
	out := cacheNorthstarPrices{}
	for key, total := range totals {
		p := cacheNorthstarPrice{prompt: total.PromptTokens, minInput: -1, maxInput: -1, minRead: -1, maxRead: -1}
		miss := northstarRatio(northstarMoney(total.InputCostMicros), float64(total.InputTokens))
		if key.platform == PlatformAnthropic && total.CreationTokens > 0 {
			miss = northstarRatio(northstarMoney(total.CreationCostMicros), float64(total.CreationTokens))
		}
		read := northstarRatio(northstarMoney(total.ReadCostMicros), float64(total.ReadTokens))
		if miss != nil && read != nil && *miss > *read {
			p.priced = true
			p.weight = *miss - *read
		}
		for _, hour := range hours[key] {
			if hour.InputTokens > 0 {
				p.minInput, p.maxInput = northstarPriceRange(p.minInput, p.maxInput, northstarMoney(hour.InputCostMicros)/float64(hour.InputTokens))
			}
			if hour.ReadTokens > 0 {
				p.minRead, p.maxRead = northstarPriceRange(p.minRead, p.maxRead, northstarMoney(hour.ReadCostMicros)/float64(hour.ReadTokens))
			}
		}
		out[key] = p
	}
	return out
}

type CacheNorthstarCost struct {
	Currency                   string                      `json:"currency"`
	View                       string                      `json:"view"`
	PricingSource              string                      `json:"pricing_source"`
	PricingChangedInWindow     bool                        `json:"pricing_changed_in_window"`
	PricedPromptShare          float64                     `json:"priced_p_share"`
	UpstreamCoveredPromptShare *float64                    `json:"upstream_covered_p_share"`
	PricedModels               int                         `json:"priced_models"`
	SavedAmount                *float64                    `json:"saved_amount"`
	OpportunityAmount          *float64                    `json:"opportunity_amount"`
	HitUpper                   *float64                    `json:"token_hit_cost_weighted_upper"`
	HitLower                   *float64                    `json:"token_hit_cost_weighted_lower"`
	Previous                   *CacheNorthstarCostPrevious `json:"previous"`
	Notes                      []string                    `json:"notes"`
}
type CacheNorthstarCostPrevious struct {
	SavedAmount *float64 `json:"saved_amount"`
	HitUpper    *float64 `json:"token_hit_cost_weighted_upper"`
}

type cacheNorthstarCostSums struct {
	saved, opportunity, weightedRead, weightedLower, weightedPrompt float64
	pricedPrompt, coveredPrompt                                     int64
	models                                                          map[cacheNorthstarModel]bool
}

func northstarCostSums(rows []CacheNorthstarRow, prices cacheNorthstarPrices, view string) cacheNorthstarCostSums {
	v := cacheNorthstarCostSums{models: map[cacheNorthstarModel]bool{}}
	for _, row := range rows {
		key := cacheNorthstarModel{row.Platform, row.Model}
		price := prices[key]
		if !price.priced {
			continue
		}
		v.models[key] = true
		v.pricedPrompt += row.PromptTokens
		v.coveredPrompt += row.UpstreamCoveredPromptTokens
		w := price.weight
		v.weightedPrompt += float64(row.PromptTokens) * w
		v.weightedRead += float64(row.ReadTokens) * w
		v.weightedLower += float64(row.ReadTokens-row.ReadMaybeRewritten) * w
		read, opportunity := float64(row.ReadTokens), float64(row.OpportunityTokens)
		switch view {
		case "charged":
			read, opportunity = float64(row.ReadChargedMicro)/1e6, float64(row.OpportunityChargedMicro)/1e6
		case "upstream":
			read, opportunity = float64(row.ReadUpstreamMicro)/1e6, float64(row.OpportunityUpstreamMicro)/1e6
		}
		v.saved += read * w
		v.opportunity += opportunity * w
	}
	return v
}

func northstarCost(rows, business []CacheNorthstarRow, prices cacheNorthstarPrices, view string) *CacheNorthstarCost {
	result := &CacheNorthstarCost{Currency: "USD", View: view, PricingSource: "settled_cost", Notes: []string{}}
	values := northstarCostSums(rows, prices, view)
	coverage := northstarCostSums(business, prices, view)
	var total CacheNorthstarTotals
	for _, row := range business {
		total.Add(row.CacheNorthstarTotals)
	}
	if total.PromptTokens > 0 {
		result.PricedPromptShare = float64(coverage.pricedPrompt) / float64(total.PromptTokens)
	}
	result.PricedModels = len(values.models)
	if len(values.models) > 0 {
		result.SavedAmount = &values.saved
		result.OpportunityAmount = &values.opportunity
		result.HitUpper = northstarRatio(values.weightedRead, values.weightedPrompt)
		result.HitLower = northstarRatio(values.weightedLower, values.weightedPrompt)
	}
	if view != "list" {
		result.Notes = append(result.Notes, "cost_weighted_hit_uses_list")
	}
	if result.PricedPromptShare < 0.95 {
		result.Notes = append(result.Notes, "partial_pricing")
	}
	for _, price := range prices {
		if !price.priced || total.PromptTokens == 0 || float64(price.prompt)/float64(total.PromptTokens) < 0.01 {
			continue
		}
		changed := func(lo, hi float64) bool { return lo >= 0 && hi > lo*1.01 }
		if changed(price.minInput, price.maxInput) || changed(price.minRead, price.maxRead) {
			result.PricingChangedInWindow = true
		}
	}
	if result.PricingChangedInWindow {
		result.Notes = append(result.Notes, "pricing_changed_in_window")
	}
	if view == "upstream" {
		result.UpstreamCoveredPromptShare = northstarRatio(float64(coverage.coveredPrompt), float64(coverage.pricedPrompt))
		if result.UpstreamCoveredPromptShare != nil && *result.UpstreamCoveredPromptShare < 0.95 {
			result.Notes = append(result.Notes, "upstream_partial_coverage")
		}
	}
	if total.ComponentMismatchRequests > 0 {
		result.Notes = append(result.Notes, "component_mismatch")
	}
	if total.ReadMaybeRewritten > 0 {
		result.Notes = append(result.Notes, "forced_cache_billing_candidates")
	}
	return result
}
