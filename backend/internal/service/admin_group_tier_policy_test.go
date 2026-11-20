package service

import (
	"encoding/json"
	"math"
	"testing"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestGroupTierPolicyMigrationAndLegacyWrites(t *testing.T) {
	group := Group{Platform: PlatformOpenAI, ForceOpenAIFast: true, FreeOpenAIFast: true}
	inherit, on, off := OpenAITierModeInherit, OpenAITierModeForceOn, OpenAITierModeForceOff
	version, revision, no, yes := OpenAITierPolicyExplicit, "checked", false, true

	require.NoError(t, applyGroupOpenAITierPolicy(&group, &UpdateGroupInput{Name: "new name"}, false))
	require.Equal(t, OpenAITierPolicyLegacy, group.OpenAITierPolicyVersion)
	require.Equal(t, on, group.OpenAIFastMode)
	require.True(t, group.FreeOpenAIFast)

	input := &UpdateGroupInput{OpenAIUltrafastMode: &on, OpenAITierPolicyVersion: &version, Revision: &revision}
	require.Equal(t, "OPENAI_TIER_FORCE_CONFLICT", infraerrors.Reason(applyGroupOpenAITierPolicy(&group, input, false)))
	require.Equal(t, OpenAITierPolicyLegacy, group.OpenAITierPolicyVersion)

	input.OpenAIFastMode, input.FreeOpenAIFast, input.FreeOpenAIUltrafast = &off, &no, &yes
	input.Revision = nil
	require.ErrorIs(t, applyGroupOpenAITierPolicy(&group, input, false), ErrConfigurationRevision)
	input.Revision = &revision
	require.NoError(t, applyGroupOpenAITierPolicy(&group, input, false))
	require.Equal(t, OpenAITierPolicyExplicit, group.OpenAITierPolicyVersion)
	require.Equal(t, off, group.OpenAIFastMode)
	require.Equal(t, on, group.OpenAIUltrafastMode)
	require.False(t, group.FreeOpenAIFast)
	require.True(t, group.FreeOpenAIUltrafast)

	require.NoError(t, applyGroupOpenAITierPolicy(&group, &UpdateGroupInput{ForceOpenAIFast: &no}, false))
	require.Equal(t, off, group.OpenAIFastMode)
	require.Equal(t, "OPENAI_TIER_EXPLICIT_EDIT_REQUIRED", infraerrors.Reason(applyGroupOpenAITierPolicy(&group, &UpdateGroupInput{ForceOpenAIFast: &yes}, false)))
	require.Equal(t, "OPENAI_TIER_LEGACY_CONFLICT", infraerrors.Reason(applyGroupOpenAITierPolicy(&group, &UpdateGroupInput{OpenAIFastMode: &inherit, ForceOpenAIFast: &yes}, false)))

	group.Platform = PlatformAnthropic
	require.NoError(t, applyGroupOpenAITierPolicy(&group, &UpdateGroupInput{}, false))
	require.Equal(t, inherit, group.OpenAIFastMode)
	require.Equal(t, inherit, group.OpenAIUltrafastMode)
	require.False(t, group.FreeOpenAIUltrafast)
}

func TestUltrafastPricingDecimalAndOldClientRoundTrip(t *testing.T) {
	for _, input := range []string{"1", "0.5", "1e-6", "999999.999999", "1234567e-5"} {
		multiplier, err := DecodeUltrafastMultiplier(json.RawMessage(input))
		require.NoError(t, err, input)
		require.NotNil(t, multiplier, input)
	}
	for _, input := range []string{"0", "-1", "1e-7", "1.1234567", "1.0000000000000000001", "1000000", "1e309", `"2"`} {
		_, err := DecodeUltrafastMultiplier(json.RawMessage(input))
		require.Error(t, err, input)
	}
	multiplier := 4.5
	current := []ChannelModelPricing{{ID: 15, Platform: PlatformOpenAI, Models: []string{"gpt-a", "gpt-b"}, UltrafastMultiplier: &multiplier}}
	incoming := []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"gpt-b", "gpt-a"}}}
	got, err := mergeUltrafastPricing(current, incoming, false)
	require.NoError(t, err)
	require.Equal(t, multiplier, *got[0].UltrafastMultiplier)
	_, err = mergeUltrafastPricing(current, nil, false)
	require.Equal(t, "TIER_PRICING_IDENTITY_CONFLICT", infraerrors.Reason(err))

	incoming[0].UltrafastMultiplierSet = true
	_, err = mergeUltrafastPricing(current, incoming, false)
	require.ErrorIs(t, err, ErrConfigurationRevision)
	got, err = mergeUltrafastPricing(current, incoming, true)
	require.NoError(t, err)
	require.Nil(t, got[0].UltrafastMultiplier)

	incoming[0] = ChannelModelPricing{ID: 15, Platform: PlatformOpenAI, Models: []string{"gpt-c"}}
	got, err = mergeUltrafastPricing(current, incoming, true)
	require.NoError(t, err)
	require.Equal(t, multiplier, *got[0].UltrafastMultiplier)
}

func TestFastMultiplierPrecisionForOpenAITokenCards(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		good bool
	}{
		{"null", true},
		{"1", true},
		{"0.5", true},
		{"999999.999999", true},
		{"1e-6", true},
		{"0", false},
		{"-0.5", false},
		{"0.0000001", false},
		{"1.1234567", false},
		{"1.0000000000000000001", false},
		{"1000000", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			value, excess, err := DecodeFastMultiplier(json.RawMessage(tc.raw))
			require.NoError(t, err)
			card := ChannelModelPricing{Models: []string{"gpt-model"}, FastMultiplier: value, FastMultiplierExcessPrecision: excess}
			_, err = normalizeGroupModelPricing(PlatformOpenAI, []ChannelModelPricing{card})
			if tc.good {
				require.NoError(t, err)
			} else {
				require.Equal(t, "INVALID_FAST_MULTIPLIER", infraerrors.Reason(err))
			}
		})
	}
	for _, value := range []float64{math.Inf(-1), math.Inf(1), math.NaN()} {
		card := ChannelModelPricing{Platform: PlatformOpenAI, FastMultiplier: &value}
		require.Equal(t, "INVALID_FAST_MULTIPLIER", infraerrors.Reason(ValidateOpenAITierMultipliers(card)))
	}
	oldMultiplier := 1.1234567
	card := ChannelModelPricing{Platform: PlatformAnthropic, FastMultiplier: &oldMultiplier}
	require.NoError(t, ValidateOpenAITierMultipliers(card))
	card.Platform, card.BillingMode = PlatformOpenAI, BillingModePerRequest
	require.NoError(t, ValidateOpenAITierMultipliers(card))
}
