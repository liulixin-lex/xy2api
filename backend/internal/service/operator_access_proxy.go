package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
)

// operatorProxyInUse expands backwards through backup edges. A root used by an
// account (including a fallback origin) or a global provider protects its chain.
func (s *OperatorAccessService) operatorProxyInUse(ctx context.Context, id int64) (bool, error) {
	if s.proxyRepo == nil || s.queryRepo == nil || s.settingService == nil || s.moderationService == nil {
		return false, operatorUnavailable()
	}
	proxies, err := s.proxyRepo.ListAllForFallback(ctx)
	if err != nil {
		return false, operatorUnavailable()
	}
	roots := map[int64]bool{id: true}
	for changed := true; changed; {
		changed = false
		for _, p := range proxies {
			if !roots[p.ID] && p.FallbackMode == FallbackModeProxy && p.BackupProxyID != nil && roots[*p.BackupProxyID] {
				roots[p.ID] = true
				changed = true
			}
		}
	}
	ids := make([]int64, 0, len(roots))
	for id := range roots {
		ids = append(ids, id)
	}
	used, err := s.queryRepo.ProxyIDsUsedByAccounts(ctx, ids)
	if err != nil {
		return false, operatorUnavailable()
	}
	// Load every source even if an account already marks it in use: load failures
	// must not turn into an accidental permission grant on a later edit.
	web, err := s.settingService.GetWebSearchEmulationConfig(ctx)
	if err != nil || web == nil {
		return false, operatorUnavailable()
	}
	moderation, err := s.moderationService.GetConfig(ctx)
	if err != nil || moderation == nil {
		return false, operatorUnavailable()
	}
	inUse := len(used) > 0
	for _, p := range web.Providers {
		if p.ProxyID != nil && roots[*p.ProxyID] {
			inUse = true
		}
	}
	if moderation.ProxyID != nil && roots[*moderation.ProxyID] {
		inUse = true
	}
	for _, cfg := range moderation.EngineConfigs {
		if cfg != nil && cfg.ProxyID != nil && roots[*cfg.ProxyID] {
			inUse = true
		}
	}
	return inUse, nil
}

func (s *OperatorAccessService) operatorProxyRules(ctx context.Context, sc *operatoraccess.Scope, rule operatoraccess.FieldRule, r operatoraccess.Request, obj operatorObject) error {
	if rule == "proxy_create" {
		if !sc.Has(operatoraccess.FlagAccountsUpstreamAddress) {
			return operatorNetworkError("host", "address")
		}
		if r.Key == "POST /proxies/batch" {
			proxies, err := operatorRead[[]operatorObject](obj, "proxies")
			if err != nil {
				return err
			}
			for _, p := range proxies {
				host, err := operatorRead[string](p, "host")
				if err != nil {
					return err
				}
				if err = validateOperatorHost(ctx, host); err != nil {
					return operatorPrivateAddress("host")
				}
			}
			return nil
		}
		host, err := operatorRead[string](obj, "host")
		if err != nil {
			return err
		}
		if err = validateOperatorHost(ctx, host); err != nil {
			return operatorPrivateAddress("host")
		}
		return nil
	}
	if rule == "proxy_delete" {
		field := "ids"
		ids, err := operatorRead[[]int64](obj, "ids")
		if err != nil {
			return err
		}
		if r.Params["id"] != "" {
			field = "id"
			id, e := operatorPathID(r, "id")
			if e != nil {
				return e
			}
			ids = []int64{id}
		}
		ids, err = operatorDistinctIDs(ids, field)
		if err != nil {
			return err
		}
		for _, id := range ids {
			used, err := s.operatorProxyInUse(ctx, id)
			if err != nil {
				return err
			}
			if used {
				return operatorFieldError(field).WithMetadata(map[string]string{"field": field, "cause": "proxy_in_use", "id": strconv.FormatInt(id, 10)})
			}
		}
		return nil
	}
	id, err := operatorPathID(r, "id")
	if err != nil {
		return err
	}
	if s.adminService == nil {
		return operatorUnavailable()
	}
	stored, err := s.adminService.GetProxy(ctx, id)
	if err != nil {
		return operatorLoadError(err, ErrProxyNotFound)
	}
	if stored == nil {
		return ErrProxyNotFound
	}
	differences := map[string]bool{}
	for _, pair := range []struct{ key, stored string }{{"protocol", stored.Protocol}, {"host", stored.Host}, {"fallback_mode", stored.FallbackMode}} {
		value, err := operatorRead[string](obj, pair.key)
		if err != nil {
			return err
		}
		value = strings.TrimSpace(value)
		differences[pair.key] = value != "" && value != pair.stored
	}
	port, err := operatorRead[int](obj, "port")
	if err != nil {
		return err
	}
	differences["port"] = port != 0 && port != stored.Port
	for _, pair := range []struct{ key, stored string }{{"username", stored.Username}, {"password", stored.Password}} {
		value, err := operatorRead[*string](obj, pair.key)
		if err != nil {
			return err
		}
		differences[pair.key] = value != nil && strings.TrimSpace(*value) != pair.stored
	}
	backup, err := operatorRead[operatorNullableInt64Field](obj, "backup_proxy_id")
	if err != nil {
		return err
	}
	differences["backup_proxy_id"] = backup.Set && !operatorInt64PtrEqual(backup.Value, stored.BackupProxyID)
	fields := []string{"protocol", "host", "port", "username", "password", "fallback_mode", "backup_proxy_id"}
	if !sc.Has(operatoraccess.FlagAccountsUpstreamAddress) {
		for _, field := range fields {
			if differences[field] {
				return operatorNetworkError(field, "address")
			}
		}
	}
	if differences["host"] || differences["protocol"] || differences["port"] {
		host := stored.Host
		if differences["host"] {
			host, _ = operatorRead[string](obj, "host")
		}
		if err = validateOperatorHost(ctx, host); err != nil {
			return operatorPrivateAddress("host")
		}
	}
	used, err := s.operatorProxyInUse(ctx, id)
	if err != nil {
		return err
	}
	if used {
		for _, field := range fields {
			if field != "password" && differences[field] {
				return operatorFieldError(field)
			}
		}
		status, e := operatorRead[string](obj, "status")
		if e != nil {
			return e
		}
		status = strings.TrimSpace(status)
		if status != "" && status != stored.Status {
			return operatorFieldError("status")
		}
		if operatorHas(obj, "expires_at") {
			expires, e := operatorRead[operatorNullableInt64Field](obj, "expires_at")
			if e != nil {
				return e
			}
			if expires.Value != nil && *expires.Value > 0 && (stored.ExpiresAt == nil || time.Unix(*expires.Value, 0).Before(*stored.ExpiresAt)) {
				return operatorFieldError("expires_at")
			}
		}
	} else if stored.Password != "" && (differences["protocol"] || differences["host"] || differences["port"] || differences["username"]) {
		password, e := operatorRead[*string](obj, "password")
		if e != nil {
			return e
		}
		if password == nil || strings.TrimSpace(*password) == "" {
			return operatorFieldError("password").WithMetadata(map[string]string{"field": "password", "requires": "password"})
		}
	}
	return nil
}
func operatorInt64PtrEqual(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
