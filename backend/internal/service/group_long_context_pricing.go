package service

import (
	"fmt"
	"strings"
)

// NormalizeLongContextPricingConfig validates the range selector and stores
// model patterns in the same normalized form used by model pricing matching.
func NormalizeLongContextPricingConfig(scope string, models []string) (string, []string, error) {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		scope = LongContextPricingScopeAll
	}
	if scope != LongContextPricingScopeAll && scope != LongContextPricingScopeSelected {
		return "", nil, fmt.Errorf("long_context_pricing_scope must be all or selected")
	}

	seen := make(map[string]struct{}, len(models))
	normalized := make([]string, 0, len(models))
	for _, raw := range models {
		model := normalizeChannelPricingModelName(raw)
		if model == "" {
			continue
		}
		if strings.Count(model, "*") > 1 || (strings.Contains(model, "*") && !strings.HasSuffix(model, "*")) || model == "*" {
			return "", nil, fmt.Errorf("long-context model pattern %q must be an exact model name or a non-empty prefix ending in *", raw)
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		normalized = append(normalized, model)
	}
	return scope, normalized, nil
}

// LongContextPricingAppliesToModel is the single model-level gate shared by
// billing and model-plaza schedule generation.
func (g *Group) LongContextPricingAppliesToModel(model string) bool {
	if g == nil {
		return true
	}
	if !g.LongContextPricingEnabled {
		return false
	}
	if g.LongContextPricingScope != LongContextPricingScopeSelected {
		return true
	}

	model = normalizeChannelPricingModelName(model)
	for _, raw := range g.LongContextPricingModels {
		pattern := normalizeChannelPricingModelName(raw)
		if strings.HasSuffix(pattern, "*") {
			if prefix := strings.TrimSuffix(pattern, "*"); prefix != "" && strings.HasPrefix(model, prefix) {
				return true
			}
			continue
		}
		if pattern != "" && pattern == model {
			return true
		}
	}
	return false
}

func (g *Group) allowsAccountLongContextPricingOverride() bool {
	return g == nil || g.LongContextPricingScope != LongContextPricingScopeSelected
}

func longContextPricingEnabledForRequest(group *Group, model string, accountEnabled *bool) bool {
	enabled := group.LongContextPricingAppliesToModel(model)
	if group == nil {
		if accountEnabled != nil {
			return *accountEnabled
		}
		return enabled
	}
	if group.allowsAccountLongContextPricingOverride() && accountEnabled != nil && *accountEnabled {
		return true
	}
	return enabled
}
