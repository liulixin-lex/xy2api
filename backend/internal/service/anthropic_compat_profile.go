package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"maps"
	"reflect"
	"strings"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

const (
	AnthropicCompatProfileExtraKey = "anthropic_compat_profile"
	// AnthropicCompatIdentitySeedExtraKey is server-managed and must not be returned or exported.
	AnthropicCompatIdentitySeedExtraKey = "anthropic_compat_identity_seed"
	AnthropicCompatProfileOff           = "off"
	AnthropicCompatProfileClaudeCodeV1  = "claude_code_v1"
	AnthropicCompatProfileClaudeCodeV2  = "claude_code_v2"
	AnthropicCompatNewSessionsExtraKey  = "anthropic_compat_new_sessions"
)

// AnthropicCompatProfile is a fixed wire contract, independent of authentication.
// ResolveAnthropicCompatProfile returns a fresh copy so callers cannot mutate the shared definition.
type AnthropicCompatProfile struct {
	ID, Revision, UserAgent, APIVersion, SystemIdentity string
	RequiredBetas                                       []string
}

var claudeCodeV2CompatProfile = AnthropicCompatProfile{
	ID:             AnthropicCompatProfileClaudeCodeV2,
	Revision:       "claude_code_v2.20261111",
	UserAgent:      "claude-cli/2.1.114 (external, sdk-cli)",
	APIVersion:     "2023-06-01",
	SystemIdentity: claudeCodeSystemPrompt,
	RequiredBetas:  []string{"claude-code-20250219", "interleaved-thinking-2025-05-14", "context-management-2025-06-27", "prompt-caching-scope-2026-01-05", "advisor-tool-2026-03-01"},
}

var claudeCodeV1CompatProfile = AnthropicCompatProfile{
	ID:             AnthropicCompatProfileClaudeCodeV1,
	Revision:       "claude_code_v1.20261110",
	UserAgent:      "claude-cli/2.1.114 (external, sdk-cli)",
	APIVersion:     "2023-06-01",
	SystemIdentity: claudeCodeSystemPrompt,
	// This combination is the measured baseline, not proof that each token is individually required.
	RequiredBetas: []string{"claude-code-20250219", "interleaved-thinking-2025-05-14", "context-management-2025-06-27", "prompt-caching-scope-2026-01-05", "advisor-tool-2026-03-01"},
}

func (p *AnthropicCompatProfile) SupportsModel(model string) bool {
	return p != nil && (p.ID == AnthropicCompatProfileClaudeCodeV1 || p.ID == AnthropicCompatProfileClaudeCodeV2) && (model == "claude-opus-4-8" || model == "claude-opus-5-5")
}

func anthropicCompatProfileValue(extra map[string]any) (string, error) {
	raw, present := extra[AnthropicCompatProfileExtraKey]
	if !present {
		return AnthropicCompatProfileOff, nil
	}
	value, ok := raw.(string)
	if !ok || (value != AnthropicCompatProfileOff && value != AnthropicCompatProfileClaudeCodeV1 && value != AnthropicCompatProfileClaudeCodeV2) {
		return "", infraerrors.BadRequest("ANTHROPIC_COMPAT_PROFILE_INVALID", "anthropic_compat_profile must be off, claude_code_v1 or claude_code_v2")
	}
	return value, nil
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

// Static capabilities do not depend on secrets, header overrides or beta policy.
// A configured model with broken account readiness is unavailable, not absent.
func configuredAnthropicCompatProfile(account *Account) (*AnthropicCompatProfile, error) {
	if account == nil {
		return nil, nil
	}
	mode, err := anthropicCompatProfileValue(account.Extra)
	if err != nil || mode == AnthropicCompatProfileOff {
		return nil, err
	}
	if err := ValidateAnthropicCompatProfileExtra(account.Platform, account.Type, account.Extra); err != nil {
		return nil, err
	}
	profile := claudeCodeV1CompatProfile
	if mode == AnthropicCompatProfileClaudeCodeV2 {
		profile = claudeCodeV2CompatProfile
	}
	profile.RequiredBetas = append([]string(nil), profile.RequiredBetas...)
	return &profile, nil
}

func ResolveAnthropicCompatProfile(account *Account) (*AnthropicCompatProfile, error) {
	profile, err := configuredAnthropicCompatProfile(account)
	if err != nil || profile == nil {
		return profile, err
	}
	return validateAnthropicCompatProfileHeaders(account, profile)
}

func validateAnthropicCompatProfileHeaders(account *Account, profile *AnthropicCompatProfile) (*AnthropicCompatProfile, error) {
	if account.IsAnthropicAPIKeyPassthroughEnabled() {
		return nil, infraerrors.BadRequest("ANTHROPIC_COMPAT_PROFILE_CONFLICT", "Claude Code request compatibility conflicts with anthropic_passthrough")
	}
	for name, value := range account.GetHeaderOverrides() {
		expected := ""
		switch name {
		case "user-agent":
			expected = profile.UserAgent
		case "x-app":
			expected = "cli"
		case "anthropic-version":
			expected = profile.APIVersion
		case "x-claude-code-session-id":
			if value != "" {
				return nil, infraerrors.BadRequest("ANTHROPIC_COMPAT_PROFILE_HEADER_CONFLICT", "header override x-claude-code-session-id conflicts with the derived compatibility session")
			}
			continue
		case "anthropic-beta":
			for _, required := range profile.RequiredBetas {
				found := false
				for _, token := range strings.Split(value, ",") {
					if strings.TrimSpace(token) == required {
						found = true
						break
					}
				}
				if !found {
					return nil, infraerrors.BadRequest("ANTHROPIC_COMPAT_PROFILE_HEADER_CONFLICT", "header override anthropic-beta omits the Claude Code compatibility baseline")
				}
			}
			continue
		default:
			continue
		}
		if value != expected {
			return nil, infraerrors.BadRequest("ANTHROPIC_COMPAT_PROFILE_HEADER_CONFLICT", "header override "+name+" conflicts with Claude Code request compatibility")
		}
	}
	return profile, nil
}

// A stored history revision selects its original adapter. Changing the default
// only affects new sessions; disabling compatibility is an explicit hard stop.
func resolveAnthropicCompatProfile(ctx context.Context, account *Account) (*AnthropicCompatProfile, error) {
	profile, err := configuredAnthropicCompatProfile(account)
	if err != nil || profile == nil {
		return profile, err
	}
	if scope := compatScope(ctx); scope != nil && scope.Record != nil {
		switch scope.Record.ProfileRevision {
		case claudeCodeV1CompatProfile.Revision:
			copy := claudeCodeV1CompatProfile
			profile = &copy
		case claudeCodeV2CompatProfile.Revision:
			copy := claudeCodeV2CompatProfile
			profile = &copy
		default:
			return nil, compatInvalid("anthropic_compat_history_incompatible", "Signed history requires an unavailable compatibility revision", AnthropicCompatReasonProfile)
		}
		profile.RequiredBetas = append([]string(nil), profile.RequiredBetas...)
	}
	return validateAnthropicCompatProfileHeaders(account, profile)
}

func anthropicCompatAcceptsNewSessions(account *Account) bool {
	value, present := account.Extra[AnthropicCompatNewSessionsExtraKey]
	return !present || value == true
}

func (a *Account) AnthropicCompatIdentitySeed() string {
	if a == nil {
		return ""
	}
	value, _ := a.Extra[AnthropicCompatIdentitySeedExtraKey].(string)
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 || strings.ToLower(value) != value {
		return ""
	}
	return value
}

func RedactAnthropicCompatExtra(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	redacted := maps.Clone(extra)
	delete(redacted, AnthropicCompatIdentitySeedExtraKey)
	return redacted
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
	if !create && previous != nil {
		seed = previous.AnthropicCompatIdentitySeed()
	}
	if seed == "" && profile != nil {
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
	for _, key := range []string{"api_key", "base_url", "auth_scheme"} {
		if !reflect.DeepEqual(previous.Credentials[key], next.Credentials[key]) {
			return true
		}
	}
	return !reflect.DeepEqual(previous.Extra["anthropic_apikey_auth_scheme"], next.Extra["anthropic_apikey_auth_scheme"])
}

func prepareAnthropicCompatAccountUpdate(account, previous *Account) error {
	trusted := *previous
	if anthropicCompatIdentityChanged(previous, account) {
		trusted.Extra = RedactAnthropicCompatExtra(previous.Extra)
	}
	extra, err := prepareAnthropicCompatExtra(account, &trusted, false)
	if err == nil {
		account.Extra = extra
	}
	return err
}

func (s *adminServiceImpl) validateAnthropicCompatConfiguration(ctx context.Context, account *Account) error {
	profile, err := ResolveAnthropicCompatProfile(account)
	if err != nil || profile == nil || s.settingService == nil {
		return err
	}
	settings, err := s.settingService.GetBetaPolicySettings(ctx)
	if err != nil {
		return err
	}
	for _, rule := range settings.Rules {
		if !betaPolicyScopeMatches(rule.Scope, false, false) {
			continue
		}
		for _, beta := range profile.RequiredBetas {
			if rule.BetaToken != beta {
				continue
			}
			for _, model := range []string{"claude-opus-4-8", "claude-opus-5-5"} {
				action, _ := resolveRuleAction(rule, model)
				if action == BetaPolicyActionBlock || action == BetaPolicyActionFilter {
					return infraerrors.BadRequest("ANTHROPIC_COMPAT_PROFILE_BETA_CONFLICT", "beta policy conflicts with Claude Code compatibility baseline: "+beta)
				}
			}
		}
	}
	return nil
}

func ShouldEnsureAnthropicCompatSeedForExtraUpdates(updates map[string]any) bool {
	_, authScheme := updates["anthropic_apikey_auth_scheme"]
	return updates[AnthropicCompatProfileExtraKey] == AnthropicCompatProfileClaudeCodeV1 || updates[AnthropicCompatProfileExtraKey] == AnthropicCompatProfileClaudeCodeV2 || authScheme
}
