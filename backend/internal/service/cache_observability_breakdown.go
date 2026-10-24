package service

import (
	"regexp"
	"sort"
	"strconv"
)

// P-3 uses [^@\s]+@[^@\s]+\.[A-Za-z]+ with Python's Unicode whitespace.
// Include its Unicode separators and additional whitespace control characters.
var cachePanelEmailLabel = regexp.MustCompile(`[^@\s\p{Z}\x{000B}\x{0085}\x{001C}-\x{001F}]+@[^@\s\p{Z}\x{000B}\x{0085}\x{001C}-\x{001F}]+\.[A-Za-z]+`)

func cacheBreakdownLabel(label, dimension, key string, meta map[string]any) string {
	if (dimension != "account" && dimension != "group") || !cachePanelEmailLabel.MatchString(label) {
		return label
	}
	if dimension == "account" {
		platform, _ := meta["platform"].(string)
		return platform + "#" + key
	}
	return "group#" + key
}

func cacheBreakdownIdentity(row CacheNorthstarRow, dimension string) (string, string) {
	switch dimension {
	case "group":
		return strconv.FormatInt(row.GroupID, 10), row.GroupLabel
	case "account":
		return strconv.FormatInt(row.AccountID, 10), row.AccountLabel
	case "upstream_host":
		return row.UpstreamHost, row.UpstreamHost
	case "client_family":
		return row.ClientFamily, row.ClientFamily
	case "protocol":
		return row.Protocol, row.Protocol
	default:
		return row.Model, row.Model
	}
}
func cacheBreakdownMetrics(rows []CacheNorthstarRow, prices cacheNorthstarPrices, view string, total CachePanelMetrics, totalCost *CacheNorthstarCost) CacheBreakdownMetrics {
	metrics := CacheBreakdownMetrics{CachePanelMetrics: cachePanelMetrics(rows)}
	metrics.OpportunityShare = northstarRatio(float64(metrics.OpportunityTokens), float64(total.OpportunityTokens))
	values := northstarCostSums(rows, prices, view)
	if len(values.models) > 0 {
		metrics.SavedAmount = &values.saved
		metrics.OpportunityAmount = &values.opportunity
		metrics.HitCostUpper = northstarRatio(values.weightedRead, values.weightedPrompt)
		if totalCost.OpportunityAmount != nil {
			metrics.OpportunityAmountShare = northstarRatio(values.opportunity, *totalCost.OpportunityAmount)
		}
	}
	return metrics
}
func cacheBreakdownMeta(rows []CacheNorthstarRow, dimension string, prices cacheNorthstarPrices) map[string]any {
	meta := map[string]any{}
	if len(rows) == 0 {
		return meta
	}
	platform := rows[0].Platform
	for _, row := range rows {
		if row.Platform != platform {
			platform = "all"
		}
	}
	switch dimension {
	case "group":
		meta["platform"] = platform
	case "account":
		meta["platform"] = platform
		meta["upstream_host"] = rows[len(rows)-1].UpstreamHost
	case "upstream_host":
		accounts := map[int64]bool{}
		for _, row := range rows {
			accounts[row.AccountID] = true
		}
		meta["accounts"] = len(accounts)
	case "model":
		values := northstarCostSums(rows, prices, "list")
		meta["platform"] = platform
		meta["priced"] = len(values.models) > 0
		meta["w_m"] = nil
		if values.pricedPrompt > 0 {
			meta["w_m"] = values.weightedPrompt / float64(values.pricedPrompt) * 1e6
		}
	}
	return meta
}
func buildCacheBreakdown(meta CachePanelMeta, q CachePanelQuery, policy CacheNorthstarPolicy, rows []CacheNorthstarRow) CacheBreakdownResponse {
	business := make([]CacheNorthstarRow, 0, len(rows))
	for _, row := range rows {
		if policy.Business(row) {
			business = append(business, row)
		}
	}
	total := cachePanelMetrics(business)
	prices := northstarPrices(business)
	cost := northstarCost(business, business, prices, q.CostView)
	result := CacheBreakdownResponse{CachePanelMeta: meta, Dimension: q.Dimension, Sort: q.Sort, Platform: q.Platform,
		Cost:  &CacheBreakdownCost{Currency: cost.Currency, View: cost.View, PricingSource: cost.PricingSource, PricingChangedInWindow: cost.PricingChangedInWindow, Notes: cost.Notes},
		Total: CacheBreakdownTotal{Requests: total.Requests, PromptTokens: total.PromptTokens, ReadTokens: total.ReadTokens, OpportunityTokens: total.OpportunityTokens},
		Rows:  []CacheBreakdownRow{}}
	grouped := map[string][]CacheNorthstarRow{}
	labels := map[string]string{}
	for _, row := range business {
		key, label := cacheBreakdownIdentity(row, q.Dimension)
		grouped[key] = append(grouped[key], row)
		labels[key] = label
	}
	candidates := make([]CacheBreakdownRow, 0, len(grouped))
	var otherRows []CacheNorthstarRow
	otherCount := 0
	for key, group := range grouped {
		metrics := cacheBreakdownMetrics(group, prices, q.CostView, total, cost)
		if metrics.Requests < 10 {
			otherRows = append(otherRows, group...)
			otherCount++
			continue
		}
		rowMeta := cacheBreakdownMeta(group, q.Dimension, prices)
		label := cacheBreakdownLabel(labels[key], q.Dimension, key, rowMeta)
		candidates = append(candidates, CacheBreakdownRow{Key: key, Label: label, Meta: rowMeta, CacheBreakdownMetrics: metrics})
	}
	score := func(row CacheBreakdownRow) float64 {
		switch q.Sort {
		case "prompt":
			return float64(row.PromptTokens)
		case "token_hit":
			if row.HitUpper != nil {
				return *row.HitUpper
			}
			return -1
		case "opportunity_amount":
			if row.OpportunityAmount != nil {
				return *row.OpportunityAmount
			}
			return -1
		default:
			return float64(row.OpportunityTokens)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := score(candidates[i]), score(candidates[j])
		if a != b {
			return a > b
		}
		return candidates[i].Key < candidates[j].Key
	})
	for i, row := range candidates {
		if i < q.Limit {
			result.Rows = append(result.Rows, row)
		} else {
			otherRows = append(otherRows, grouped[row.Key]...)
			otherCount++
		}
	}
	if otherCount > 0 {
		result.Other = &CacheBreakdownOther{Count: otherCount, CacheBreakdownMetrics: cacheBreakdownMetrics(otherRows, prices, q.CostView, total, cost)}
	}
	return result
}
