package domain

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestIQTargetsPatchLegacyShape(t *testing.T) {
	var p IQTargetsPatch
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"model":"gpt-6-astra","reasoning_effort":"high","interval_minutes":15,"timeout_seconds":90,"output_mode":"strict"}`), &p))
	require.Len(t, p.Targets, 1)
	require.Equal(t, "gpt-6-astra", p.Targets[0].Model)
	require.True(t, *p.Targets[0].Enabled)
	require.Equal(t, "high", *p.Targets[0].ReasoningEffort)
	require.Equal(t, 15, *p.Targets[0].IntervalMinutes)
	require.Equal(t, 90, *p.Targets[0].TimeoutSeconds)
}
