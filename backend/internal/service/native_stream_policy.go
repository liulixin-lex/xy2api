package service

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// NativeStreamPolicy is immutable for a logical response. Disabling admission
// leaves existing executions and their readable journals on their own snapshot.
type NativeStreamPolicy struct {
	Version     int64
	Delivery    bool
	Recovery    bool
	Persistence bool
}
type nativeStreamPolicyKey struct{}
type nativeStreamPolicyReaderKey struct{}
type nativeStreamPolicyReader struct {
	read    func(context.Context, int64) (NativeStreamPolicy, error)
	groupID int64
}

// RefreshNativeStreamPolicy is called only at the admission of a new logical
// response on a long-lived connection. An existing turn keeps its old context.
// Authentication, deadline and control-plane cancellation remain on the parent.
func RefreshNativeStreamPolicy(ctx context.Context) (context.Context, error) {
	source, ok := ctx.Value(nativeStreamPolicyReaderKey{}).(nativeStreamPolicyReader)
	if !ok || source.read == nil {
		return ctx, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	policy, err := source.read(readCtx, source.groupID)
	if err != nil {
		return ctx, err
	}
	return WithNativeStreamPolicy(ctx, policy), nil
}

func WithNativeStreamPolicy(ctx context.Context, p NativeStreamPolicy) context.Context {
	return context.WithValue(ctx, nativeStreamPolicyKey{}, p)
}
func NativeStreamPolicyFromContext(ctx context.Context) NativeStreamPolicy {
	if ctx == nil {
		return NativeStreamPolicy{}
	}
	p, _ := ctx.Value(nativeStreamPolicyKey{}).(NativeStreamPolicy)
	return p
}
func NativeStreamDeliveryEnabled(ctx context.Context) bool {
	return NativeStreamPolicyFromContext(ctx).Delivery
}

// NativeStreamFirstAnswerRecoveryEnabled keeps the recovery contract explicit:
// delivery-only policies retain their established wire behaviour, while a
// recovery policy may withhold a replay-safe pre-answer preamble.
func NativeStreamFirstAnswerRecoveryEnabled(ctx context.Context) bool {
	p := NativeStreamPolicyFromContext(ctx)
	if !p.Delivery || !p.Recovery {
		return false
	}
	r := controlledRequest(ctx)
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ReplaySafe && !r.owner && r.ownerAccountID == 0 && !r.attemptCommitted && !r.localFailureStarted
}

// Install after authentication, before allocating or parsing a generation.
func NativeStreamPolicyMiddleware(reader func(context.Context, int64) (NativeStreamPolicy, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		if reader == nil {
			c.Next()
			return
		}
		key, exists := c.Get("api_key")
		apiKey, ok := key.(*APIKey)
		if !exists || !ok || apiKey == nil {
			c.Next()
			return
		}
		groupID := int64(0)
		if apiKey.GroupID != nil {
			groupID = *apiKey.GroupID
		}
		base := context.WithValue(c.Request.Context(), nativeStreamPolicyReaderKey{}, nativeStreamPolicyReader{read: reader, groupID: groupID})
		ctx, err := RefreshNativeStreamPolicy(base)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "service_unavailable", "message": "Native stream policy is temporarily unavailable"}})
			return
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
