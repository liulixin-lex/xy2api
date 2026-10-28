package operatoraccess

import (
	"context"
	"net/url"
)

type scopeKey struct{}

const GinKey = "operator_permissions" // DEV_PLAN 4.3.1

// Scope is the operator's view of one request. A nil *Scope means "not an operator".
type Scope struct {
	UserID int64
	Perms  Permissions
	Guard  Guard                                                // set by the service
	Reload func(ctx context.Context) (Permissions, bool, error) // false: no longer an active operator
}

func WithScope(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}
func ScopeFrom(ctx context.Context) *Scope  { s, _ := ctx.Value(scopeKey{}).(*Scope); return s }
func (s *Scope) Can(m Module, l Level) bool { return s != nil && s.Perms.Can(m, l) }
func (s *Scope) Has(f Flag) bool            { return s != nil && s.Perms.Has(f) }

// HideAPIKeySearch is true for operators. user_repo uses it (Catalog §2.2 user_search).
// It reads the request context, so it sees the scope set by setOperatorScope.
func HideAPIKeySearch(ctx context.Context) bool { return ScopeFrom(ctx) != nil }

type Request struct {
	Method string
	Key    string
	Params map[string]string // gin path params
	Query  url.Values
	Body   []byte // nil when the request has no body
}

// Guard is implemented by service.OperatorAccessService.
type Guard interface {
	// CheckTarget returns nil, or an *infraerrors.ApplicationError-compatible error
	// (403 ADMIN_TARGET_FORBIDDEN, 404 when a target id does not exist).
	CheckTarget(ctx context.Context, s *Scope, rule TargetRule, r Request) error
	// CheckFields returns the body to forward (nil means unchanged) or an error
	// (400 ADMIN_FIELD_FORBIDDEN, 400 ACCOUNT_API_KEY_REQUIRED, 403 SUPER_ADMIN_REQUIRED,
	// 403 ADMIN_PERMISSION_DENIED for monitor_create's accounts:view, 403
	// ADMIN_TARGET_FORBIDDEN for redeem_batch_rows' used_by, 503
	// OPERATOR_ACCESS_UNAVAILABLE when a stored value cannot be loaded).
	CheckFields(ctx context.Context, s *Scope, rule FieldRule, r Request) ([]byte, error)
}
