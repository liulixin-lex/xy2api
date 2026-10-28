package operatoraccess

import "strings"

type Decision struct {
	Reason   string
	Required string
	AnyOf    string
}

// Decide evaluates a catalog entry for an operator. Unknown kinds fail closed.
func Decide(e Entry, found bool, p Permissions) Decision {
	if !found {
		return Decision{Reason: "ADMIN_PERMISSION_DENIED", Required: "catalog"}
	}
	switch e.Kind {
	case KindSuperOnly:
		return Decision{Reason: "SUPER_ADMIN_REQUIRED"}
	case KindAnyAdmin:
		return Decision{}
	case KindModule:
	default:
		return Decision{Reason: "ADMIN_PERMISSION_DENIED", Required: "catalog"}
	}
	allowed := p.Can(e.Module, e.Level)
	anyOf := []string{}
	for _, m := range e.AnyOf {
		anyOf = append(anyOf, string(m)+":view")
		if e.Level == LevelView && p.Can(m, LevelView) {
			allowed = true
		}
	}
	if !allowed {
		return Decision{Reason: "ADMIN_PERMISSION_DENIED", Required: string(e.Module) + ":" + e.Level.String(), AnyOf: strings.Join(anyOf, ",")}
	}
	for _, f := range e.Flags {
		if !p.Has(f) {
			return Decision{Reason: "ADMIN_PERMISSION_DENIED", Required: string(f)}
		}
	}
	return Decision{}
}
