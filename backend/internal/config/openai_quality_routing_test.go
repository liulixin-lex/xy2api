package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIQualityRoutingConfig(t *testing.T) {
	c := OpenAIQualityRoutingConfig{}
	require.Equal(t, "enforce", c.EffectiveMode())
	require.Equal(t, 300, c.EffectiveAvoidSeconds())
	for _, mode := range []string{"off", "observe", "enforce"} {
		c.Mode = mode
		require.NoError(t, c.Validate())
	}
	c.Mode = "typo"
	require.Error(t, c.Validate())
	c.Mode = "enforce"
	c.AvoidSeconds = -1
	require.Error(t, c.Validate())
}
