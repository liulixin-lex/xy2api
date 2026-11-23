package service

import (
	"context"
	"errors"
	"reflect"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// The constructor is a fault-injection boundary; ordinary requests only read
// the already constructed, gateway-owned store.
var newCollabAffinityStore = resilience.NewCollabTreeAffinity

// CollabContinuationCache is optional. Shared state providers can supply their
// alias adapter without widening GatewayCache or using local aliases by mistake.
type CollabContinuationCache interface {
	CollabContinuations() CollabContinuationStore
}

func collabCapabilityNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Func, reflect.Chan, reflect.Slice:
		return v.IsNil()
	}
	return false
}

func (s *OpenAIGatewayService) initCollabContinuation() {
	cache, ok := s.cache.(CollabContinuationCache)
	if !ok {
		return
	}
	installed := false
	defer func() {
		_ = recover()
		if !installed {
			s.SetCollabContinuationStore(unavailableCollabContinuation{})
			resilience.Count("collab_construction_failed", 0, 0, "continuation")
		}
	}()
	store := cache.CollabContinuations()
	if collabCapabilityNil(store) {
		return
	}
	s.SetCollabContinuationStore(store)
	installed = true
}

type unavailableCollabContinuation struct{}

func (unavailableCollabContinuation) Ready() bool { return false }
func (unavailableCollabContinuation) Put(context.Context, CollabContinuationScope, string, CollabContinuationAlias) error {
	return errors.New("continuation store unavailable")
}
func (unavailableCollabContinuation) Get(context.Context, CollabContinuationScope, string) (CollabContinuationAlias, bool, error) {
	return CollabContinuationAlias{}, false, nil
}
