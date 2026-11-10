//go:build unit

package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/antigravity"
	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
	"github.com/liulixin-lex/xy2api/internal/pkg/gemini"
	"github.com/liulixin-lex/xy2api/internal/pkg/geminicli"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/pkg/xai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestTimeoutPlanMetricModelCardinality(t *testing.T) {
	withResilienceSettings(t, nil)
	const groupID, accountID = int64(95130), int64(95131)
	for _, tc := range []struct{ input, want string }{
		{"openai/GPT-6", "gpt-6-astra"},
		{"anthropic/claude-sonnet-4-5", "claude-sonnet-4-5-20250929"},
		{"models/gemini-2.5-pro", "gemini-2.5-pro"},
		{"unknown-model", "other"},
		{"gpt-6-client-controlled", "other"},
		{"", "other"},
	} {
		require.Equal(t, tc.want, timeoutMetricModel(tc.input))
	}
	countOther := func() int64 {
		var total int64
		for _, row := range resilience.Snapshot(time.Hour) {
			if row.Name == "ttfo_lower_bound" && row.GroupID == groupID && row.AccountID == accountID {
				require.Equal(t, "other:10_30", row.Label)
				total += row.Count
			}
		}
		return total
	}
	before := countOther()
	const attempts = 200
	for i := range attempts {
		st := resilience.NewRequestState()
		st.SetGroup(groupID)
		ctx := resilience.WithRequestState(t.Context(), st)
		now := time.Now()
		st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, now))
		model := fmt.Sprintf("unknown-client-model-%d", i)
		st.SetAttemptProfile(resilience.AttemptProfile{AccountID: accountID, Model: model, AttemptStart: now})
		plan := st.PlanAttemptTimeout(20*time.Second, false, now)
		noteTimeoutPlan(ctx, &Account{ID: accountID}, plan)
		require.Equal(t, model, plan.Model(), "metric normalization must not change the frozen request model")
	}
	require.Equal(t, int64(attempts), countOther()-before)
	t.Logf("unknown_models=%d emitted_label=other:10_30 maximum_labels=%d", attempts, 5*(len(timeoutMetricModels)+1))
}

func TestTimeoutMetricIncludesAllBuiltinCatalogs(t *testing.T) {
	catalogs := map[string][]string{
		"claude": claude.DefaultModelIDs(),
		"openai": openai.DefaultModelIDs(),
		"grok":   xai.DefaultModelIDs(),
	}
	for _, model := range gemini.DefaultModels() {
		catalogs["gemini"] = append(catalogs["gemini"], model.Name)
	}
	for _, model := range antigravity.DefaultModels() {
		catalogs["antigravity"] = append(catalogs["antigravity"], model.ID)
	}
	for _, model := range geminicli.DefaultModels {
		catalogs["geminicli"] = append(catalogs["geminicli"], model.ID)
	}
	for _, model := range geminicli.GoogleOneModels {
		catalogs["google_one"] = append(catalogs["google_one"], model.ID)
	}
	for catalog, models := range catalogs {
		for _, model := range models {
			t.Run(catalog+"/"+model, func(t *testing.T) {
				require.NotEqual(t, "other", timeoutMetricModel(model), "declared built-in models must retain a known label")
			})
		}
	}
}
