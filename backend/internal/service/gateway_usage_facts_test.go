//go:build unit

package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGatewayUsageFactsUseObservedPresence(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		present    bool
	}{
		{"missing", `{}`, false}, {"null", `{"usage":null}`, false}, {"empty", `{"usage":{}}`, false}, {"zero", `{"usage":{"input_tokens":0,"output_tokens":0}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage, present := openAIResponsesUsageEvidence([]byte(tc.body), "upstream_json")
			require.Equal(t, tc.present, present)
			require.Zero(t, usage.InputTokens)
			source := "missing"
			if present {
				source = "upstream_json"
			}
			var row UsageLog
			applyUsageResponseFacts(&row, true, present, source, false, false, "json")
			frozen := BillingUsageSnapshot(&row)
			*row.UsageObserved = !*row.UsageObserved
			*row.UsageSource = "changed"
			require.Equal(t, tc.present, *frozen.UsageObserved)
			require.Equal(t, source, *frozen.UsageSource)
			applyUsageResponseFacts(frozen, false, false, "", false, false, "")
			require.Equal(t, tc.present, *frozen.UsageObserved)
		})
	}
	var unknown UsageLog
	applyUsageResponseFacts(&unknown, false, true, "upstream_json", true, true, "sse")
	require.Nil(t, unknown.UsageObserved)
	require.Nil(t, unknown.TerminalEventValid)
}
