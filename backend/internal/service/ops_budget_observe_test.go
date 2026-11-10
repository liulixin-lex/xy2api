//go:build unit

package service

import (
	"encoding/json"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestOpsBudgetFieldsLegacyByteBoundNeutral(t *testing.T) {
	makeEvents := func(enriched bool) []*OpsUpstreamErrorEvent {
		events := make([]*OpsUpstreamErrorEvent, 256)
		for i := range events {
			event := &OpsUpstreamErrorEvent{Message: strings.Repeat("x", 2040), Kind: "first_output_timeout", AccountID: int64(i + 1)}
			if enriched {
				event.TimeoutDiagnostics = resilience.TimeoutDiagnostics{TimeoutSource: "test", TierMs: 40000, PlannedTimeoutMs: 120000, EffectiveTimeoutMs: 7000, BudgetRemainingMsAtStart: 7000, WouldBudgetCut: true, AnchorOffsetMs: 100000, AttemptStartOffsetMs: 233000, ShadowEOFRemainingMs: 57000}
			}
			events[i] = event
		}
		return events
	}
	old, oldDropped := boundOpsUpstreamErrors(makeEvents(false))
	enriched, dropped := boundOpsUpstreamErrors(makeEvents(true))
	require.Equal(t, oldDropped, dropped)
	require.Len(t, enriched, len(old))
	for i, event := range enriched {
		copy := *event
		copy.TimeoutDiagnostics = resilience.TimeoutDiagnostics{}
		before, err := json.Marshal(old[i])
		require.NoError(t, err)
		after, err := json.Marshal(copy)
		require.NoError(t, err)
		require.Equal(t, string(before), string(after))
	}
	zero, _ := json.Marshal(OpsUpstreamErrorEvent{})
	require.NotContains(t, string(zero), "timeout_source")
	require.NotContains(t, string(zero), "would_budget_cut")
	require.NotContains(t, string(zero), "budget_cut_applied")
	// Existing decoding is intentionally permissive about additional JSON keys.
	var decoded []*OpsUpstreamErrorEvent
	raw, _ := json.Marshal(enriched)
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.True(t, decoded[0].WouldBudgetCut)
}
