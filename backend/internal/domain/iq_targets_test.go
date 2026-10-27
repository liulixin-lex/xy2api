package domain

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestIQTargetsPatchLegacyShape(t *testing.T) {
	var p IQTargetsPatch
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"model":"gpt-6-astra","interval_minutes":15}`), &p))
	require.Len(t, p.Targets, 1)
	require.Equal(t, "gpt-6-astra", p.Targets[0].Model)
}
