package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"reflect"
	"slices"
)

var OperatorGroupPriceFields = []string{
	"Platform",
	"ForceOpenAIFast",
	"RateMultiplier",
	"SubscriptionType",
	"DailyLimitUSD",
	"WeeklyLimitUSD",
	"MonthlyLimitUSD",
	"LongContextPricingEnabled",
	"LongContextPricingScope",
	"LongContextPricingModels",
	"ModelPricing",
	"ImageRateIndependent",
	"ImageRateMultiplier",
	"BatchImageDiscountMultiplier",
	"BatchImageHoldMultiplier",
	"VideoRateIndependent",
	"VideoRateMultiplier",
	"PeakRateEnabled",
	"PeakStart",
	"PeakEnd",
	"PeakRateMultiplier",
	"ProfitControlEnabled",
	"ProfitMinMargin",
	"ProfitSafetyBuffer",
	"ImagePrice1K",
	"ImagePrice2K",
	"ImagePrice4K",
	"VideoPrice480P",
	"VideoPrice720P",
	"VideoPrice1080P",
	"VideoModelPrices",
	"WebSearchPricePerCall",
	"SearchPricePer1k",
	"AudioRealtimePricePerMin",
	"AudioTTSPricePerMillionChars",
	"AudioSTTPricePerHour",
	"FreeOpenAIFast",
}
var OperatorGroupFreeFields = []string{
	"Name",
	"Description",
	"IsExclusive",
	"ShowExclusiveBadge",
	"SystemPromptConfig",
	"Status",
	"AllowImageGeneration",
	"AllowBatchImageGeneration",
	"ClaudeCodeOnly",
	"FallbackGroupID",
	"FallbackGroupIDOnInvalidRequest",
	"AvailabilityFallbackGroupID",
	"ModelRouting",
	"ModelRoutingEnabled",
	"MCPXMLInject",
	"SupportedModelScopes",
	"AllowMessagesDispatch",
	"AllowLive",
	"DefaultMappedModel",
	"RequireOAuthOnly",
	"RequirePrivacySet",
	"MessagesDispatchModelConfig",
	"ModelAllowlist",
	"CodexModelsManifestConfig",
	"RPMLimit",
	"MaxReasoningEffort",
	"MaxReasoningEffortOverLimit",
	"ReasoningEffortMappings",
	"CopyAccountsFromGroupIDs",
}
var operatorGroupJSONNames = map[string]string{
	"AudioTTSPricePerMillionChars":    "audio_tts_price_per_million_chars",
	"AudioSTTPricePerHour":            "audio_stt_price_per_hour",
	"Name":                            "name",
	"Description":                     "description",
	"Platform":                        "platform",
	"RateMultiplier":                  "rate_multiplier",
	"IsExclusive":                     "is_exclusive",
	"ShowExclusiveBadge":              "show_exclusive_badge",
	"SystemPromptConfig":              "system_prompt_config",
	"Status":                          "status",
	"SubscriptionType":                "subscription_type",
	"DailyLimitUSD":                   "daily_limit_usd",
	"WeeklyLimitUSD":                  "weekly_limit_usd",
	"MonthlyLimitUSD":                 "monthly_limit_usd",
	"LongContextPricingEnabled":       "long_context_pricing_enabled",
	"LongContextPricingScope":         "long_context_pricing_scope",
	"LongContextPricingModels":        "long_context_pricing_models",
	"ModelPricing":                    "model_pricing",
	"AllowImageGeneration":            "allow_image_generation",
	"AllowBatchImageGeneration":       "allow_batch_image_generation",
	"ImageRateIndependent":            "image_rate_independent",
	"ImageRateMultiplier":             "image_rate_multiplier",
	"BatchImageDiscountMultiplier":    "batch_image_discount_multiplier",
	"BatchImageHoldMultiplier":        "batch_image_hold_multiplier",
	"VideoRateIndependent":            "video_rate_independent",
	"VideoRateMultiplier":             "video_rate_multiplier",
	"PeakRateEnabled":                 "peak_rate_enabled",
	"PeakStart":                       "peak_start",
	"PeakEnd":                         "peak_end",
	"PeakRateMultiplier":              "peak_rate_multiplier",
	"ProfitControlEnabled":            "profit_control_enabled",
	"ProfitMinMargin":                 "profit_min_margin",
	"ProfitSafetyBuffer":              "profit_safety_buffer",
	"ImagePrice1K":                    "image_price_1k",
	"ImagePrice2K":                    "image_price_2k",
	"ImagePrice4K":                    "image_price_4k",
	"VideoPrice480P":                  "video_price_480p",
	"VideoPrice720P":                  "video_price_720p",
	"VideoPrice1080P":                 "video_price_1080p",
	"VideoModelPrices":                "video_model_prices",
	"WebSearchPricePerCall":           "web_search_price_per_call",
	"SearchPricePer1k":                "search_price_per_1k",
	"AudioRealtimePricePerMin":        "audio_realtime_price_per_min",
	"ClaudeCodeOnly":                  "claude_code_only",
	"FallbackGroupID":                 "fallback_group_id",
	"FallbackGroupIDOnInvalidRequest": "fallback_group_id_on_invalid_request",
	"AvailabilityFallbackGroupID":     "availability_fallback_group_id",
	"ModelRouting":                    "model_routing",
	"ModelRoutingEnabled":             "model_routing_enabled",
	"MCPXMLInject":                    "mcp_xml_inject",
	"SupportedModelScopes":            "supported_model_scopes",
	"AllowMessagesDispatch":           "allow_messages_dispatch",
	"AllowLive":                       "allow_live",
	"ForceOpenAIFast":                 "force_openai_fast",
	"FreeOpenAIFast":                  "free_openai_fast",
	"RequireOAuthOnly":                "require_oauth_only",
	"RequirePrivacySet":               "require_privacy_set",
	"DefaultMappedModel":              "default_mapped_model",
	"MessagesDispatchModelConfig":     "messages_dispatch_model_config",
	"ModelAllowlist":                  "model_allowlist",
	"CodexModelsManifestConfig":       "codex_models_manifest_config",
	"MaxReasoningEffort":              "max_reasoning_effort",
	"MaxReasoningEffortOverLimit":     "max_reasoning_effort_over_limit",
	"ReasoningEffortMappings":         "reasoning_effort_mappings",
	"CopyAccountsFromGroupIDs":        "copy_accounts_from_group_ids",
}

func (s *adminServiceImpl) operatorGroupCreateRules(ctx context.Context, input *CreateGroupInput) error {
	sc, _, err := s.operatorScope(ctx)
	if err != nil {
		return err
	}
	if sc == nil || sc.Has(operatoraccess.FlagUsersBalanceAdjust) {
		return nil
	}
	if !input.IsExclusive {
		return operatorFieldError("is_exclusive")
	}
	for _, f := range []struct {
		name string
		id   *int64
	}{{"fallback_group_id", input.FallbackGroupID}, {"fallback_group_id_on_invalid_request", input.FallbackGroupIDOnInvalidRequest}, {"availability_fallback_group_id", input.AvailabilityFallbackGroupID}} {
		if f.id != nil && *f.id > 0 {
			return operatorFieldError(f.name)
		}
	}
	if len(input.CopyAccountsFromGroupIDs) > 0 {
		return operatorFieldError("copy_accounts_from_group_ids")
	}
	return nil
}
func (s *adminServiceImpl) operatorGroupUpdateRules(ctx context.Context, id int64, input *UpdateGroupInput) error {
	sc, _, err := s.operatorScope(ctx)
	if err != nil {
		return err
	}
	if sc == nil || sc.Has(operatoraccess.FlagUsersBalanceAdjust) {
		return nil
	}
	if s.groupRepo == nil {
		return operatorUnavailable()
	}
	group, err := s.groupRepo.GetByID(ctx, id)
	if err != nil {
		return operatorLoadError(err, ErrGroupNotFound)
	}
	if group == nil {
		return ErrGroupNotFound
	}
	incoming, stored := reflect.ValueOf(input).Elem(), reflect.ValueOf(group).Elem()
	for _, name := range OperatorGroupPriceFields {
		value := incoming.FieldByName(name)
		if !operatorGroupFieldSet(value) {
			continue
		}
		old := stored.FieldByName(name)
		if slices.Contains([]string{"DailyLimitUSD", "WeeklyLimitUSD", "MonthlyLimitUSD"}, name) {
			incomingLimit, incomingOK := value.Interface().(*float64)
			storedLimit, storedOK := old.Interface().(*float64)
			if !incomingOK || !storedOK {
				return operatorUnavailable()
			}
			if !reflect.DeepEqual(normalizeLimit(incomingLimit), normalizeLimit(storedLimit)) {
				return operatorFieldError(operatorGroupJSONNames[name])
			}
			continue
		}
		if !reflect.DeepEqual(operatorGroupValue(value), operatorGroupValue(old)) {
			return operatorFieldError(operatorGroupJSONNames[name])
		}
	}
	if input.IsExclusive != nil && group.IsExclusive && !*input.IsExclusive {
		return operatorFieldError("is_exclusive")
	}
	if input.Status == StatusActive && group.Status != StatusActive {
		return operatorFieldError("status")
	}
	for _, f := range []struct {
		name    string
		id, old *int64
	}{{"fallback_group_id", input.FallbackGroupID, group.FallbackGroupID}, {"fallback_group_id_on_invalid_request", input.FallbackGroupIDOnInvalidRequest, group.FallbackGroupIDOnInvalidRequest}, {"availability_fallback_group_id", input.AvailabilityFallbackGroupID, group.AvailabilityFallbackGroupID}} {
		if f.id != nil && *f.id > 0 && !reflect.DeepEqual(f.id, f.old) {
			return operatorFieldError(f.name)
		}
	}
	if len(input.CopyAccountsFromGroupIDs) > 0 {
		return operatorFieldError("copy_accounts_from_group_ids")
	}
	return nil
}
func operatorGroupFieldSet(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		return !v.IsNil()
	case reflect.String:
		return v.String() != ""
	}
	return !v.IsZero()
}
func operatorGroupValue(v reflect.Value) any {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return v.Elem().Interface()
	}
	return v.Interface()
}
func (s *adminServiceImpl) operatorAPIKeyGroupRules(ctx context.Context, keyID int64, groupID *int64) error {
	sc, _, err := s.operatorScope(ctx)
	if err != nil {
		return err
	}
	if sc == nil || sc.Has(operatoraccess.FlagUsersBalanceAdjust) || groupID == nil || *groupID <= 0 {
		return nil
	}
	if s.apiKeyRepo == nil {
		return operatorUnavailable()
	}
	key, err := s.apiKeyRepo.GetByID(ctx, keyID)
	if err != nil {
		return operatorLoadError(err, ErrAPIKeyNotFound)
	}
	if key == nil {
		return ErrAPIKeyNotFound
	}
	if s.userRepo == nil {
		return operatorUnavailable()
	}
	user, err := s.userRepo.GetByIDIncludeDeleted(ctx, key.UserID)
	if err != nil {
		return operatorLoadError(err, ErrUserNotFound)
	}
	if user == nil {
		return ErrUserNotFound
	}
	return s.operatorAllowedGroups(ctx, []int64{*groupID}, user.AllowedGroups, "group_id")
}
func (s *adminServiceImpl) operatorReplaceGroupRules(ctx context.Context, userID, newGroupID int64) error {
	sc, _, err := s.operatorScope(ctx)
	if err != nil {
		return err
	}
	if sc == nil || sc.Has(operatoraccess.FlagUsersBalanceAdjust) {
		return nil
	}
	if s.userRepo == nil {
		return operatorUnavailable()
	}
	user, err := s.userRepo.GetByIDIncludeDeleted(ctx, userID)
	if err != nil {
		return operatorLoadError(err, ErrUserNotFound)
	}
	if user == nil {
		return ErrUserNotFound
	}
	if slices.Contains(user.AllowedGroups, newGroupID) {
		return nil
	}
	if s.groupRepo == nil {
		return operatorUnavailable()
	}
	group, err := s.groupRepo.GetByID(ctx, newGroupID)
	if err != nil {
		return operatorLoadError(err, ErrGroupNotFound)
	}
	if group == nil {
		return ErrGroupNotFound
	}
	if group.IsExclusive {
		return operatorFieldError("new_group_id")
	}
	return nil
}
func (s *adminServiceImpl) operatorDeleteGroup(ctx context.Context, id int64) (bool, error) {
	sc, _, err := s.operatorScope(ctx)
	if err != nil {
		return true, err
	}
	if sc != nil && !sc.Has(operatoraccess.FlagUsersBalanceAdjust) {
		return true, s.deleteGroup(ctx, id, true)
	}
	return false, nil
}
