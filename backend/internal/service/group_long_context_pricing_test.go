//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeLongContextPricingConfig(t *testing.T) {
	scope, models, err := NormalizeLongContextPricingConfig("", []string{" GPT-5.6-* ", "gpt-5.6-*", "gpt-6-astra"})
	require.NoError(t, err)
	require.Equal(t, LongContextPricingScopeAll, scope)
	require.Equal(t, []string{"gpt-5.6-*", "gpt-6-astra"}, models)

	_, _, err = NormalizeLongContextPricingConfig("selected", []string{"gpt-*-sol"})
	require.ErrorContains(t, err, "exact model name")
	_, _, err = NormalizeLongContextPricingConfig("invalid", nil)
	require.ErrorContains(t, err, "must be all or selected")
}

func TestGroupLongContextPricingAppliesToModel(t *testing.T) {
	legacy := &Group{LongContextPricingEnabled: true}
	require.True(t, legacy.LongContextPricingAppliesToModel("gpt-6-astra"), "empty legacy scope means all")

	group := &Group{
		LongContextPricingEnabled: true,
		LongContextPricingScope:   LongContextPricingScopeSelected,
		LongContextPricingModels:  []string{"gpt-5.6-*", "GPT-5.5"},
	}
	require.True(t, group.LongContextPricingAppliesToModel("gpt-5.6-sol"))
	require.True(t, group.LongContextPricingAppliesToModel("gpt-5.6-terra-high"))
	require.True(t, group.LongContextPricingAppliesToModel("gpt-5.5"))
	require.False(t, group.LongContextPricingAppliesToModel("gpt-5.5-pro"))
	require.False(t, group.LongContextPricingAppliesToModel("gpt-6-astra"))

	group.LongContextPricingEnabled = false
	require.False(t, group.LongContextPricingAppliesToModel("gpt-5.6-sol"))
}

func TestLongContextPricingEnabledForRequestAccountOverride(t *testing.T) {
	accountEnabled := true
	selected := &Group{
		LongContextPricingEnabled: true,
		LongContextPricingScope:   LongContextPricingScopeSelected,
		LongContextPricingModels:  []string{"gpt-5.6-*"},
	}
	require.False(t, longContextPricingEnabledForRequest(selected, "gpt-6-astra", &accountEnabled))
	require.True(t, longContextPricingEnabledForRequest(selected, "gpt-5.6-sol", &accountEnabled))

	allDisabled := &Group{LongContextPricingScope: LongContextPricingScopeAll}
	require.True(t, longContextPricingEnabledForRequest(allDisabled, "gpt-6-astra", &accountEnabled))
}
