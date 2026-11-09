package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
	"github.com/liulixin-lex/xy2api/internal/pkg/gemini"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/pkg/xai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func noteTimeoutPlan(ctx context.Context, account *Account, plan resilience.TimeoutPlan) {
	d := plan.Diagnostics()
	if d.TimeoutSource == "" {
		return
	}
	st := resilience.StateFrom(ctx)
	id := int64(0)
	if account != nil {
		id = account.ID
	}
	resilience.Count("budget_cut_fot", st.GroupID(), id, fmt.Sprintf("would=%t,applied=%t", d.WouldBudgetCut, d.BudgetCutApplied))
	bucket := "lt10"
	if d.TTFOLowerBoundMs >= 120000 {
		bucket = "ge120"
	} else if d.TTFOLowerBoundMs >= 60000 {
		bucket = "60_120"
	} else if d.TTFOLowerBoundMs >= 30000 {
		bucket = "30_60"
	} else if d.TTFOLowerBoundMs >= 10000 {
		bucket = "10_30"
	}
	resilience.Count("ttfo_lower_bound", st.GroupID(), id, timeoutMetricModel(plan.Model())+":"+bucket)
	slog.Info("resilience.budget_timeout_plan", "account_id", id, "timeout_source", d.TimeoutSource, "tier_ms", d.TierMs, "planned_timeout_ms", d.PlannedTimeoutMs, "effective_timeout_ms", d.EffectiveTimeoutMs, "budget_remaining_ms_at_start", d.BudgetRemainingMsAtStart, "would_budget_cut", d.WouldBudgetCut, "budget_cut_applied", d.BudgetCutApplied, "budget_cut_after_full_tier", d.CutAfterFullTier, "anchor_offset_ms", d.AnchorOffsetMs, "attempt_start_offset_ms", d.AttemptStartOffsetMs, "committed_at_start", d.CommittedAtStart, "shadow_eof_remaining_ms", d.ShadowEOFRemainingMs)
}

// Snapshot the built-in catalogs: client names never add metric label values.
// Five time buckets yield at most 5*(len(timeoutMetricModels)+1) labels here;
// the shared metric store may still collapse rows into its overflow label.
var timeoutMetricModels = func() map[string]struct{} {
	models := append(claude.DefaultModelIDs(), openai.DefaultModelIDs()...)
	models = append(models, xai.DefaultModelIDs()...)
	for _, model := range gemini.DefaultModels() {
		models = append(models, model.Name)
	}
	known := make(map[string]struct{}, len(models))
	for _, model := range models {
		known[claude.NormalizeModelID(normalizeModelNameForPricing(model))] = struct{}{}
	}
	return known
}()

func timeoutMetricModel(model string) string {
	model = strings.ToLower(lastOpenAIModelSegment(model))
	model = claude.NormalizeModelID(normalizeModelNameForPricing(model))
	if _, known := timeoutMetricModels[model]; known {
		return model
	}
	return "other"
}
