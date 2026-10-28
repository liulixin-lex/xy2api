package service

import (
	"context"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
)

// operatorScope requires an explicit actor once production wiring arms this instance.
func (s *adminServiceImpl) operatorScope(ctx context.Context) (*operatoraccess.Scope, *OperatorAccessService, error) {
	sc := operatoraccess.ScopeFrom(ctx)
	if sc != nil {
		svc, ok := sc.Guard.(*OperatorAccessService)
		if !ok || svc == nil {
			return nil, nil, operatorUnavailable()
		}
		return sc, svc, nil
	}
	actor := operatoraccess.ActorFrom(ctx)
	if s.requireActor && actor.Kind != operatoraccess.ActorSuperAdmin && actor.Kind != operatoraccess.ActorSystem {
		return nil, nil, infraerrors.ServiceUnavailable("ADMIN_ACTOR_MISSING", "Admin actor is required")
	}
	return nil, nil, nil
}
func (s *adminServiceImpl) operatorSuperAdminOnly(ctx context.Context) error {
	sc, _, err := s.operatorScope(ctx)
	if err != nil {
		return err
	}
	if sc != nil {
		return operatorSuperOnly()
	}
	return nil
}
