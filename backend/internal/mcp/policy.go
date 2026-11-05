package mcp

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/service"
)

const ContractVersion = "readonly-mcp-v1-20261105"

var pageParameters = []string{"start_time", "end_time", "limit", "cursor"}

// ToolPolicies returns a copy. Each request builds its own SDK server view;
// neither the catalog nor another user's registry can be changed by a request.
func ToolPolicies() []ToolPolicy {
	page := func(extra ...string) []string { return append(append([]string{}, pageParameters...), extra...) }
	out := []ToolPolicy{
		{Name: "xy2api.requests.search", CatalogKeys: []string{"GET /ops/requests"}, OptionalKeys: []string{"GET /ops/upstream-errors"}, Ops: true, Parameters: page("request_id", "client_request_id", "platform", "model", "account_id", "group_id", "user_id", "api_key_id", "include_recovered_upstream")},
		{Name: "xy2api.requests.get_detail", CatalogKeys: []string{"GET /ops/errors/:id"}, Ops: true, Parameters: []string{"error_row_id", "start_time", "end_time"}, Required: []string{"error_row_id"}},
		{Name: "xy2api.requests.get_attempts", CatalogKeys: []string{"GET /ops/request-errors/:id", "GET /ops/request-errors/:id/upstream-errors"}, OptionalKeys: []string{"GET /ops/upstream-errors/:id"}, Ops: true, Parameters: page("error_row_id"), Required: []string{"error_row_id"}},
		{Name: "xy2api.requests.search_ingress_rejections", CatalogKeys: []string{"GET /ops/ingress-rejections"}, Ops: true, Parameters: page("reason")},
		{Name: "xy2api.accounts.search", CatalogKeys: []string{"GET /accounts"}, Parameters: page("search", "platform", "status", "group_id")},
		{Name: "xy2api.accounts.get_health", CatalogKeys: []string{"POST /accounts/recent-health/batch", "GET /accounts/upstream-balances"}, Runtime: true, Parameters: []string{"limit", "cursor", "account_ids"}, Required: []string{"account_ids"}},
		{Name: "xy2api.groups.search", CatalogKeys: []string{"GET /groups"}, Parameters: page("search", "platform", "status")},
		{Name: "xy2api.groups.get_usage_summary", CatalogKeys: []string{"GET /groups/usage-summary"}, Parameters: page("group_id"), Required: []string{"group_id"}},
		{Name: "xy2api.channels.search", CatalogKeys: []string{"GET /channels"}, Parameters: page("search", "status", "group_id")},
		{Name: "xy2api.channels.get_metrics", CatalogKeys: []string{"GET /channel-monitor-v2/snapshot", "GET /channel-monitor-v2/matrix", "GET /channel-monitor-v2/errors"}, Parameters: page("group_id", "platform")},
		{Name: "xy2api.ops.get_error_breakdown", CatalogKeys: []string{"GET /ops/dashboard/error-distribution"}, Ops: true, Parameters: page("platform", "group_id", "model")},
		{Name: "xy2api.ops.get_trends", CatalogKeys: []string{"GET /ops/dashboard/error-trend"}, Ops: true, Parameters: page("platform", "group_id", "granularity")},
		{Name: "xy2api.ops.get_alerts", CatalogKeys: []string{"GET /ops/alert-events"}, Ops: true, Parameters: page("level", "status")},
		{Name: "xy2api.ops.get_system_events", CatalogKeys: []string{"GET /ops/system-logs"}, Ops: true, Parameters: page("level", "component", "request_id", "client_request_id")},
		{Name: "xy2api.ops.get_realtime_snapshot", CatalogKeys: []string{"GET /ops/realtime-traffic"}, Ops: true, Realtime: true, Parameters: page("platform", "group_id")},
		{Name: "xy2api.ops.get_concurrency_snapshot", CatalogKeys: []string{"GET /ops/concurrency"}, Ops: true, Realtime: true, Runtime: true, Parameters: []string{"account_ids"}, Required: []string{"account_ids"}},
		{Name: "xy2api.routing.get_quality_state", CatalogKeys: []string{"GET /group-quality-gate/status"}, OptionalKeys: []string{"GET /group-quality-gate/policies"}, Runtime: true, Parameters: []string{"group_id", "include_policies", "limit", "cursor"}},
		{Name: "xy2api.users.search", CatalogKeys: []string{"GET /users"}, Parameters: page("search", "group_id", "status")},
		{Name: "xy2api.users.get_usage_summary", CatalogKeys: []string{"GET /users/:id/usage"}, TargetUser: true, Parameters: page("user_id", "model"), Required: []string{"user_id"}},
		{Name: "xy2api.users.get_subscriptions", CatalogKeys: []string{"GET /users/:id/subscriptions"}, TargetUser: true, Parameters: page("user_id"), Required: []string{"user_id"}},
		{Name: "xy2api.audit.search", CatalogKeys: []string{"GET /audit-logs"}, Parameters: page("user_id", "status")},
		{Name: "xy2api.security.search_prompt_audit", CatalogKeys: []string{"GET /prompt-audit/events", "GET /prompt-audit/events/:id"}, Parameters: page("user_id", "group_id", "model")},
		{Name: "xy2api.me.get_profile", UserOwned: true},
		{Name: "xy2api.me.search_usage", UserOwned: true, Parameters: page("api_key_id", "group_id", "request_id", "model")},
		{Name: "xy2api.me.get_usage_detail", UserOwned: true, Parameters: []string{"usage_row_id", "start_time", "end_time"}, Required: []string{"usage_row_id"}},
		{Name: "xy2api.me.get_error_history", UserOwned: true, UserError: true, Parameters: page("api_key_id", "request_id", "model", "status")},
		{Name: "xy2api.me.get_error_detail", UserOwned: true, UserError: true, Parameters: []string{"error_row_id", "start_time", "end_time"}, Required: []string{"error_row_id"}},
		{Name: "xy2api.me.get_subscription_status", UserOwned: true, Parameters: []string{"limit", "cursor"}},
		{Name: "xy2api.me.get_quota_status", UserOwned: true, Parameters: []string{"limit", "cursor"}},
	}
	return out
}

type userReader interface {
	GetByID(context.Context, int64) (*service.User, error)
}
type scopeReader interface {
	LoadScope(context.Context, *service.User) (*operatoraccess.Scope, error)
}
type complianceReader interface {
	IsAdminComplianceAcknowledged(context.Context, int64) (bool, error)
}
type settingsReader interface {
	GetMultiple(context.Context, []string) (map[string]string, error)
}

func freshActor(ctx context.Context, users userReader, access scopeReader, id int64) (context.Context, Actor, error) {
	if users == nil {
		return ctx, Actor{}, ErrUnavailable
	}
	u, e := users.GetByID(ctx, id)
	if e != nil || u == nil || !u.IsActive() || u.DeletedAt != nil {
		return ctx, Actor{}, ErrForbidden
	}
	a := Actor{UserID: u.ID, Role: u.Role, TokenVersion: u.TokenVersion}
	switch u.Role {
	case service.RoleAdmin:
		ctx = operatoraccess.WithSuperAdmin(ctx, u.ID)
	case service.RoleOperator:
		if access == nil {
			return ctx, Actor{}, ErrUnavailable
		}
		a.Scope, e = access.LoadScope(ctx, u)
		if e != nil || a.Scope == nil {
			return ctx, Actor{}, ErrUnavailable
		}
		ctx = operatoraccess.WithScope(ctx, a.Scope)
	case service.RoleUser:
	default:
		return ctx, Actor{}, ErrForbidden
	}
	return ctx, a, nil
}

func catalogAllowed(a Actor, key string) bool {
	e, ok := operatoraccess.Lookup(key)
	if !ok || e.Level != operatoraccess.LevelView || e.Kind != operatoraccess.KindModule {
		return false
	}
	if e.Fields != "" && e.Fields != "user_search" {
		return false
	}
	if e.Target != "" && e.Target != "user_path_id" {
		return false
	}
	if a.Role == service.RoleAdmin {
		return true
	}
	return a.Role == service.RoleOperator && a.Scope != nil && operatoraccess.Decide(e, true, a.Scope.Perms).Reason == ""
}
func policyAllowed(a Actor, p ToolPolicy) bool {
	if p.UserOwned {
		return a.Role == service.RoleUser || a.Role == service.RoleOperator || a.Role == service.RoleAdmin
	}
	for _, key := range p.CatalogKeys {
		if !catalogAllowed(a, key) {
			return false
		}
	}
	return len(p.CatalogKeys) > 0
}

func checkTarget(ctx context.Context, a Actor, p ToolPolicy, in QueryInput) error {
	if !p.TargetUser {
		return nil
	}
	if in.UserID <= 0 {
		return ErrInvalidInput
	}
	if a.Role == service.RoleAdmin {
		return nil
	}
	if a.Scope == nil || a.Scope.Guard == nil {
		return ErrForbidden
	}
	for _, key := range p.CatalogKeys {
		e, ok := operatoraccess.Lookup(key)
		if !ok {
			return ErrForbidden
		}
		if e.Target == "" {
			continue
		}
		r := operatoraccess.Request{Method: "GET", Key: key, Params: map[string]string{"id": strconv.FormatInt(in.UserID, 10)}, Query: url.Values{}}
		if err := a.Scope.Guard.CheckTarget(ctx, a.Scope, e.Target, r); err != nil {
			return ErrForbidden
		}
	}
	return nil
}

// Read settings directly: the console's cached helpers deliberately return
// permissive defaults on DB errors and cannot enforce MCP's fail-closed gates.
type gateSnapshot struct{ backend, monitor, realtime, userErrors bool }

func loadGates(ctx context.Context, repo settingsReader, opsEnabled bool) (gateSnapshot, error) {
	if repo == nil {
		return gateSnapshot{}, ErrUnavailable
	}
	vals, e := repo.GetMultiple(ctx, []string{service.SettingKeyBackendModeEnabled, service.SettingKeyOpsMonitoringEnabled, service.SettingKeyOpsRealtimeMonitoringEnabled, service.SettingKeyAllowUserViewErrorRequests})
	if e != nil {
		return gateSnapshot{}, ErrUnavailable
	}
	readBool := func(key string, defaultValue bool) (bool, error) {
		switch strings.ToLower(strings.TrimSpace(vals[key])) {
		case "":
			return defaultValue, nil
		case "true", "1", "on", "enabled":
			return true, nil
		case "false", "0", "off", "disabled":
			return false, nil
		default:
			return false, ErrUnavailable
		}
	}
	backend, e1 := readBool(service.SettingKeyBackendModeEnabled, false)
	monitor, e2 := readBool(service.SettingKeyOpsMonitoringEnabled, true)
	realtime, e3 := readBool(service.SettingKeyOpsRealtimeMonitoringEnabled, true)
	userErrors, e4 := readBool(service.SettingKeyAllowUserViewErrorRequests, false)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return gateSnapshot{}, ErrUnavailable
	}
	return gateSnapshot{backend: backend, monitor: opsEnabled && monitor, realtime: realtime, userErrors: userErrors}, nil
}
func checkGates(ctx context.Context, a Actor, p ToolPolicy, g gateSnapshot, compliance complianceReader) error {
	if p.UserOwned {
		if a.Role == service.RoleUser && g.backend {
			return ErrForbidden
		}
		if p.UserError && !g.userErrors {
			return ErrSourceDisabled
		}
	} else {
		if compliance == nil {
			return ErrUnavailable
		}
		ok, e := compliance.IsAdminComplianceAcknowledged(ctx, a.UserID)
		if e != nil {
			return ErrUnavailable
		}
		if !ok {
			return ErrForbidden
		}
	}
	if p.Ops && !g.monitor {
		return ErrSourceDisabled
	}
	if p.Realtime && !g.realtime {
		return ErrSourceDisabled
	}
	return nil
}

func additionalSource(a Actor, p ToolPolicy, key string) error {
	if !slices.Contains(p.OptionalKeys, key) || !catalogAllowed(a, key) {
		return ErrForbidden
	}
	return nil
}

func applyRedaction(a Actor, p ToolPolicy, result QueryResult) (QueryResult, error) {
	if a.Scope == nil {
		return result, nil
	}
	rules := []operatoraccess.Redaction{}
	for _, key := range p.CatalogKeys {
		e, ok := operatoraccess.Lookup(key)
		if !ok {
			return QueryResult{}, ErrForbidden
		}
		rules = append(rules, e.Redact...)
	}
	if len(rules) == 0 {
		return result, nil
	}
	b, e := json.Marshal(result)
	if e != nil {
		return QueryResult{}, ErrUnavailable
	}
	b, e = operatoraccess.Redact(b, rules, a.Scope)
	if e != nil {
		return QueryResult{}, ErrUnavailable
	}
	if json.Unmarshal(b, &result) != nil {
		return QueryResult{}, ErrUnavailable
	}
	return result, nil
}
