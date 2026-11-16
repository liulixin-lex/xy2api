package service

import (
	"context"
	"errors"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/config"
)

// ModelAvailabilityDiagnosis describes whether the requested model can be
// served by any persistently eligible account in the group (active with its
// schedulable setting enabled), ignoring transient state such as rate limits,
// overload, temporary unschedulability, and runtime blocks. Handlers use this
// on the "no available accounts" error path to distinguish 404
// model_not_found from 503 service_unavailable.
type ModelAvailabilityDiagnosis struct {
	Known                                     bool
	Err                                       error
	Members, ModelSupported, RequestSupported int
	// HasAccountsInPool is true if the group has at least one persistently
	// eligible account on the queried platform (or, for Anthropic/Gemini, on
	// the platform plus mixed-scheduled Antigravity accounts).
	HasAccountsInPool bool
	// HasModelSupport intersects account mappings and compatibility profiles,
	// independently of request-specific capabilities and transient eligibility.
	HasModelSupport bool
}

// ModelAvailabilityDiagnoser is implemented by gateway services that can
// report whether the requested model is configured to be served by any
// account. Both *GatewayService and *OpenAIGatewayService implement this so
// handlers in either package can share a single classifier.
type ModelAvailabilityDiagnoser interface {
	DiagnoseModelAvailabilityForPlatform(
		ctx context.Context,
		groupID *int64,
		requestedModel string,
		platform string,
	) ModelAvailabilityDiagnosis
}

// DiagnoseModelAvailabilityForPlatform inspects accounts enabled for scheduling
// by persistent configuration and returns whether the requested model is
// configured to be served by any of them. The dedicated repository query
// bypasses scheduler snapshots and deliberately ignores transient rate-limit,
// overload, temporary-unschedulable, expiry, quota, and runtime-block state.
//
// Incomplete inputs or dependency failures remain unknown. Only a complete
// persistent configuration scan can establish model availability.
func (s *GatewayService) DiagnoseModelAvailabilityForPlatform(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	platform string,
) ModelAvailabilityDiagnosis {
	if s == nil {
		return ModelAvailabilityDiagnosis{}
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		// No model specified: availability is unknown.
		return ModelAvailabilityDiagnosis{}
	}
	if strings.TrimSpace(platform) == "" {
		// Without a platform we cannot scope the lookup; bail out to the
		// 503 branch rather than make an unscoped scan.
		return ModelAvailabilityDiagnosis{}
	}

	if s.accountRepo == nil {
		return ModelAvailabilityDiagnosis{Err: errors.New("account repository unavailable")}
	}

	useMixed := platform == PlatformAnthropic || platform == PlatformGemini
	platforms := []string{platform}
	if useMixed {
		platforms = append(platforms, PlatformAntigravity)
	}

	queryGroupID := groupID
	includeGrouped := false
	if useMixed {
		// Preserve the generic scheduler's scope rules: an explicit group wins
		// for mixed scheduling, while group-less simple mode scans all accounts.
		if groupID == nil && s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
			includeGrouped = true
		}
	} else if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		queryGroupID = nil
		includeGrouped = true
	}

	accounts, err := s.accountRepo.ListModelAvailabilityCandidates(ctx, queryGroupID, platforms, includeGrouped)
	if err != nil {
		return ModelAvailabilityDiagnosis{Err: err}
	}

	diag := ModelAvailabilityDiagnosis{Known: true}
	for i := range accounts {
		if useMixed && accounts[i].Platform == PlatformAntigravity && !accounts[i].IsMixedSchedulingEnabled() {
			continue
		}
		diag.Members++
		account := &accounts[i]
		profile, profileErr := configuredAnthropicCompatProfile(account)
		supported := false
		if profileErr == nil && profile != nil {
			model := anthropicCompatDispatchModel(ctx, requestedModel)
			supported = anthropicCompatModelSupported(account, model)
		} else if profileErr == nil {
			supported = s.isModelSupportedByAccountWithContext(ctx, account, requestedModel)
		} else {
			diag.Err = errors.Join(diag.Err, profileErr)
			diag.Known = false
			continue
		}
		if supported {
			diag.ModelSupported++
		}
	}
	// Generic routing has no separate OpenAI Chat capability gate. Its static
	// request support is exactly the model/compatibility scope counted above.
	diag.RequestSupported = diag.ModelSupported
	diag.HasAccountsInPool = diag.Members > 0
	diag.HasModelSupport = diag.ModelSupported > 0
	return diag
}
