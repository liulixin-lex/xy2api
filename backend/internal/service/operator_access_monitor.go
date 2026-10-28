package service

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
)

func operatorNewKey(field string) error {
	return infraerrors.BadRequest("ACCOUNT_API_KEY_REQUIRED", "A new key is required for this change").WithMetadata(map[string]string{"field": field})
}

func (s *OperatorAccessService) operatorMonitorRules(ctx context.Context, sc *operatoraccess.Scope, rule operatoraccess.FieldRule, r operatoraccess.Request, obj operatorObject) ([]byte, error) {
	if rule == "monitor_create" {
		if err := s.operatorMonitorTemplate(ctx, obj, nil, false); err != nil {
			return nil, err
		}
		id, err := operatorRead[*int64](obj, "account_id")
		if err != nil {
			return nil, err
		}
		if id != nil && *id > 0 && !sc.Can(operatoraccess.ModuleAccounts, operatoraccess.LevelView) {
			return nil, infraerrors.Forbidden("ADMIN_PERMISSION_DENIED", "Permission required").WithMetadata(map[string]string{"required": "accounts:view"})
		}
		headers, err := operatorRead[map[string]string](obj, "extra_headers")
		if err != nil {
			return nil, err
		}
		for _, value := range headers {
			if value == "******" {
				return nil, operatorFieldError("extra_headers")
			}
		}
		endpoint, err := operatorRead[string](obj, "endpoint")
		if err != nil {
			return nil, err
		}
		if endpoint != "" {
			return nil, operatorAddressField(ctx, "endpoint", endpoint)
		}
		return nil, nil
	}
	id, err := operatorPathID(r, "id")
	if err != nil {
		return nil, err
	}
	if rule == "monitor_template_update" || rule == "monitor_template_apply" {
		if s.templateService == nil || s.templateService.repo == nil {
			return nil, operatorUnavailable()
		}
		stored, err := s.templateService.Get(ctx, id)
		if err != nil {
			return nil, operatorLoadError(err, ErrChannelMonitorTemplateNotFound)
		}
		if stored == nil {
			return nil, ErrChannelMonitorTemplateNotFound
		}
		if rule == "monitor_template_apply" {
			if len(stored.ExtraHeaders) > 0 {
				return nil, operatorSuperOnly()
			}
			return nil, nil
		}
		headers, err := operatorRead[*map[string]string](obj, "extra_headers")
		if err != nil {
			return nil, err
		}
		if headers == nil {
			return nil, nil
		}
		operatorRestoreHeaders(*headers, stored.ExtraHeaders, false)
		operatorSet(obj, "extra_headers", headers)
		return json.Marshal(obj)
	}
	if s.monitorService == nil || s.monitorService.repo == nil {
		return nil, operatorUnavailable()
	}
	stored, err := s.monitorService.Get(ctx, id)
	if err != nil {
		return nil, operatorLoadError(err, ErrChannelMonitorNotFound)
	}
	if stored == nil {
		return nil, ErrChannelMonitorNotFound
	}
	accountID, err := operatorRead[*int64](obj, "account_id")
	if err != nil {
		return nil, err
	}
	if accountID != nil && *accountID > 0 && !operatorInt64PtrEqual(accountID, stored.AccountID) && !sc.Can(operatoraccess.ModuleAccounts, operatoraccess.LevelView) {
		return nil, infraerrors.Forbidden("ADMIN_PERMISSION_DENIED", "Permission required").WithMetadata(map[string]string{"required": "accounts:view"})
	}
	if err := s.operatorMonitorTemplate(ctx, obj, stored.TemplateID, true); err != nil {
		return nil, err
	}
	endpoint, err := operatorRead[*string](obj, "endpoint")
	if err != nil {
		return nil, err
	}
	// Without the address flag the edit view omits the redacted endpoint.
	// With the flag an explicit empty endpoint retains its handler meaning
	// (clearing it in quota mode), and therefore counts as a network change.
	if endpoint != nil {
		*endpoint = normalizeEndpoint(*endpoint)
		operatorSet(obj, "endpoint", *endpoint)
	}
	changed := endpoint != nil && *endpoint != stored.Endpoint
	if !sc.Has(operatoraccess.FlagAccountsUpstreamAddress) && (endpoint == nil || *endpoint == "") {
		operatorSet(obj, "endpoint", stored.Endpoint)
		changed = false
	} else if endpoint != nil {
		if changed && !sc.Has(operatoraccess.FlagAccountsUpstreamAddress) {
			return nil, operatorFieldError("endpoint")
		}
		if *endpoint != "" {
			if err = operatorAddressField(ctx, "endpoint", *endpoint); err != nil {
				return nil, err
			}
		}
	}
	for _, pair := range []struct{ key, stored string }{{"provider", stored.Provider}, {"api_mode", stored.APIMode}} {
		value, e := operatorRead[*string](obj, pair.key)
		if e != nil {
			return nil, e
		}
		if value != nil && pair.key == "api_mode" {
			*value = defaultAPIMode(*value)
		}
		if value != nil && *value != pair.stored {
			changed = true
		}
	}
	key, err := operatorRead[*string](obj, "api_key")
	if err != nil {
		return nil, err
	}
	if changed && (key == nil || strings.TrimSpace(*key) == "") {
		return nil, operatorNewKey("api_key")
	}
	headers, err := operatorRead[*map[string]string](obj, "extra_headers")
	if err != nil {
		return nil, err
	}
	if changed {
		clean := map[string]string{}
		if headers != nil {
			for k, v := range *headers {
				if v != "******" {
					clean[k] = v
				}
			}
		}
		operatorSet(obj, "extra_headers", clean)
	} else if headers != nil {
		operatorRestoreHeaders(*headers, stored.ExtraHeaders, false)
		operatorSet(obj, "extra_headers", headers)
	}
	return json.Marshal(obj)
}
func operatorRestoreHeaders(headers, stored map[string]string, drop bool) {
	for k, value := range headers {
		if value != "******" {
			continue
		}
		if old, ok := stored[k]; ok && !drop {
			headers[k] = old
		} else {
			delete(headers, k)
		}
	}
}

func (s *OperatorAccessService) operatorChannelRules(ctx context.Context, sc *operatoraccess.Scope, rule operatoraccess.FieldRule, r operatoraccess.Request, obj operatorObject) error {
	if sc.Has(operatoraccess.FlagUsersBalanceAdjust) {
		return nil
	}
	if rule == "channel_create" {
		groups, err := operatorRead[[]int64](obj, "group_ids")
		if err != nil {
			return err
		}
		if len(groups) > 0 {
			return operatorFieldError("group_ids")
		}
		return nil
	}
	id, err := operatorPathID(r, "id")
	if err != nil {
		return err
	}
	if s.channelService == nil || s.channelService.repo == nil {
		return operatorUnavailable()
	}
	stored, err := s.channelService.GetByID(ctx, id)
	if err != nil {
		return operatorLoadError(err, ErrChannelNotFound)
	}
	if stored == nil {
		return ErrChannelNotFound
	}
	groups, err := operatorRead[*[]int64](obj, "group_ids")
	if err != nil {
		return err
	}
	if groups != nil && !operatorSameIDSet(*groups, stored.GroupIDs) {
		return operatorFieldError("group_ids")
	}
	if len(stored.GroupIDs) == 0 {
		return nil
	}
	for _, pair := range []struct{ key, stored string }{{"status", stored.Status}, {"billing_model_source", stored.BillingModelSource}} {
		value, e := operatorRead[string](obj, pair.key)
		if e != nil {
			return e
		}
		if value != "" && value != pair.stored {
			return operatorFieldError(pair.key)
		}
	}
	mapping, err := operatorRead[map[string]map[string]string](obj, "model_mapping")
	if err != nil {
		return err
	}
	if mapping != nil && !reflect.DeepEqual(mapping, stored.ModelMapping) && (len(mapping) != 0 || len(stored.ModelMapping) != 0) {
		return operatorFieldError("model_mapping")
	}
	pricing, err := operatorRead[*[]operatorChannelModelPricingRequest](obj, "model_pricing")
	if err != nil {
		return err
	}
	if pricing != nil {
		raw, e := json.Marshal(stored.ModelPricing)
		if e != nil {
			return operatorUnavailable()
		}
		var old []operatorChannelModelPricingRequest
		if json.Unmarshal(raw, &old) != nil {
			return operatorUnavailable()
		}
		// Normalize the handler's default token mode before comparing, and ignore DB IDs.
		for i := range *pricing {
			if (*pricing)[i].Platform == "" {
				(*pricing)[i].Platform = PlatformAnthropic
			}
			if (*pricing)[i].BillingMode == "" {
				(*pricing)[i].BillingMode = "token"
			}
		}
		for i := range old {
			if old[i].Platform == "" {
				old[i].Platform = PlatformAnthropic
			}
			if old[i].BillingMode == "" {
				old[i].BillingMode = "token"
			}
		}
		if (len(*pricing) != 0 || len(old) != 0) && !reflect.DeepEqual(*pricing, old) {
			return operatorFieldError("model_pricing")
		}
	}
	return nil
}
func operatorSameIDSet(a, b []int64) bool {
	m := map[int64]bool{}
	for _, id := range a {
		m[id] = true
	}
	n := map[int64]bool{}
	for _, id := range b {
		n[id] = true
	}
	return reflect.DeepEqual(m, n)
}

func (s *OperatorAccessService) operatorRiskRules(ctx context.Context, obj operatorObject) error {
	// All credential mutators are refused before any settings read.
	if err := operatorRiskKeyFields(obj); err != nil {
		return err
	}
	if s.moderationService == nil {
		return operatorUnavailable()
	}
	stored, err := s.moderationService.GetConfig(ctx)
	if err != nil || stored == nil {
		return operatorUnavailable()
	}
	if err = operatorRiskIdentity(ctx, obj, stored, ""); err != nil {
		return err
	}
	if operatorHas(obj, "engine_configs") {
		configs, e := operatorRead[map[string]UpdateContentModerationEngineInput](obj, "engine_configs")
		if e != nil {
			return e
		}
		for engine, input := range configs {
			raw, _ := json.Marshal(input)
			child, e := operatorObjectBody(raw)
			if e != nil {
				return e
			}
			if e = operatorRiskKeyFields(child); e != nil {
				return operatorFieldError("engine_configs")
			}
			old := stored.EngineConfigs[engine]
			if old == nil {
				return operatorFieldError("engine_configs")
			}
			if e = operatorRiskIdentity(ctx, child, old, "engine_configs"); e != nil {
				return e
			}
			// engine_configs itself is protected, including timeout/threshold changes.
			if input.TimeoutMS != nil && *input.TimeoutMS != old.TimeoutMS {
				return operatorFieldError("engine_configs")
			}
			if input.RetryCount != nil && *input.RetryCount != old.RetryCount {
				return operatorFieldError("engine_configs")
			}
			if input.Thresholds != nil && !reflect.DeepEqual(*input.Thresholds, old.Thresholds) {
				return operatorFieldError("engine_configs")
			}
		}
	}
	return nil
}
func operatorRiskKeyFields(obj operatorObject) error {
	key, err := operatorRead[*string](obj, "api_key")
	if err != nil {
		return err
	}
	if key != nil && *key != "" {
		return operatorFieldError("api_key")
	}
	mode, err := operatorRead[string](obj, "api_keys_mode")
	if err != nil {
		return err
	}
	if mode != "" {
		return operatorFieldError("api_keys_mode")
	}
	clear, err := operatorRead[bool](obj, "clear_api_key")
	if err != nil {
		return err
	}
	if clear {
		return operatorFieldError("clear_api_key")
	}
	for _, field := range []string{"api_keys", "delete_api_key_hashes"} {
		values, e := operatorRead[*[]string](obj, field)
		if e != nil {
			return e
		}
		if values != nil && len(*values) > 0 {
			return operatorFieldError(field)
		}
	}
	return nil
}
func operatorRiskIdentity(ctx context.Context, obj operatorObject, stored *ContentModerationConfigView, prefix string) error {
	fail := func(key string) error {
		if prefix != "" {
			key = prefix
		}
		return operatorFieldError(key)
	}
	for _, pair := range []struct{ key, stored string }{{"base_url", stored.BaseURL}, {"model", stored.Model}} {
		value, err := operatorRead[*string](obj, pair.key)
		if err != nil {
			return err
		}
		if value != nil {
			*value = strings.TrimSpace(*value)
			if pair.key == "base_url" {
				*value = strings.TrimRight(*value, "/")
			}
			if pair.key == "base_url" && *value != "" {
				if err = operatorAddressField(ctx, pair.key, *value); err != nil {
					return err
				}
			}
			if *value != pair.stored {
				return fail(pair.key)
			}
		}
	}
	proxy, err := operatorRead[*int64](obj, "proxy_id")
	if err != nil {
		return err
	}
	if proxy != nil {
		var actual int64
		if stored.ProxyID != nil {
			actual = *stored.ProxyID
		}
		if *proxy != actual {
			return fail("proxy_id")
		}
	}
	return nil
}

// A staged template is a future outbound header source, even before apply.
func (s *OperatorAccessService) operatorMonitorTemplate(ctx context.Context, obj operatorObject, storedID *int64, allowClear bool) error {
	clear, err := operatorRead[bool](obj, "clear_template")
	if err != nil {
		return err
	}
	id, err := operatorRead[*int64](obj, "template_id")
	if err != nil {
		return err
	}
	if (allowClear && clear) || id == nil || operatorInt64PtrEqual(id, storedID) {
		return nil
	}
	if s.templateService == nil || s.templateService.repo == nil {
		return operatorUnavailable()
	}
	template, err := s.templateService.Get(ctx, *id)
	if err != nil {
		return operatorLoadError(err, ErrChannelMonitorTemplateNotFound)
	}
	if template == nil {
		return ErrChannelMonitorTemplateNotFound
	}
	if len(template.ExtraHeaders) > 0 {
		return operatorSuperOnly()
	}
	return nil
}
