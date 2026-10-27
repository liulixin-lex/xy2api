package service

import (
	"strings"

	"github.com/liulixin-lex/xy2api/internal/domain"
)

func iqGateModelKey(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	return domain.IQGateKeyPrefix + model
}

func CanonicalIQOutboundModel(account *Account, model string) string {
	return canonicalOpenAIAccountSchedulingModel(account, model)
}

func iqGateKeyPresent(account *Account, model string) bool {
	return accountHasIQGate(account)
}

func accountHasIQGate(account *Account) bool {
	if account == nil || account.Extra == nil {
		return false
	}
	limits, _ := account.Extra[modelRateLimitsKey].(map[string]any)
	for key := range limits {
		if domain.IsIQGateKey(key) {
			return true
		}
	}
	return false
}

func iqGateKeysForRequest(account *Account, model string, requireCompact bool, compactOutbound ...string) []string {
	if account == nil || !accountHasIQGate(account) {
		return nil
	}
	keys := []string{iqGateModelKey(canonicalOpenAIAccountSchedulingModel(account, model))}
	if requireCompact {
		compact := resolveOpenAIAccountUpstreamModelForRequest(account, model, true)
		if len(compactOutbound) > 0 && strings.TrimSpace(compactOutbound[0]) != "" {
			compact = strings.TrimSpace(compactOutbound[0])
		}
		if compact != "" {
			key := iqGateModelKey(compact)
			if key != keys[0] {
				keys = append(keys, key)
			}
		}
	}
	return keys
}

func iqGateBlocksRequest(account *Account, model string, requireCompact bool, compactOutbound ...string) bool {
	for _, key := range iqGateKeysForRequest(account, model, requireCompact, compactOutbound...) {
		if account.hasRateLimitKey(key) {
			return true
		}
	}
	return false
}
