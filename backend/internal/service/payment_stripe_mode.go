package service

import (
	"github.com/liulixin-lex/xy2api/internal/payment"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

// StripePaymentMode resolves legacy configurations with both modes to hosted.
// Historical providers remain available to callbacks, reconciliation and refunds.
func StripePaymentMode(types []string) string {
	mode := ""
	for _, kind := range NormalizeVisibleMethods(types) {
		if kind == payment.TypeStripeHosted {
			return kind
		}
		if kind == payment.TypeStripe {
			mode = kind
		}
	}
	return mode
}

func normalizeStripePaymentTypes(types []string) []string {
	mode := StripePaymentMode(types)
	result := make([]string, 0, len(types))
	for _, kind := range NormalizeVisibleMethods(types) {
		if (kind == payment.TypeStripe || kind == payment.TypeStripeHosted) && kind != mode {
			continue
		}
		result = append(result, kind)
	}
	return result
}

func ValidateStripePaymentTypes(types []string) error {
	hasEmbedded, hasHosted := false, false
	for _, kind := range NormalizeVisibleMethods(types) {
		hasEmbedded = hasEmbedded || kind == payment.TypeStripe
		hasHosted = hasHosted || kind == payment.TypeStripeHosted
	}
	if hasEmbedded && hasHosted {
		return infraerrors.BadRequest("STRIPE_MODE_CONFLICT", "select either embedded or hosted Stripe checkout")
	}
	return nil
}
