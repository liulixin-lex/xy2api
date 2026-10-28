package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
)

type operatorObject map[string]json.RawMessage

func operatorObjectBody(body []byte) (operatorObject, error) {
	obj := operatorObject{}
	if len(bytes.TrimSpace(body)) == 0 {
		return obj, nil
	}
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return nil, operatorFieldError("body")
	}
	return obj, nil
}
func operatorRead[T any](obj operatorObject, key string) (T, error) {
	var value T
	if raw, ok := operatoraccess.Field(obj, key); ok {
		if err := json.Unmarshal(raw, &value); err != nil {
			return value, operatorFieldError(key)
		}
	}
	return value, nil
}
func operatorHas(obj operatorObject, key string) bool {
	_, ok := operatoraccess.Field(obj, key)
	return ok
}
func operatorSet(obj operatorObject, key string, value any) {
	for k := range obj {
		if strings.EqualFold(k, key) {
			delete(obj, k)
		}
	}
	obj[key], _ = json.Marshal(value)
}
func operatorPathID(r operatoraccess.Request, key string) (int64, error) {
	raw := r.Params[key]
	if r.Key == "POST /risk-control/users/:user_id/unban" && key == "user_id" {
		// ContentModerationHandler.UnbanUser trims its path parameter.
		raw = strings.TrimSpace(raw)
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, operatorFieldError(key)
	}
	return id, nil
}
func operatorDistinctIDs(ids []int64, field string) ([]int64, error) {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		if id <= 0 {
			return nil, operatorFieldError(field)
		}
		seen[id] = true
		out = append(out, id)
		if len(out) > 1000 {
			return nil, operatorFieldError(field).WithMetadata(map[string]string{"field": field, "max": "1000"})
		}
	}
	return out, nil
}
func operatorTargetError() error {
	return infraerrors.Forbidden("ADMIN_TARGET_FORBIDDEN", "Target user is not allowed")
}
func operatorSuperOnly() error {
	return infraerrors.Forbidden("SUPER_ADMIN_REQUIRED", "Super admin required")
}
func operatorNeedsFlag(flag operatoraccess.Flag) error {
	return infraerrors.Forbidden("ADMIN_PERMISSION_DENIED", "Permission required").WithMetadata(map[string]string{"required": string(flag)})
}

func (s *OperatorAccessService) operatorTargetUsers(ctx context.Context, sc *operatoraccess.Scope, ids []int64, field string) error {
	ids, err := operatorDistinctIDs(ids, field)
	if err != nil {
		return err
	}
	if len(ids) > 0 && (s == nil || s.userRepo == nil || sc == nil) {
		return operatorUnavailable()
	}
	for _, id := range ids {
		user, err := s.userRepo.GetByIDIncludeDeleted(ctx, id)
		if err != nil {
			return operatorLoadError(err, ErrUserNotFound)
		}
		if user == nil {
			return ErrUserNotFound
		}
		if user.Role != RoleUser || user.ID == sc.UserID {
			return operatorTargetError()
		}
	}
	return nil
}

// CheckTarget follows the same folded JSON lookup and numeric types as Gin.
func (s *OperatorAccessService) CheckTarget(ctx context.Context, sc *operatoraccess.Scope, rule operatoraccess.TargetRule, r operatoraccess.Request) error {
	if s == nil || sc == nil {
		return operatorUnavailable()
	}
	obj, err := operatorObjectBody(r.Body)
	if err != nil {
		return err
	}
	var ids []int64
	field := "id"
	switch rule {
	case "user_path_id", "user_path_user_id":
		if rule == "user_path_user_id" {
			field = "user_id"
		}
		id, err := operatorPathID(r, field)
		if err != nil {
			return err
		}
		ids = []int64{id}
	case "user_body_id":
		field = "user_id"
		id, err := operatorRead[int64](obj, field)
		if err != nil {
			return err
		}
		ids = []int64{id}
	case "user_body_ids":
		field = "user_ids"
		ids, err = operatorRead[[]int64](obj, field)
	case "apikey_owner":
		id, e := operatorPathID(r, "id")
		if e != nil {
			return e
		}
		if s.apiKeyRepo == nil {
			return operatorUnavailable()
		}
		key, e := s.apiKeyRepo.GetByID(ctx, id)
		if e != nil {
			return operatorLoadError(e, ErrAPIKeyNotFound)
		}
		if key == nil {
			return ErrAPIKeyNotFound
		}
		ids = []int64{key.UserID}
	case "sub_owner", "sub_body_owners":
		if rule == "sub_owner" {
			id, e := operatorPathID(r, "id")
			if e != nil {
				return e
			}
			ids = []int64{id}
		} else {
			field = "subscription_ids"
			ids, err = operatorRead[[]int64](obj, field)
		}
		if err != nil {
			return err
		}
		ids, err = operatorDistinctIDs(ids, field)
		if err != nil {
			return err
		}
		if len(ids) > 0 && s.subscriptionRepo == nil {
			return operatorUnavailable()
		}
		for i, id := range ids {
			sub, e := s.subscriptionRepo.GetByIDIncludeDeleted(ctx, id)
			if e != nil {
				return operatorLoadError(e, ErrSubscriptionNotFound)
			}
			if sub == nil {
				return ErrSubscriptionNotFound
			}
			ids[i] = sub.UserID
		}
	case "order_owner":
		id, e := operatorPathID(r, "id")
		if e != nil {
			return e
		}
		if s.paymentService == nil {
			return operatorUnavailable()
		}
		order, e := s.paymentService.GetOrderByID(ctx, id)
		if e != nil {
			if errors.Is(e, ErrOrderNotFound) {
				return e
			}
			if infraerrors.Code(e) == 404 {
				return e
			}
			return operatorUnavailable()
		}
		if order == nil {
			return ErrOrderNotFound
		}
		ids = []int64{order.UserID}
	case "group_rate_rows", "group_rpm_rows":
		id, e := operatorPathID(r, "id")
		if e != nil {
			return e
		}
		field = "entries[].user_id"
		entries, e := operatorRead[[]operatorObject](obj, "entries")
		if e != nil {
			return e
		}
		for _, entry := range entries {
			userID, e := operatorRead[int64](entry, "user_id")
			if e != nil {
				return e
			}
			ids = append(ids, userID)
		}
		if s.userGroupRateRepo == nil {
			return operatorUnavailable()
		}
		rows, e := s.userGroupRateRepo.GetByGroupID(ctx, id)
		if e != nil {
			return operatorUnavailable()
		}
		for _, row := range rows {
			if rule == "group_rate_rows" || row.RPMOverride != nil {
				ids = append(ids, row.UserID)
			}
		}
	case "group_members":
		id, e := operatorPathID(r, "id")
		if e != nil {
			return e
		}
		if s.queryRepo == nil {
			return operatorUnavailable()
		}
		protected, e := s.queryRepo.GroupHasProtectedMembers(ctx, id, sc.UserID)
		if e != nil {
			return operatorUnavailable()
		}
		if protected {
			return operatorTargetError()
		}
		return nil
	default:
		return operatorUnavailable()
	}
	if err != nil {
		return err
	}
	return s.operatorTargetUsers(ctx, sc, ids, field)
}

func (s *OperatorAccessService) CheckFields(ctx context.Context, sc *operatoraccess.Scope, rule operatoraccess.FieldRule, r operatoraccess.Request) ([]byte, error) {
	if s == nil || sc == nil {
		return nil, operatorUnavailable()
	}
	obj, err := operatorObjectBody(r.Body)
	if err != nil {
		return nil, err
	}
	switch rule {
	case "user_batch_no_all":
		all, e := operatorRead[bool](obj, "all")
		if e != nil {
			return nil, e
		}
		if all {
			return nil, operatorFieldError("all")
		}
	case "channel_create", "channel_update":
		return nil, s.operatorChannelRules(ctx, sc, rule, r, obj)
	case "monitor_create", "monitor_update", "monitor_template_update", "monitor_template_apply":
		return s.operatorMonitorRules(ctx, sc, rule, r, obj)
	case "proxy_create", "proxy_update", "proxy_delete":
		return nil, s.operatorProxyRules(ctx, sc, rule, r, obj)
	case "redeem_search", "promo_search":
		if !sc.Has(operatoraccess.FlagRedeemCodesIssue) {
			search := strings.TrimSpace(r.Query.Get("search"))
			if search != "" && (rule == "promo_search" || !strings.Contains(search, "@")) {
				return nil, operatorFieldError("search")
			}
			if strings.ToLower(strings.TrimSpace(r.Query.Get("sort_by"))) == "code" {
				return nil, operatorFieldError("sort_by")
			}
		}
	case "redeem_safe_rows", "redeem_batch_rows", "redeem_create_and_redeem":
		return nil, s.operatorRedeemRules(ctx, sc, rule, r, obj)
	case "sub_bulk_action":
		action, e := operatorRead[string](obj, "action")
		if e != nil {
			return nil, e
		}
		if slices.Contains([]string{"extend", "reset_quota", "restore"}, action) && !sc.Has(operatoraccess.FlagUsersBalanceAdjust) {
			return nil, operatorNeedsFlag(operatoraccess.FlagUsersBalanceAdjust)
		}
	case "aff_rate":
		// Presence matters, including false/null: the handler can clear a rate.
		if !sc.Has(operatoraccess.FlagUsersBalanceAdjust) && (operatorHas(obj, "aff_rebate_rate_percent") || operatorHas(obj, "clear_rebate_rate")) {
			return nil, operatorNeedsFlag(operatoraccess.FlagUsersBalanceAdjust)
		}
	case "codex_ticket_proxy":
		proxy, e := operatorRead[string](obj, "proxy_url")
		if e != nil {
			return nil, e
		}
		if proxy != "" {
			return nil, operatorFieldError("proxy_url")
		}
	case "risk_config":
		return nil, s.operatorRiskRules(ctx, obj)
	case "ops_runtime_alert":
		if s.opsService == nil {
			return nil, operatorUnavailable()
		}
		stored, e := s.opsService.GetOpsAlertRuntimeSettings(ctx)
		if e != nil || stored == nil {
			return nil, operatorUnavailable()
		}
		if operatorHas(obj, "distributed_lock") {
			value, e := operatorRead[OpsDistributedLockSettings](obj, "distributed_lock")
			if e != nil {
				return nil, e
			}
			if !reflect.DeepEqual(value, stored.DistributedLock) {
				return nil, operatorFieldError("distributed_lock")
			}
		} else {
			// The handler binds a value struct and the service replaces the whole
			// settings document. Preserve the lock when this protected field is
			// omitted, rather than letting the binder's zero value disable it.
			operatorSet(obj, "distributed_lock", stored.DistributedLock)
			return json.Marshal(obj)
		}
	case "account_url_probe":
		raw, e := operatorRead[string](obj, "base_url")
		if e != nil {
			return nil, e
		}
		if raw != "" {
			if !sc.Has(operatoraccess.FlagAccountsUpstreamAddress) {
				return nil, operatorNetworkError("base_url", "address")
			}
			return nil, operatorAddressField(ctx, "base_url", raw)
		}
	case "account_import_address":
		// The import binders use map[string]any for these dynamic containers.
		for _, key := range []string{"credentials", "credential_extras", "extra"} {
			if _, err := operatorRead[map[string]any](obj, key); err != nil {
				return nil, err
			}
		}
		return nil, operatorImportAddresses(ctx, sc, obj, "")
	default:
		// Service rules cannot accidentally become a second, weaker authorizer.
		return nil, operatorUnavailable()
	}
	return nil, nil
}

func operatorImportAddresses(ctx context.Context, sc *operatoraccess.Scope, obj operatorObject, prefix string) error {
	for key, raw := range obj {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		switch {
		case strings.EqualFold(key, "base_url") || strings.EqualFold(key, "custom_base_url") || strings.EqualFold(key, "api_base_urls") || strings.EqualFold(key, "custom_base_url_enabled"):
			if !sc.Has(operatoraccess.FlagAccountsUpstreamAddress) {
				return operatorNetworkError(path, "address")
			}
			if strings.EqualFold(key, "custom_base_url_enabled") {
				var enabled bool
				if json.Unmarshal(raw, &enabled) != nil {
					return operatorFieldError(path)
				}
				continue
			}
			if err := operatorAddressValues(ctx, raw, path); err != nil {
				return err
			}
		default:
			trimmed := bytes.TrimSpace(raw)
			if len(trimmed) > 0 && trimmed[0] == '{' {
				child, err := operatorObjectBody(raw)
				if err != nil {
					return err
				}
				if err = operatorImportAddresses(ctx, sc, child, path); err != nil {
					return err
				}
			}
			if len(trimmed) > 0 && trimmed[0] == '[' {
				if err := operatorImportArray(ctx, sc, raw, path); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func operatorImportArray(ctx context.Context, sc *operatoraccess.Scope, raw json.RawMessage, path string) error {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return operatorFieldError(path)
	}
	for _, value := range values {
		value = bytes.TrimSpace(value)
		if len(value) == 0 {
			continue
		}
		switch value[0] {
		case '{':
			obj, err := operatorObjectBody(value)
			if err != nil {
				return err
			}
			if err = operatorImportAddresses(ctx, sc, obj, path+"[]"); err != nil {
				return err
			}
		case '[':
			if err := operatorImportArray(ctx, sc, value, path+"[]"); err != nil {
				return err
			}
		}
	}
	return nil
}

func operatorAddressValues(ctx context.Context, raw json.RawMessage, path string) error {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if len(trimmed) == 0 {
		return operatorFieldError(path)
	}
	if trimmed[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return operatorFieldError(path)
		}
		if value != "" {
			return operatorAddressField(ctx, path, value)
		}
		return nil
	}
	if trimmed[0] == '{' {
		obj, err := operatorObjectBody(raw)
		if err != nil {
			return err
		}
		for k, v := range obj {
			if err = operatorAddressValues(ctx, v, path+"."+k); err != nil {
				return err
			}
		}
		return nil
	}
	if trimmed[0] == '[' {
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return operatorFieldError(path)
		}
		for _, v := range values {
			if err := operatorAddressValues(ctx, v, path+"[]"); err != nil {
				return err
			}
		}
		return nil
	}
	return operatorFieldError(path)
}

func (s *OperatorAccessService) operatorRedeemRules(ctx context.Context, sc *operatoraccess.Scope, rule operatoraccess.FieldRule, r operatoraccess.Request, obj operatorObject) error {
	if s.redeemRepo == nil {
		return operatorUnavailable()
	}
	if rule == "redeem_create_and_redeem" {
		kind, err := operatorRead[string](obj, "type")
		if err != nil {
			return err
		}
		if kind == "invitation" {
			return operatorFieldError("type")
		}
		code, err := operatorRead[string](obj, "code")
		if err != nil {
			return err
		}
		trimmed := strings.TrimSpace(code)
		if trimmed != code {
			return operatorFieldError("code")
		}
		stored, err := s.redeemRepo.GetByCode(ctx, trimmed)
		if err == nil && stored != nil {
			return operatorFieldError("code")
		}
		if err != nil && !errors.Is(err, ErrRedeemCodeNotFound) {
			return operatorUnavailable()
		}
		return nil
	}
	ids, err := operatorRead[[]int64](obj, "ids")
	if err != nil {
		return err
	}
	if rule == "redeem_safe_rows" && r.Params["id"] != "" {
		id, e := operatorPathID(r, "id")
		if e != nil {
			return e
		}
		ids = []int64{id}
	}
	ids, err = operatorDistinctIDs(ids, "ids")
	if err != nil {
		return err
	}
	var owners []int64
	for _, id := range ids {
		row, err := s.redeemRepo.GetByID(ctx, id)
		if err != nil {
			return operatorLoadError(err, ErrRedeemCodeNotFound)
		}
		if row == nil {
			return ErrRedeemCodeNotFound
		}
		if !slices.Contains([]string{"balance", "concurrency", "subscription", "invitation"}, row.Type) {
			return operatorSuperOnly()
		}
		if rule == "redeem_safe_rows" && !slices.Contains([]string{"unused", "disabled", "expired"}, row.Status) {
			return operatorSuperOnly()
		}
		if rule == "redeem_batch_rows" && row.UsedBy != nil {
			owners = append(owners, *row.UsedBy)
		}
	}
	return s.operatorTargetUsers(ctx, sc, owners, "ids")
}
