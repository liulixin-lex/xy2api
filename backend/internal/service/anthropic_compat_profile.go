package service

import (
	"context"
	"encoding/hex"
	"maps"
	"strings"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

const (
	AnthropicCompatProfileExtraKey = "anthropic_compat_profile"
	// AnthropicCompatIdentitySeedExtraKey is server-managed and must not be returned or exported.
	AnthropicCompatIdentitySeedExtraKey = "anthropic_compat_identity_seed"
	AnthropicCompatProfileOff           = "off"
	AnthropicCompatProfileClaudeCode    = "claude_code"
	AnthropicCompatNewSessionsExtraKey  = "anthropic_compat_new_sessions"
)

// AnthropicCompatProfile is a fixed wire contract, independent of authentication.
// ResolveAnthropicCompatProfile returns a fresh copy so callers cannot mutate the shared definition.
type AnthropicCompatProfile struct {
	ID, UserAgent, APIVersion, SystemIdentity string
	RequiredBetas                             []string
}

var claudeCodeCompatProfile = AnthropicCompatProfile{
	ID:             AnthropicCompatProfileClaudeCode,
	UserAgent:      "claude-cli/2.1.114 (external, sdk-cli)",
	APIVersion:     "2023-06-01",
	SystemIdentity: claudeCodeSystemPrompt,
	// All Claude models retain the v0.6.3 five-beta baseline. Haiku and Sonnet on CC-only upstreams have not been verified by the gateway; administrators must test each model.
	RequiredBetas: []string{"claude-code-20250219", "interleaved-thinking-2025-05-14", "context-management-2025-06-27", "prompt-caching-scope-2026-01-05", "advisor-tool-2026-03-01"},
}

func (p *AnthropicCompatProfile) SupportsModel(model string) bool {
	return p != nil && strings.HasPrefix(model, "claude-")
}

func anthropicCompatProfileValue(extra map[string]any) (string, error) {
	raw, present := extra[AnthropicCompatProfileExtraKey]
	if !present {
		return AnthropicCompatProfileOff, nil
	}
	value, ok := raw.(string)
	if !ok || (value != AnthropicCompatProfileOff && value != AnthropicCompatProfileClaudeCode) {
		return "", infraerrors.BadRequest("ANTHROPIC_COMPAT_PROFILE_INVALID", "anthropic_compat_profile must be off, claude_code")
	}
	return value, nil
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
	profile := claudeCodeCompatProfile
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
