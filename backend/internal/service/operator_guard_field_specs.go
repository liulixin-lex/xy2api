package service

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// OperatorGuardFieldSpec describes a protected JSON value read by a middleware
// guard. RequestType names the actual binder struct (or a method-local req).
// Container traversal through RawMessage is not a separate protected value.
type OperatorGuardFieldSpec struct {
	Rule        string
	KeyPath     string
	RequestType string
	DecodeType  reflect.Type
}

func operatorFieldSpec[T any](rule, request, path string) OperatorGuardFieldSpec {
	return OperatorGuardFieldSpec{Rule: rule, KeyPath: path, RequestType: request, DecodeType: reflect.TypeFor[T]()}
}

// operatorNullableInt64Field is the wire twin of dto.NullableInt64Field. Service
// cannot import dto (dto imports service). The handler contract test checks its
// structure AND custom JSON decoder, including absent, null and invalid values.
type operatorNullableInt64Field struct {
	Set   bool
	Value *int64
}

func (f *operatorNullableInt64Field) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(data, []byte("null")) {
		f.Value = nil
		return nil
	}
	var value int64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	f.Value = &value
	return nil
}

// OperatorGuardFieldSpecs pins the binder paths and decode types, including
// target-rule body IDs. Handler-private pricing and DTO nullable types have wire
// twins because importing handler into service would introduce a package cycle.
func OperatorGuardFieldSpecs() []OperatorGuardFieldSpec {
	specs := []OperatorGuardFieldSpec{
		operatorFieldSpec[int64]("user_body_id", "CreateAndRedeemCodeRequest", "user_id"),
		operatorFieldSpec[int64]("user_body_id", "AssignSubscriptionRequest", "user_id"),
		operatorFieldSpec[[]int64]("user_body_ids", "BatchSetRateRequest", "user_ids"),
		operatorFieldSpec[[]int64]("user_body_ids", "BatchGetUserAttributesRequest", "user_ids"),
		operatorFieldSpec[[]int64]("user_body_ids", "BulkAssignSubscriptionRequest", "user_ids"),
		operatorFieldSpec[[]int64]("user_body_ids", "BatchUpdateConcurrencyRequest", "user_ids"),
		operatorFieldSpec[[]int64]("user_body_ids", "BatchUpdateLimitsRequest", "user_ids"),
		operatorFieldSpec[[]int64]("user_body_ids", "BatchUpdateStatusRequest", "user_ids"),
		operatorFieldSpec[[]int64]("sub_body_owners", "BulkSubscriptionActionInput", "subscription_ids"),
		operatorFieldSpec[int64]("group_rate_rows", "BatchSetGroupRateMultipliersRequest", "entries[].user_id"),
		operatorFieldSpec[int64]("group_rpm_rows", "BatchSetGroupRPMOverridesRequest", "entries[].user_id"),
		operatorFieldSpec[bool]("user_batch_no_all", "BatchUpdateConcurrencyRequest", "all"),
		operatorFieldSpec[bool]("user_batch_no_all", "BatchUpdateLimitsRequest", "all"),
		operatorFieldSpec[[]operatorAccountStatsPricingRuleRequest]("channel_create", "createChannelRequest", "account_stats_pricing_rules"),
		operatorFieldSpec[bool]("channel_create", "createChannelRequest", "apply_pricing_to_account_stats"),
		operatorFieldSpec[*[]operatorAccountStatsPricingRuleRequest]("channel_update", "updateChannelRequest", "account_stats_pricing_rules"),
		operatorFieldSpec[*bool]("channel_update", "updateChannelRequest", "apply_pricing_to_account_stats"),
		operatorFieldSpec[*string]("risk_config", "contentModerationConfigRequest", "engine"),
		operatorFieldSpec[[]int64]("channel_create", "createChannelRequest", "group_ids"),
		operatorFieldSpec[*[]int64]("channel_update", "updateChannelRequest", "group_ids"),
		operatorFieldSpec[*[]operatorChannelModelPricingRequest]("channel_update", "updateChannelRequest", "model_pricing"),
		operatorFieldSpec[map[string]map[string]string]("channel_update", "updateChannelRequest", "model_mapping"),
		operatorFieldSpec[string]("channel_update", "updateChannelRequest", "status"),
		operatorFieldSpec[string]("channel_update", "updateChannelRequest", "billing_model_source"),
		operatorFieldSpec[*int64]("monitor_create", "channelMonitorCreateRequest", "account_id"),
		operatorFieldSpec[*int64]("monitor_create", "channelMonitorCreateRequest", "template_id"),
		operatorFieldSpec[*int64]("monitor_update", "channelMonitorUpdateRequest", "account_id"),
		operatorFieldSpec[*int64]("monitor_update", "channelMonitorUpdateRequest", "template_id"),
		operatorFieldSpec[bool]("monitor_update", "channelMonitorUpdateRequest", "clear_template"),
		// Body masks are restored before binding; these are the rewritten wire types.
		operatorFieldSpec[map[string]any]("monitor_create", "channelMonitorCreateRequest", "body_override"),
		operatorFieldSpec[*map[string]any]("monitor_update", "channelMonitorUpdateRequest", "body_override"),
		operatorFieldSpec[*map[string]any]("monitor_template_update", "channelMonitorTemplateUpdateRequest", "body_override"),
		operatorFieldSpec[map[string]string]("monitor_create", "channelMonitorCreateRequest", "extra_headers"),
		operatorFieldSpec[string]("monitor_create", "channelMonitorCreateRequest", "endpoint"),
		operatorFieldSpec[*string]("monitor_update", "channelMonitorUpdateRequest", "endpoint"),
		operatorFieldSpec[*string]("monitor_update", "channelMonitorUpdateRequest", "provider"),
		operatorFieldSpec[*string]("monitor_update", "channelMonitorUpdateRequest", "api_mode"),
		operatorFieldSpec[*string]("monitor_update", "channelMonitorUpdateRequest", "api_key"),
		operatorFieldSpec[*map[string]string]("monitor_update", "channelMonitorUpdateRequest", "extra_headers"),
		operatorFieldSpec[*map[string]string]("monitor_template_update", "channelMonitorTemplateUpdateRequest", "extra_headers"),
		operatorFieldSpec[string]("proxy_create", "CreateProxyRequest", "host"),
		operatorFieldSpec[string]("proxy_create", "BatchCreateRequest", "proxies[].host"),
		operatorFieldSpec[int]("proxy_update", "UpdateProxyRequest", "port"),
		operatorFieldSpec[*string]("proxy_update", "UpdateProxyRequest", "username"),
		operatorFieldSpec[*string]("proxy_update", "UpdateProxyRequest", "password"),
		operatorFieldSpec[operatorNullableInt64Field]("proxy_update", "UpdateProxyRequest", "backup_proxy_id"),
		operatorFieldSpec[operatorNullableInt64Field]("proxy_update", "UpdateProxyRequest", "expires_at"),
		operatorFieldSpec[[]int64]("proxy_delete", "ProxyHandler.BatchDelete.BatchDeleteRequest", "ids"),
		operatorFieldSpec[[]int64]("redeem_safe_rows", "RedeemHandler.BatchDelete.req", "ids"),
		operatorFieldSpec[[]int64]("redeem_batch_rows", "BatchUpdateRedeemCodesRequest", "ids"),
		operatorFieldSpec[string]("redeem_create_and_redeem", "CreateAndRedeemCodeRequest", "type"),
		operatorFieldSpec[string]("redeem_create_and_redeem", "CreateAndRedeemCodeRequest", "code"),
		operatorFieldSpec[string]("sub_bulk_action", "BulkSubscriptionActionInput", "action"),
		operatorFieldSpec[*float64]("aff_rate", "UpdateAffiliateUserRequest", "aff_rebate_rate_percent"),
		operatorFieldSpec[bool]("aff_rate", "UpdateAffiliateUserRequest", "clear_rebate_rate"),
		operatorFieldSpec[string]("codex_ticket_proxy", "AccountHandler.UpdateCodexAccountTicket.req", "proxy_url"),
		operatorFieldSpec[map[string]UpdateContentModerationEngineInput]("risk_config", "contentModerationConfigRequest", "engine_configs"),
		operatorFieldSpec[OpsDistributedLockSettings]("ops_runtime_alert", "OpsAlertRuntimeSettings", "distributed_lock"),
		operatorFieldSpec[string]("account_url_probe", "AccountHandler.SyncUpstreamModelsPreview.req", "base_url"),
	}
	for _, path := range []string{"protocol", "host", "fallback_mode", "status"} {
		specs = append(specs, operatorFieldSpec[string]("proxy_update", "UpdateProxyRequest", path))
	}
	for _, prefix := range []string{"", "engine_configs.*."} {
		for _, path := range []string{"api_key", "base_url", "model"} {
			specs = append(specs, operatorFieldSpec[*string]("risk_config", "contentModerationConfigRequest", prefix+path))
		}
		for _, path := range []string{"api_keys", "delete_api_key_hashes"} {
			specs = append(specs, operatorFieldSpec[*[]string]("risk_config", "contentModerationConfigRequest", prefix+path))
		}
		specs = append(specs,
			operatorFieldSpec[string]("risk_config", "contentModerationConfigRequest", prefix+"api_keys_mode"),
			operatorFieldSpec[bool]("risk_config", "contentModerationConfigRequest", prefix+"clear_api_key"),
			operatorFieldSpec[*int64]("risk_config", "contentModerationConfigRequest", prefix+"proxy_id"))
	}
	for _, path := range []string{"timeout_ms", "retry_count"} {
		specs = append(specs, operatorFieldSpec[*int]("risk_config", "contentModerationConfigRequest", "engine_configs.*."+path))
	}
	specs = append(specs, operatorFieldSpec[*map[string]float64]("risk_config", "contentModerationConfigRequest", "engine_configs.*.thresholds"))
	for _, request := range []string{"CodexSessionImportRequest", "OpenAICodexPATCreateRequest", "GrokSSOToOAuthRequest"} {
		path := "credential_extras"
		if request == "GrokSSOToOAuthRequest" {
			path = "credentials"
		}
		specs = append(specs,
			operatorFieldSpec[map[string]any]("account_import_address", request, path),
			operatorFieldSpec[map[string]any]("account_import_address", request, "extra"))
	}
	return specs
}
