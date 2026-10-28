package operatoraccess

import (
	"context"
	"strings"
	"testing"
)

type ActorKind uint8

const (
	ActorNone ActorKind = iota
	ActorSuperAdmin
	ActorOperator
	ActorSystem
)

type Actor struct {
	Kind   ActorKind
	UserID int64
	System string
}
type actorKey struct{}

func WithSuperAdmin(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, actorKey{}, Actor{Kind: ActorSuperAdmin, UserID: userID})
}

// WithSystemActor names trusted background work. Empty names never confer trust.
func WithSystemActor(ctx context.Context, name string) context.Context {
	if strings.TrimSpace(name) == "" {
		if testing.Testing() {
			panic("operatoraccess: empty system actor")
		}
		return ctx
	}
	return context.WithValue(ctx, actorKey{}, Actor{Kind: ActorSystem, System: name})
}
func ActorFrom(ctx context.Context) Actor {
	if s := ScopeFrom(ctx); s != nil {
		return Actor{Kind: ActorOperator, UserID: s.UserID}
	}
	a, _ := ctx.Value(actorKey{}).(Actor)
	return a
}
