package domain

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIQTargetsPatchAcceptsLegacySettings(t *testing.T) {
	enabled := true
	var patch IQTargetsPatch
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"model":"custom-model","reasoning_effort":"upstream_default","interval_minutes":15,"timeout_seconds":90,"output_mode":"strict"}`), &patch))
	require.Len(t, patch.Targets, 3)
	require.Equal(t, []string{IQModelAstra, IQModelSol, "custom-model"}, []string{patch.Targets[0].Model, patch.Targets[1].Model, patch.Targets[2].Model})
	for _, target := range patch.Targets {
		require.Equal(t, &enabled, target.Enabled)
		require.Equal(t, 15, *target.IntervalMinutes)
		require.Equal(t, 90, *target.TimeoutSeconds)
		require.Equal(t, "upstream_default", *target.ReasoningEffort)
	}
}

func TestIQTargetSettingsValidation(t *testing.T) {
	require.NoError(t, (IQTargetSettings{Model: "custom", ReasoningEffort: strPtr("high"), IntervalMinutes: intPtr(5), TimeoutSeconds: intPtr(120)}).Validate())
	for _, target := range []IQTargetSettings{
		{Model: ""}, {Model: "has\ncontrol"}, {Model: "custom", ReasoningEffort: strPtr("Upper")},
		{Model: "custom", IntervalMinutes: intPtr(0)}, {Model: "custom", TimeoutSeconds: intPtr(29)},
	} {
		require.Error(t, target.Validate())
	}
}

func strPtr(value string) *string { return &value }
func intPtr(value int) *int       { return &value }
