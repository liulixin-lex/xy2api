package service

import (
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// OpenAIFCOCompatExtraKey is the accounts.extra key of the per-account
// standalone function_call_output compatibility override. A missing key or
// JSON null inherits the global mode.
const OpenAIFCOCompatExtraKey = "openai_fco_compat_mode"

// ValidateOpenAIFCOCompatExtra checks only the override key: it must be
// missing, JSON null, or exactly off, reactive or proactive. Other extra keys
// are left alone.
func ValidateOpenAIFCOCompatExtra(extra map[string]any) error {
	raw, exists := extra[OpenAIFCOCompatExtraKey]
	if !exists || raw == nil {
		return nil
	}
	if mode, ok := raw.(string); ok && resilience.ValidFCOCompatMode(mode) {
		return nil
	}
	return infraerrors.BadRequest(
		"OPENAI_FCO_COMPAT_MODE_INVALID",
		OpenAIFCOCompatExtraKey+" must be off, reactive, proactive or null",
	)
}

// SanitizeOpenAIFCOCompatExtra removes an override value that
// ValidateOpenAIFCOCompatExtra would reject, so the account inherits the
// global mode. Valid values and JSON null are left as they are. Paths that
// copy extra from elsewhere (CRS sync, account duplication) use it instead of
// failing on a value they did not choose.
func SanitizeOpenAIFCOCompatExtra(extra map[string]any) {
	if ValidateOpenAIFCOCompatExtra(extra) != nil {
		delete(extra, OpenAIFCOCompatExtraKey)
	}
}

// openAIFCOCompatOverride returns the account's valid override. A stored
// value that is not a valid mode is treated as absent.
func openAIFCOCompatOverride(account *Account) (string, bool) {
	if account == nil {
		return "", false
	}
	mode, ok := account.Extra[OpenAIFCOCompatExtraKey].(string)
	if !ok || !resilience.ValidFCOCompatMode(mode) {
		return "", false
	}
	return mode, true
}

// ResolveOpenAIFCOCompatMode returns the effective mode for one account. The
// resilience master switch being off, or the global mode being off, forces
// off and no account override can bypass it; otherwise a valid account
// override wins over the global mode. A nil cfg resolves to off. It only
// resolves the mode: whether the account's type, platform or upstream is
// eligible is decided by the caller.
func ResolveOpenAIFCOCompatMode(account *Account, cfg *resilience.Settings) string {
	global := cfg.FCOCompatMode()
	if global == resilience.FCOCompatModeOff {
		return resilience.FCOCompatModeOff
	}
	if mode, ok := openAIFCOCompatOverride(account); ok {
		return mode
	}
	return global
}

// validateBulkOpenAIFCOCompatTargets requires every bulk target, after filters
// are resolved, to be an OpenAI API key account whenever the patch carries the
// override key (any value, null included). One mismatch rejects the batch.
func validateBulkOpenAIFCOCompatTargets(accountIDs []int64, targetsByID map[int64]*Account) error {
	for _, accountID := range accountIDs {
		account := targetsByID[accountID]
		if account == nil {
			return invalidBulkOpenAITarget(accountID, "account does not exist")
		}
		if account.Platform != PlatformOpenAI || account.Type != AccountTypeAPIKey {
			return invalidBulkOpenAITarget(accountID, OpenAIFCOCompatExtraKey+" only applies to OpenAI API key accounts")
		}
	}
	return nil
}
