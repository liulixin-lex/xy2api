//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestM1ActualEffortFromWire(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"reasoning":{"effort":"max"}}`, "max"},
		{`{"reasoning":{"effort":"xhigh"}}`, "xhigh"},
		{`{"reasoning":{"effort":"ultra"}}`, "ultra"},
		{`{"reasoning_effort":"none"}`, "none"},
		{`{"model":"gpt-5.4-high"}`, ""},
		{`{"reasoning":{"effort":null}}`, ""},
		{`{"reasoning":{"effort":"unknown"}}`, ""},
		{`{"thinking":{"type":"disabled"}}`, ""},
	} {
		t.Run(tc.body, func(t *testing.T) {
			got := extractOpenAIReasoningEffortFromBody([]byte(tc.body), "gpt-5.4-high")
			var object map[string]any
			require.NoError(t, json.Unmarshal([]byte(tc.body), &object))
			require.Equal(t, got, extractOpenAIReasoningEffort(object, "gpt-5.4-high"))
			if tc.want == "" {
				require.Nil(t, got)
			} else {
				require.NotNil(t, got)
				require.Equal(t, tc.want, *got)
				if tc.want == "ultra" {
					require.Equal(t, 1.0, reasoningEffortBillingMultiplier(*got, map[string]float64{"ultra": 9}))
				}
				if tc.want == "max" || tc.want == "xhigh" {
					want := map[string]float64{"max": 3, "xhigh": 2}
					require.Equal(t, want[tc.want], reasoningEffortBillingMultiplier(*got, want))
				}
			}
		})
	}
}
