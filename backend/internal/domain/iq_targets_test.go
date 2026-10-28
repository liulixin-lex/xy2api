package domain

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestIQTargetsPatchLegacyShape(t *testing.T) {
	var p IQTargetsPatch
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"model":"gpt-6-astra","reasoning_effort":"high","interval_minutes":15,"timeout_seconds":90,"output_mode":"strict"}`), &p))
	require.Len(t, p.Targets, 2)
	require.Equal(t, []string{IQModelAstra, IQModelSol}, []string{p.Targets[0].Model, p.Targets[1].Model})
	for _, target := range p.Targets {
		require.True(t, *target.Enabled)
		require.Equal(t, "high", *target.ReasoningEffort)
		require.Equal(t, 15, *target.IntervalMinutes)
		require.Equal(t, 90, *target.TimeoutSeconds)
	}
}
