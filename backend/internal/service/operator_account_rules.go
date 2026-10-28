package service

import (
	"context"
	"encoding/json"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

var OperatorAddressKeys = []string{"credentials.base_url", "credentials.api_base_urls", "extra.custom_base_url", "extra.custom_base_url_enabled"}
var OperatorNetworkIdentityKeys = []string{"credentials.base_url", "credentials.api_base_urls", "extra.custom_base_url", "extra.custom_base_url_enabled", "proxy_id", "type", "platform", "credentials.api_protocol", "credentials.account_mode", "credentials.aws_region"}
var OperatorRefreshableTypes = []string{"oauth", "setup-token", "service_account"}
var OperatorAccountBillingExtraKeys = []string{"cache_ttl_override_enabled", "cache_ttl_override_target", "openai_long_context_billing_enabled"}
var operatorAWSRegion = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)

func operatorEmpty(v any) bool { return v == nil || v == "" }
func operatorMapKey(path string, credentials, extra map[string]any) (map[string]any, string) {
	if key, ok := strings.CutPrefix(path, "credentials."); ok {
		return credentials, key
	}
	if key, ok := strings.CutPrefix(path, "extra."); ok {
		return extra, key
	}
	return nil, ""
}
func operatorRegionCheck(credentials map[string]any) error {
	if v := credentials["aws_region"]; !operatorEmpty(v) {
		region, ok := v.(string)
		if !ok || !operatorAWSRegion.MatchString(region) {
			return operatorFieldError("credentials.aws_region").WithMetadata(map[string]string{"field": "credentials.aws_region", "cause": "invalid_region"})
		}
	}
	return nil
}
func operatorAccountAddressValues(ctx context.Context, field string, value any) error {
	if operatorEmpty(value) || value == false {
		return nil
	}
	if strings.HasSuffix(field, "_enabled") {
		return nil
	}
	switch v := value.(type) {
	case string:
		return operatorAddressField(ctx, field, v)
	case []string:
		for _, s := range v {
			if err := operatorAddressField(ctx, field, s); err != nil {
				return err
			}
		}
	case []any:
		for _, v := range v {
			if err := operatorAccountAddressValues(ctx, field, v); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, v := range v {
			if err := operatorAccountAddressValues(ctx, field, v); err != nil {
				return err
			}
		}
	case map[string]string:
		for _, v := range v {
			if err := operatorAccountAddressValues(ctx, field, v); err != nil {
				return err
			}
		}
	default:
		return operatorFieldError(field)
	}
	return nil
}

// Restore the exact response projection before comparing network identity. A
// partial edit containing a mask is ambiguous and must never reach storage.
func operatorRestoreURLUserinfo(value, stored any, field string) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, operatorFieldError(field)
	}
	if !strings.Contains(string(raw), "***@") {
		return value, nil
	}
	oldRaw, err := json.Marshal(stored)
	if err != nil {
		return nil, operatorFieldError(field)
	}
	var projection any
	if json.Unmarshal(oldRaw, &projection) != nil {
		return nil, operatorFieldError(field)
	}
	var strip func(any) any
	strip = func(v any) any {
		switch x := v.(type) {
		case string:
			return operatoraccess.StripURLUserinfo(x)
		case []any:
			for i := range x {
				x[i] = strip(x[i])
			}
		case map[string]any:
			for k := range x {
				x[k] = strip(x[k])
			}
		}
		return v
	}
	masked, err := json.Marshal(strip(projection))
	if err == nil && string(masked) == string(raw) {
		return stored, nil
	}
	return nil, operatorFieldError(field)
}
func (s *adminServiceImpl) operatorAccountCreateRules(ctx context.Context, input *CreateAccountInput) error {
	sc, _, err := s.operatorScope(ctx)
	if err != nil {
		return err
	}
	if sc == nil {
		return nil
	}
	if err := operatorRegionCheck(input.Credentials); err != nil {
		return err
	}
	for _, path := range OperatorAddressKeys {
		m, k := operatorMapKey(path, input.Credentials, input.Extra)
		v := m[k]
		if operatorEmpty(v) || v == false {
			continue
		}
		if !sc.Has(operatoraccess.FlagAccountsUpstreamAddress) {
			return operatorNetworkError(path, "address")
		}
		if _, err := operatorRestoreURLUserinfo(v, nil, path); err != nil {
			return err
		}
		if err := operatorAccountAddressValues(ctx, path, v); err != nil {
			return err
		}
	}
	if input.ProxyID != nil && *input.ProxyID != 0 && !sc.Has(operatoraccess.FlagAccountsUpstreamAddress) {
		return operatorNetworkError("proxy_id", "address")
	}
	if !sc.Has(operatoraccess.FlagUsersBalanceAdjust) {
		for _, key := range OperatorAccountBillingExtraKeys {
			v, ok := input.Extra[key]
			if ok && (key == "cache_ttl_override_target" || (v != nil && v != false)) {
				return operatorFieldError("extra." + key)
			}
		}
	}
	return nil
}
func (s *adminServiceImpl) operatorAccountUpdateRules(ctx context.Context, account *Account, input *UpdateAccountInput) error {
	sc, _, err := s.operatorScope(ctx)
	if err != nil {
		return err
	}
	if sc == nil {
		return nil
	}
	address := sc.Has(operatoraccess.FlagAccountsUpstreamAddress)
	for _, path := range OperatorNetworkIdentityKeys {
		m, k := operatorMapKey(path, input.Credentials, input.Extra)
		old, _ := operatorMapKey(path, account.Credentials, account.Extra)
		if m == nil {
			continue
		}
		v, present := m[k]
		if present && slices.Contains(OperatorAddressKeys, path) && path != "extra.custom_base_url_enabled" {
			restored, restoreErr := operatorRestoreURLUserinfo(v, old[k], path)
			if restoreErr != nil {
				return restoreErr
			}
			m[k] = restored
			v = restored
		}
		if !present || (!address && slices.Contains(OperatorAddressKeys, path) && operatorEmpty(v)) {
			if stored, ok := old[k]; ok {
				m[k] = stored
			} else {
				delete(m, k)
			}
		}
	}
	if !sc.Has(operatoraccess.FlagUsersBalanceAdjust) && input.Extra != nil {
		for _, k := range OperatorAccountBillingExtraKeys {
			if v, ok := input.Extra[k]; ok {
				if !reflect.DeepEqual(v, account.Extra[k]) {
					return operatorFieldError("extra." + k)
				}
			} else if v, ok := account.Extra[k]; ok {
				input.Extra[k] = v
			}
		}
	}
	changed := ""
	if input.Type != "" && input.Type != account.Type {
		changed = "type"
	}
	if input.ProxyID != nil {
		old := int64(0)
		if account.ProxyID != nil {
			old = *account.ProxyID
		}
		if *input.ProxyID != old && changed == "" {
			changed = "proxy_id"
		}
	}
	var addresses []string
	for _, path := range OperatorNetworkIdentityKeys {
		m, k := operatorMapKey(path, input.Credentials, input.Extra)
		old, _ := operatorMapKey(path, account.Credentials, account.Extra)
		if m != nil && !reflect.DeepEqual(m[k], old[k]) {
			if changed == "" {
				changed = path
			}
			if slices.Contains(OperatorAddressKeys, path) {
				addresses = append(addresses, path)
			}
		}
	}
	region := account.Credentials["aws_region"]
	if input.Credentials != nil {
		region = input.Credentials["aws_region"]
	}
	if !operatorEmpty(region) && (!reflect.DeepEqual(region, account.Credentials["aws_region"]) || (input.Type == "bedrock" && input.Type != account.Type)) {
		if err := operatorRegionCheck(map[string]any{"aws_region": region}); err != nil {
			return err
		}
	}
	if changed != "" {
		if slices.Contains(OperatorRefreshableTypes, account.Type) {
			return operatorNetworkError(changed, "refreshable_account")
		}
		if !address {
			return operatorNetworkError(changed, "address")
		}
		for _, path := range addresses {
			m, k := operatorMapKey(path, input.Credentials, input.Extra)
			if err := operatorAccountAddressValues(ctx, path, m[k]); err != nil {
				return err
			}
		}
		for _, key := range SensitiveCredentialKeys {
			stored := account.Credentials[key]
			if operatorEmpty(stored) {
				continue
			}
			v, present := input.Credentials[key]
			if !present || (!operatorEmpty(v) && reflect.DeepEqual(v, stored)) {
				return infraerrors.BadRequest("ACCOUNT_API_KEY_REQUIRED", "Re-enter every stored secret").WithMetadata(map[string]string{"field": "credentials." + key})
			}
		}
	}
	operatorAccountHeaders(account, input, changed != "")
	return nil
}
func operatorAccountHeaders(account *Account, input *UpdateAccountInput, changed bool) {
	if input.Credentials == nil {
		if !changed {
			return
		}
		input.Credentials = map[string]any{}
		for _, path := range OperatorNetworkIdentityKeys {
			if key, ok := strings.CutPrefix(path, "credentials."); ok {
				if value, present := account.Credentials[key]; present {
					input.Credentials[key] = value
				}
			}
		}
	}
	raw, present := input.Credentials[credKeyHeaderOverrides]
	if !present && !changed {
		return
	}
	incoming := stringMappingFromRaw(raw)
	stored := stringMappingFromRaw(account.Credentials[credKeyHeaderOverrides])
	out := map[string]any{}
	for k, v := range incoming {
		if v == operatoraccess.HeaderMask {
			if !changed {
				for old, value := range stored {
					if strings.EqualFold(k, old) {
						out[k] = value
						break
					}
				}
			}
		} else {
			out[k] = v
		}
	}
	input.Credentials[credKeyHeaderOverrides] = out
}
func (s *adminServiceImpl) operatorAccountExtraRules(ctx context.Context, id int64, updates map[string]any) error {
	sc, _, err := s.operatorScope(ctx)
	if err != nil {
		return err
	}
	if sc == nil {
		return nil
	}
	needed := false
	for k := range updates {
		if slices.Contains(OperatorAccountBillingExtraKeys, k) || slices.Contains(OperatorAddressKeys, "extra."+k) {
			needed = true
		}
	}
	if !needed {
		return nil
	}
	if s.accountRepo == nil {
		return operatorUnavailable()
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return operatorLoadError(err, ErrAccountNotFound)
	}
	if account == nil {
		return ErrAccountNotFound
	}
	for _, path := range OperatorAddressKeys {
		if k, ok := strings.CutPrefix(path, "extra."); ok {
			if v, present := updates[k]; present {
				restored, err := operatorRestoreURLUserinfo(v, account.Extra[k], path)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(restored, account.Extra[k]) {
					return operatorNetworkError(path, "address")
				}
				updates[k] = restored
			}
		}
	}
	if !sc.Has(operatoraccess.FlagUsersBalanceAdjust) {
		for _, k := range OperatorAccountBillingExtraKeys {
			if v, ok := updates[k]; ok && !reflect.DeepEqual(v, account.Extra[k]) {
				return operatorFieldError("extra." + k)
			}
		}
	}
	return nil
}
func (s *adminServiceImpl) operatorAccountBulkRules(ctx context.Context, input *BulkUpdateAccountsInput) error {
	sc, _, err := s.operatorScope(ctx)
	if err != nil {
		return err
	}
	if sc == nil {
		return nil
	}
	// Bulk updates merge keys. Remove unchanged address projections after
	// validating every target so each account retains its own stored userinfo.
	var targets []*Account
	for _, path := range OperatorAddressKeys {
		if path == "extra.custom_base_url_enabled" {
			continue
		}
		m, k := operatorMapKey(path, input.Credentials, input.Extra)
		value, present := m[k]
		if !present {
			continue
		}
		if len(input.AccountIDs) == 0 {
			return operatorNetworkError(path, "address")
		}
		if s.accountRepo == nil {
			return operatorUnavailable()
		}
		if targets == nil {
			targets, err = s.accountRepo.GetByIDs(ctx, input.AccountIDs)
			if err != nil {
				return operatorUnavailable()
			}
		}
		seen := map[int64]bool{}
		for _, account := range targets {
			if account == nil {
				continue
			}
			old, key := operatorMapKey(path, account.Credentials, account.Extra)
			restored, e := operatorRestoreURLUserinfo(value, old[key], path)
			if e != nil {
				return e
			}
			if !reflect.DeepEqual(restored, old[key]) {
				return operatorNetworkError(path, "address")
			}
			seen[account.ID] = true
		}
		for _, id := range input.AccountIDs {
			if !seen[id] {
				return ErrAccountNotFound
			}
		}
		delete(m, k)
	}
	for _, path := range OperatorNetworkIdentityKeys {
		m, k := operatorMapKey(path, input.Credentials, input.Extra)
		if _, ok := m[k]; ok {
			cause := "bulk"
			if slices.Contains(OperatorAddressKeys, path) {
				cause = "address"
			}
			return operatorNetworkError(path, cause)
		}
	}
	if input.ProxyID != nil {
		return operatorNetworkError("proxy_id", "bulk")
	}
	for _, v := range stringMappingFromRaw(input.Credentials[credKeyHeaderOverrides]) {
		if v == operatoraccess.HeaderMask {
			return operatorFieldError("header_overrides")
		}
	}
	if !sc.Has(operatoraccess.FlagUsersBalanceAdjust) {
		for _, k := range OperatorAccountBillingExtraKeys {
			if _, ok := input.Extra[k]; ok {
				return operatorFieldError("extra." + k)
			}
		}
	}
	return nil
}

// OperatorCheckOAuthApply runs before stored credentials are merged into a refresh.
func OperatorCheckOAuthApply(ctx context.Context, account *Account, typ string, credentials, extra map[string]any) error {
	sc := operatoraccess.ScopeFrom(ctx)
	if sc == nil {
		return nil
	}
	if typ != account.Type {
		return operatorNetworkError("type", "refreshable_account")
	}
	for _, path := range OperatorNetworkIdentityKeys {
		m, k := operatorMapKey(path, credentials, extra)
		if _, ok := m[k]; ok {
			return operatorNetworkError(path, "refreshable_account")
		}
	}
	if !sc.Has(operatoraccess.FlagUsersBalanceAdjust) {
		for _, k := range OperatorAccountBillingExtraKeys {
			if v, ok := extra[k]; ok && !reflect.DeepEqual(v, account.Extra[k]) {
				return operatorFieldError("extra." + k)
			}
		}
	}
	return nil
}
