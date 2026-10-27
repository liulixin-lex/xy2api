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

func iqGateKeyPresent(account *Account, model string) bool {
	if account == nil || account.Extra == nil {
		return false
	}
	limits, _ := account.Extra[modelRateLimitsKey].(map[string]any)
	_, ok := limits[iqGateModelKey(model)]
	return ok
}

func iqGateKeysForRequest(account *Account, model string, requireCompact bool) []string {
	if account == nil || !iqGateKeyPresent(account, model) {
		return nil
	}
	keys := []string{iqGateModelKey(canonicalOpenAIAccountSchedulingModel(account, model))}
	if requireCompact {
		if compact := resolveOpenAIAccountUpstreamModelForRequest(account, model, true); compact != "" {
			key := iqGateModelKey(compact)
			if key != keys[0] {
				keys = append(keys, key)
			}
		}
	}
	return keys
}
