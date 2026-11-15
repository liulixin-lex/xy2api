package service

import (
	"context"
	"errors"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/config"
)

// DiagnoseModelAvailabilityForPlatform reports whether the requested model
// is configured to be served by any persistently eligible OpenAI-compatible
// account in the group for the given platform (e.g. PlatformOpenAI,
// PlatformGrok). The platform scopes the candidate pool so distinct
// OpenAI-compatible platforms do not cross-contaminate diagnosis results.
// The query bypasses scheduler snapshots and ignores transient runtime state.
//
// A failed lookup is unknown and carries its cause; it cannot prove absence.
func (s *OpenAIGatewayService) DiagnoseModelAvailabilityForPlatform(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	platform string,
) ModelAvailabilityDiagnosis {
	diag, _ := s.openAIModelAvailability(ctx, groupID, requestedModel, platform)
	return diag
}

func (s *OpenAIGatewayService) openAIModelAvailability(ctx context.Context, groupID *int64, requestedModel, platform string) (ModelAvailabilityDiagnosis, []Account) {
	if s == nil || s.accountRepo == nil {
		return ModelAvailabilityDiagnosis{Err: errors.New("account availability repository unavailable")}, nil
	}
	requestedModel = strings.TrimSpace(requestedModel)

	platform = NormalizeOpenAICompatiblePlatform(platform)
	queryGroupID := groupID
	includeGrouped := false
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		queryGroupID = nil
		includeGrouped = true
	}
	accounts, err := s.accountRepo.ListModelAvailabilityCandidates(
		ctx,
		queryGroupID,
		[]string{platform},
		includeGrouped,
	)
	if err != nil {
		return ModelAvailabilityDiagnosis{Err: err}, nil
	}
	diag := ModelAvailabilityDiagnosis{Known: true}
	req, _ := ctx.Value(openAISelectionRequestKey{}).(OpenAIAccountScheduleRequest)
	for i := range accounts {
		account := &accounts[i]
		diag.Members++
		if source, ok := CompositeRouteSourceFromContext(ctx); ok && source == CompositeRouteSourceAccount {
			if publicModel, known := RequestedPublicModelFromContext(ctx); known && !explicitModelMappingClaims(*account, publicModel) {
				continue
			}
		}
		if requestedModel != "" && !account.IsModelSupported(requestedModel) {
			continue
		}
		diag.ModelSupported++
		if s.openAIConfiguredRequestFailureReason(ctx, account, req) == "" {
			diag.RequestSupported++
		}
	}
	diag.HasAccountsInPool = diag.Members > 0
	diag.HasModelSupport = diag.ModelSupported > 0
	return diag, accounts
}
