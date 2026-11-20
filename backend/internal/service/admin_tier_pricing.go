package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

// DecodeUltrafastMultiplier preserves explicit null and validates decimal
// precision before the number is reduced to the database's scale.
func DecodeUltrafastMultiplier(raw json.RawMessage) (*float64, error) {
	value, excessPrecision, err := DecodeFastMultiplier(raw)
	if err != nil || value != nil && (!validTierMultiplier(*value) || excessPrecision) {
		return nil, invalidUltrafastMultiplier()
	}
	return value, nil
}

// DecodeFastMultiplier keeps excess decimal precision visible until the
// effective platform and billing mode are resolved.
func DecodeFastMultiplier(raw json.RawMessage) (value *float64, excessPrecision bool, err error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, false, nil
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false, err
	}
	number := strings.ToLower(string(raw))
	mantissa, exponent, _ := strings.Cut(number, "e")
	_, fraction, _ := strings.Cut(mantissa, ".")
	power := 0
	if exponent != "" {
		power, err = strconv.Atoi(exponent)
		if err != nil {
			return value, true, nil
		}
	}
	digits := strings.ReplaceAll(mantissa, ".", "")
	trailingZeroes := len(digits) - len(strings.TrimRight(digits, "0"))
	return value, len(fraction)-trailingZeroes-6 > power, nil
}

func validTierMultiplier(value float64) bool {
	if !(value > 0 && value <= 999999.999999) {
		return false
	}
	_, decimal, _ := strings.Cut(strconv.FormatFloat(value, 'f', -1, 64), ".")
	return len(decimal) <= 6
}

func invalidUltrafastMultiplier() error {
	return infraerrors.BadRequest("INVALID_ULTRAFAST_MULTIPLIER", "ultrafast_multiplier must be a positive number up to 999999.999999 with at most six decimal places")
}

func ValidateOpenAITierMultipliers(p ChannelModelPricing) error {
	openAIToken := p.Platform == PlatformOpenAI && (p.BillingMode == "" || p.BillingMode == BillingModeToken)
	if p.UltrafastMultiplier != nil {
		if !validTierMultiplier(*p.UltrafastMultiplier) {
			return invalidUltrafastMultiplier()
		}
		if !openAIToken {
			return infraerrors.BadRequest("ULTRAFAST_PRICING_UNSUPPORTED", "ultrafast_multiplier requires an OpenAI token price card")
		}
	}
	if openAIToken && p.FastMultiplier != nil &&
		(!validTierMultiplier(*p.FastMultiplier) || p.FastMultiplierExcessPrecision) {
		return infraerrors.BadRequest("INVALID_FAST_MULTIPLIER", "fast_multiplier must be a positive number up to 999999.999999 with at most six decimal places")
	}
	return nil
}

func tierPricingNaturalKey(p ChannelModelPricing) string {
	models := make([]string, len(p.Models))
	for i, model := range p.Models {
		models[i] = strings.ToLower(strings.TrimSpace(model))
	}
	slices.Sort(models)
	mode := p.BillingMode
	if mode == "" {
		mode = BillingModeToken
	}
	key, _ := json.Marshal([]any{p.Platform, mode, models})
	return string(key)
}

// mergeUltrafastPricing preserves a stored multiplier if the incoming card
// omits it. A checked revision allows intentional card deletion and ID-based
// changes to the card's platform, billing mode, or model names.
func mergeUltrafastPricing(stored, incoming []ChannelModelPricing, hasRevision bool) ([]ChannelModelPricing, error) {
	result := make([]ChannelModelPricing, len(incoming))
	storedByKey := make(map[string][]int, len(stored))
	storedByID := make(map[int64]int, len(stored))
	for i, pricing := range stored {
		key := tierPricingNaturalKey(pricing)
		storedByKey[key] = append(storedByKey[key], i)
		if pricing.ID != 0 {
			storedByID[pricing.ID] = i
		}
	}

	matched := make([]bool, len(stored))
	for i, pricing := range incoming {
		result[i] = pricing.Clone()
		result[i].UltrafastMultiplierSet = false
		candidates := storedByKey[tierPricingNaturalKey(pricing)]
		oldIndex, found := storedByID[pricing.ID]
		switch {
		case len(candidates) == 1:
			oldIndex, found = candidates[0], true
		case found && hasRevision:
		default:
			found = false
		}
		var oldMultiplier *float64
		if found {
			if matched[oldIndex] && stored[oldIndex].UltrafastMultiplier != nil {
				return nil, ErrConfigurationRevision
			}
			matched[oldIndex] = true
			oldMultiplier = stored[oldIndex].UltrafastMultiplier
		}
		if !pricing.UltrafastMultiplierSet && pricing.UltrafastMultiplier == nil {
			result[i].UltrafastMultiplier = oldMultiplier
		} else if !hasRevision && !sameTierMultiplier(oldMultiplier, pricing.UltrafastMultiplier) {
			return nil, ErrConfigurationRevision
		}
	}

	for i, pricing := range stored {
		if !matched[i] && pricing.UltrafastMultiplier != nil && !hasRevision {
			return nil, infraerrors.Conflict("TIER_PRICING_IDENTITY_CONFLICT", fmt.Sprintf("cannot match the Ultrafast price for %v, reload the price cards before saving", pricing.Models))
		}
	}
	return result, nil
}

func sameTierMultiplier(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ultrafastPricingChanged compares explicit overrides by their billing identity.
func ultrafastPricingChanged(current, incoming []ChannelModelPricing) bool {
	overrides := make(map[string]*float64)
	for _, pricing := range current {
		if pricing.UltrafastMultiplier != nil {
			overrides[tierPricingNaturalKey(pricing)] = pricing.UltrafastMultiplier
		}
	}
	for _, pricing := range incoming {
		key := tierPricingNaturalKey(pricing)
		if !sameTierMultiplier(overrides[key], pricing.UltrafastMultiplier) {
			return true
		}
		delete(overrides, key)
	}
	return len(overrides) != 0
}
