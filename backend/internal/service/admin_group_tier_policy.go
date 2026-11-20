package service

import (
	"time"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

const (
	OpenAITierModeInherit  = "inherit"
	OpenAITierModeForceOn  = "force_on"
	OpenAITierModeForceOff = "force_off"

	OpenAITierPolicyLegacy   = 1
	OpenAITierPolicyExplicit = 2
)

var ErrConfigurationRevision = infraerrors.Conflict("CONFIGURATION_REVISION_CONFLICT", "configuration changed, reload it before saving")

// ConfigurationRevision uses the database timestamp returned by the write.
func ConfigurationRevision(updatedAt time.Time) string {
	return updatedAt.UTC().Format(time.RFC3339Nano)
}

func checkConfigurationRevision(revision *string, updatedAt time.Time) error {
	if revision == nil {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *revision)
	if err != nil || !parsed.Equal(updatedAt) {
		return ErrConfigurationRevision
	}
	return nil
}

func legacyOpenAIFastMode(force bool) string {
	if force {
		return OpenAITierModeForceOn
	}
	return OpenAITierModeInherit
}

// NormalizeGroupOpenAITierPolicy supplies defaults for groups created by older callers
// and clears settings when the group changes to an unsupported platform.
func NormalizeGroupOpenAITierPolicy(group *Group) {
	if group.OpenAITierPolicyVersion == 0 {
		group.OpenAITierPolicyVersion = OpenAITierPolicyLegacy
	}
	if group.OpenAIFastMode == "" || group.OpenAITierPolicyVersion == OpenAITierPolicyLegacy {
		group.OpenAIFastMode = legacyOpenAIFastMode(group.ForceOpenAIFast)
	}
	if group.OpenAIUltrafastMode == "" || group.OpenAITierPolicyVersion == OpenAITierPolicyLegacy {
		group.OpenAIUltrafastMode = OpenAITierModeInherit
	}
	if !groupSupportsOpenAIFast(group.Platform) {
		group.OpenAIFastMode = OpenAITierModeInherit
		group.OpenAIUltrafastMode = OpenAITierModeInherit
		group.FreeOpenAIFast = false
		group.FreeOpenAIUltrafast = false
	}
	group.ForceOpenAIFast = group.OpenAIFastMode == OpenAITierModeForceOn
}

func validOpenAITierMode(mode *string) bool {
	return mode == nil || *mode == OpenAITierModeInherit || *mode == OpenAITierModeForceOn || *mode == OpenAITierModeForceOff
}

func applyGroupOpenAITierPolicy(group *Group, input *UpdateGroupInput, creating bool) error {
	NormalizeGroupOpenAITierPolicy(group)
	if !validOpenAITierMode(input.OpenAIFastMode) || !validOpenAITierMode(input.OpenAIUltrafastMode) {
		return infraerrors.BadRequest("INVALID_OPENAI_TIER_MODE", "OpenAI tier mode must be inherit, force_on, or force_off")
	}
	if input.OpenAITierPolicyVersion != nil && *input.OpenAITierPolicyVersion != OpenAITierPolicyLegacy && *input.OpenAITierPolicyVersion != OpenAITierPolicyExplicit {
		return infraerrors.BadRequest("INVALID_OPENAI_TIER_POLICY_VERSION", "unknown OpenAI tier policy version")
	}
	if input.OpenAIFastMode != nil && input.ForceOpenAIFast != nil && (*input.OpenAIFastMode == OpenAITierModeForceOn) != *input.ForceOpenAIFast {
		return infraerrors.BadRequest("OPENAI_TIER_LEGACY_CONFLICT", "force_openai_fast conflicts with openai_fast_mode")
	}
	version := group.OpenAITierPolicyVersion
	if input.OpenAITierPolicyVersion != nil {
		version = *input.OpenAITierPolicyVersion
	}
	rollback := group.OpenAITierPolicyVersion == OpenAITierPolicyExplicit && version == OpenAITierPolicyLegacy
	if rollback {
		if input.Revision == nil {
			return ErrConfigurationRevision
		}
		if input.OpenAIFastMode == nil || *input.OpenAIFastMode == OpenAITierModeForceOff ||
			input.OpenAIUltrafastMode == nil || *input.OpenAIUltrafastMode != OpenAITierModeInherit ||
			input.FreeOpenAIUltrafast == nil || *input.FreeOpenAIUltrafast {
			return infraerrors.BadRequest("OPENAI_TIER_LEGACY_ROLLBACK_INVALID", "to restore the legacy policy, explicitly set a compatible Fast mode, Ultrafast inherit, and free_openai_ultrafast=false")
		}
	}
	if !groupSupportsOpenAIFast(group.Platform) {
		if (input.OpenAIFastMode != nil && *input.OpenAIFastMode != OpenAITierModeInherit) ||
			(input.OpenAIUltrafastMode != nil && *input.OpenAIUltrafastMode != OpenAITierModeInherit) ||
			(input.FreeOpenAIUltrafast != nil && *input.FreeOpenAIUltrafast) {
			return infraerrors.BadRequest("OPENAI_TIER_PLATFORM_UNSUPPORTED", "OpenAI tier policy requires an OpenAI or Composite group")
		}
		if rollback {
			group.OpenAITierPolicyVersion = version
		}
		return nil
	}

	fast, ultra, freeUltra := group.OpenAIFastMode, group.OpenAIUltrafastMode, group.FreeOpenAIUltrafast
	if input.OpenAIFastMode != nil {
		fast = *input.OpenAIFastMode
	} else if input.ForceOpenAIFast != nil && *input.ForceOpenAIFast != group.ForceOpenAIFast {
		if group.OpenAITierPolicyVersion == OpenAITierPolicyExplicit {
			return infraerrors.Conflict("OPENAI_TIER_EXPLICIT_EDIT_REQUIRED", "edit openai_fast_mode using the current group configuration")
		}
		fast = legacyOpenAIFastMode(*input.ForceOpenAIFast)
	}
	if input.OpenAIUltrafastMode != nil {
		ultra = *input.OpenAIUltrafastMode
	}
	if input.FreeOpenAIUltrafast != nil {
		freeUltra = *input.FreeOpenAIUltrafast
	}
	if fast == OpenAITierModeForceOn && ultra == OpenAITierModeForceOn {
		return infraerrors.BadRequest("OPENAI_TIER_FORCE_CONFLICT", "Fast and Ultrafast cannot both be forced on")
	}

	modesChanged := (input.OpenAIFastMode != nil && fast != group.OpenAIFastMode) ||
		ultra != group.OpenAIUltrafastMode || freeUltra != group.FreeOpenAIUltrafast
	if version == OpenAITierPolicyLegacy && modesChanged && !rollback {
		return infraerrors.BadRequest("OPENAI_TIER_POLICY_CONFIRMATION_REQUIRED", "set openai_tier_policy_version to 2 to activate the explicit tier policy")
	}
	if !creating && (modesChanged || version != group.OpenAITierPolicyVersion) && input.Revision == nil {
		return ErrConfigurationRevision
	}

	group.OpenAITierPolicyVersion = version
	group.OpenAIFastMode = fast
	group.OpenAIUltrafastMode = ultra
	group.ForceOpenAIFast = fast == OpenAITierModeForceOn
	group.FreeOpenAIUltrafast = freeUltra
	if input.FreeOpenAIFast != nil {
		group.FreeOpenAIFast = *input.FreeOpenAIFast
	}
	return nil
}
