package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

// gatewaySessionBindingReader reads a binding and its remaining lifetime in
// one round trip. Every hit refreshes the binding to its full TTL, so the
// session's idle time is TTL minus the remaining lifetime.
type gatewaySessionBindingReader interface {
	GetSessionAccountIDWithTTL(ctx context.Context, groupID int64, sessionHash string) (int64, time.Duration, error)
}

// gatewaySessionBindingCreator creates a binding only when none exists, so
// two instances placing the same new session keep the first placement.
type gatewaySessionBindingCreator interface {
	SetSessionAccountIDIfAbsent(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) (bool, error)
}

// readShareBinding returns the session's bound account and idle time. Caches
// without TTL support report zero idle time, which only delays migrations.
func readShareBinding(ctx context.Context, cache GatewayCache, groupID int64, key string, ttl time.Duration) (sharesched.Binding, error) {
	if cache == nil || key == "" {
		return sharesched.Binding{}, nil
	}
	if reader, ok := cache.(gatewaySessionBindingReader); ok {
		accountID, remaining, err := reader.GetSessionAccountIDWithTTL(ctx, groupID, key)
		if errors.Is(err, ErrStickySessionNotFound) {
			return sharesched.Binding{}, nil
		}
		if err != nil {
			return sharesched.Binding{}, err
		}
		idle := time.Duration(0)
		if remaining > 0 && remaining < ttl {
			idle = ttl - remaining
		}
		return sharesched.Binding{AccountID: accountID, Idle: idle}, nil
	}
	accountID, err := cache.GetSessionAccountID(ctx, groupID, key)
	if errors.Is(err, ErrStickySessionNotFound) {
		return sharesched.Binding{}, nil
	}
	if err != nil {
		return sharesched.Binding{}, err
	}
	return sharesched.Binding{AccountID: accountID}, nil
}

// applyShareBindingWrite performs a plan's binding update.
func applyShareBindingWrite(ctx context.Context, cache GatewayCache, groupID int64, key string, accountID int64, write sharesched.Write, ttl time.Duration) error {
	if cache == nil || key == "" || accountID <= 0 {
		return nil
	}
	switch write {
	case sharesched.WriteRefresh:
		return cache.RefreshSessionTTL(ctx, groupID, key, ttl)
	case sharesched.WriteIfAbsent:
		if creator, ok := cache.(gatewaySessionBindingCreator); ok {
			_, err := creator.SetSessionAccountIDIfAbsent(ctx, groupID, key, accountID, ttl)
			return err
		}
		return cache.SetSessionAccountID(ctx, groupID, key, accountID, ttl)
	case sharesched.WriteReplace:
		return cache.SetSessionAccountID(ctx, groupID, key, accountID, ttl)
	default:
		return nil
	}
}

// shareBindingIntent is the binding update of a share placement, carried to
// the handler. It is pending while the request still has to wait for a slot
// or pass the profit gate's final check; once done, the handler's admission
// bind is a no-op so it cannot overwrite a binding the plan chose to keep.
type shareBindingIntent struct {
	cache   GatewayCache
	groupID int64
	key     string
	write   sharesched.Write
	ttl     time.Duration
	done    bool
}

type shareBindingIntentKey struct{}

func contextWithShareBindingIntent(ctx context.Context, intent *shareBindingIntent) context.Context {
	if intent == nil {
		return ctx
	}
	return context.WithValue(ctx, shareBindingIntentKey{}, intent)
}

// applyDeferredShareBinding applies the intent carried by the selection
// instead of the original admission binding. It reports false when the
// request was not placed by share scheduling.
func applyDeferredShareBinding(ctx context.Context, groupID *int64, key string, accountID int64) (bool, error) {
	intent, _ := ctx.Value(shareBindingIntentKey{}).(*shareBindingIntent)
	if intent == nil || intent.key != key || intent.groupID != derefGroupID(groupID) {
		return false, nil
	}
	if intent.done {
		return true, nil
	}
	return true, applyShareBindingWrite(ctx, intent.cache, intent.groupID, intent.key, accountID, intent.write, intent.ttl)
}

// shareBindingTTL is the binding lifetime of a share-scheduled group.
func (s *ShareSchedulingService) shareBindingTTL(groupID int64) (time.Duration, bool) {
	if s == nil {
		return 0, false
	}
	if !s.shareSchedulingActive(groupID) {
		return 0, false
	}
	return sharesched.BindingTTL, true
}

// shareProtectedBindingPrefixes are keys stored next to session bindings
// that carry protocol continuation or ownership, not placement. A rebalance
// must never delete them.
var shareProtectedBindingPrefixes = []string{
	openAIWSResponseAccountCachePrefix,
	openAIHTTPResponseOwnerUserPrefix,
	openAIHTTPResponseOwnerKeyPrefix,
	"openai:grok-video:",
}

// IsShareRebalanceableBinding reports whether a session binding key (the
// part after the group id) is a placement that a rebalance may clear.
func IsShareRebalanceableBinding(sessionKey string) bool {
	for _, prefix := range shareProtectedBindingPrefixes {
		if strings.HasPrefix(sessionKey, prefix) {
			return false
		}
	}
	return sessionKey != ""
}
