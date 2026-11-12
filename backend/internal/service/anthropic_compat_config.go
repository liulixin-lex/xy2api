package service

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

// NormalizeAnthropicCompatCredentials is the sole normalization contract for
// identity seed rotation in both single-account and transaction-locked SQL writes.
func NormalizeAnthropicCompatCredentials(account *Account) (apiKey, baseURL string, bearer bool) {
	baseURL = "https://api.anthropic.com"
	if account == nil {
		return
	}
	apiKey = strings.TrimSpace(account.GetCredential("api_key"))
	if endpoint := strings.TrimRight(strings.TrimSpace(account.GetCredential("base_url")), "/"); endpoint != "" {
		baseURL = endpoint
	}
	bearer = strings.TrimSpace(account.GetExtraString(anthropicAPIKeyAuthSchemeExtraKey)) == AnthropicAPIKeyAuthSchemeAuthorizationBearer
	return
}

// ValidateAnthropicCompatProfileExtra validates explicitly supplied configuration.
func ValidateAnthropicCompatProfileExtra(platform, accountType string, extra map[string]any) error {
	_, admissionPresent := extra[AnthropicCompatNewSessionsExtraKey]
	if _, present := extra[AnthropicCompatProfileExtraKey]; !present && !admissionPresent {
		return nil
	}
	if admissionPresent {
		if _, ok := extra[AnthropicCompatNewSessionsExtraKey].(bool); !ok {
			return infraerrors.BadRequest("ANTHROPIC_COMPAT_ADMISSION_INVALID", "anthropic_compat_new_sessions must be a boolean")
		}
	}
	if _, err := anthropicCompatProfileValue(extra); err != nil {
		return err
	}
	if platform != PlatformAnthropic || accountType != AccountTypeAPIKey {
		return infraerrors.BadRequest("ANTHROPIC_COMPAT_PROFILE_ACCOUNT_INVALID", "Claude Code request compatibility is only available for Anthropic API Key accounts")
	}
	return nil
}

// prepareAnthropicCompatExtra preserves only this feature's omitted setting and managed seed.
// Other extra keys retain the existing full-replacement semantics of account editing.
func prepareAnthropicCompatExtra(account, previous *Account, create bool) (map[string]any, error) {
	prepared := RedactAnthropicCompatExtra(account.Extra)
	if prepared == nil {
		prepared = make(map[string]any)
	}
	if !create && previous != nil {
		for _, key := range []string{AnthropicCompatProfileExtraKey, AnthropicCompatNewSessionsExtraKey} {
			if _, provided := prepared[key]; !provided {
				if value, exists := previous.Extra[key]; exists {
					prepared[key] = value
				}
			}
		}
	}

	if err := ValidateAnthropicCompatProfileExtra(account.Platform, account.Type, prepared); err != nil {
		return nil, err
	}
	candidate := *account
	candidate.Extra = prepared
	profile, err := ResolveAnthropicCompatProfile(&candidate)
	if err != nil {
		return nil, err
	}
	seed := ""
	if !create && previous != nil && !anthropicCompatIdentityChanged(previous, &candidate) {
		seed = previous.AnthropicCompatIdentitySeed()
	}
	if seed == "" && (profile != nil || (!create && previous != nil && previous.AnthropicCompatIdentitySeed() != "")) {
		bytes := make([]byte, 32)
		if _, err := rand.Read(bytes); err != nil {
			return nil, err
		}
		seed = hex.EncodeToString(bytes)
	}
	if seed != "" {
		prepared[AnthropicCompatIdentitySeedExtraKey] = seed
	}
	if len(prepared) == 0 && account.Extra == nil {
		return nil, nil
	}
	return prepared, nil
}

func anthropicCompatIdentityChanged(previous, next *Account) bool {
	if previous == nil || next == nil {
		return true
	}
	if previous.Platform != next.Platform || previous.Type != next.Type {
		return true
	}
	oldKey, oldURL, oldBearer := NormalizeAnthropicCompatCredentials(previous)
	newKey, newURL, newBearer := NormalizeAnthropicCompatCredentials(next)
	return oldKey != newKey || oldURL != newURL || oldBearer != newBearer
}

func prepareAnthropicCompatAccountUpdate(account, previous *Account) error {
	extra, err := prepareAnthropicCompatExtra(account, previous, false)
	if err == nil {
		account.Extra = extra
	}
	return err
}

func ShouldEnsureAnthropicCompatSeedForExtraUpdates(updates map[string]any) bool {
	_, authScheme := updates["anthropic_apikey_auth_scheme"]
	return updates[AnthropicCompatProfileExtraKey] == "claude_code" || authScheme
}
