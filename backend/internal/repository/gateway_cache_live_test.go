package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheLiveCallIdentityAndController(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache, ok := NewGatewayCache(client).(service.LiveCallStore)
	require.True(t, ok)
	otherInstance, ok := NewGatewayCache(client).(service.LiveCallStore)
	require.True(t, ok)
	record := &service.LiveCallRecord{
		CallID:                "call_secret",
		CallHash:              HashLiveCallID("call_secret"),
		AccountID:             11,
		APIKeyID:              22,
		UserID:                33,
		GroupID:               44,
		LeaseID:               "lease",
		Model:                 "gpt-live-test",
		AttestationCiphertext: "encrypted-attestation",
		CreatedAt:             time.Now(),
		ExpiresAt:             time.Now().Add(time.Hour),
		Controller:            service.LiveControllerPending,
	}
	require.NoError(t, cache.SaveLiveCall(context.Background(), record, time.Hour))

	loaded, err := otherInstance.GetLiveCall(context.Background(), record.CallHash)
	require.NoError(t, err)
	require.Equal(t, record.CallID, loaded.CallID)
	require.Equal(t, record.AccountID, loaded.AccountID)
	require.Equal(t, record.AttestationCiphertext, loaded.AttestationCiphertext)

	claimed, err := cache.ClaimLiveController(context.Background(), record.CallHash, service.LiveControllerObserver, "observer-1")
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = cache.ClaimLiveController(context.Background(), record.CallHash, service.LiveControllerProxy, "proxy-1")
	require.NoError(t, err)
	require.True(t, claimed)
	controller, err := cache.GetLiveController(context.Background(), record.CallHash)
	require.NoError(t, err)
	require.Equal(t, service.LiveControllerProxy, controller)

	released, err := cache.ReleaseLiveController(context.Background(), record.CallHash, "proxy-1")
	require.NoError(t, err)
	require.True(t, released)
	closed, err := cache.MarkLiveCallClosed(context.Background(), record.CallHash, time.Hour)
	require.NoError(t, err)
	require.True(t, closed)
	closed, err = cache.MarkLiveCallClosed(context.Background(), record.CallHash, time.Hour)
	require.NoError(t, err)
	require.False(t, closed)
}

// A live call an availability fallback group serves keeps, through Redis, the
// group it was made for: its sideband belongs to the caller's group whichever
// group serves the sideband request, and its settlement reports the origin
// (review FALLBACK-P1-1).
func TestGatewayCacheLiveCallKeepsTheOriginGroup(t *testing.T) {
	ctx := context.Background()
	redisServer := miniredis.RunT(t)
	gatewayCache := NewGatewayCache(redis.NewClient(&redis.Options{Addr: redisServer.Addr()}))
	store, ok := gatewayCache.(service.LiveCallStore)
	require.True(t, ok)
	record := &service.LiveCallRecord{
		CallID: "call_fallback", CallHash: HashLiveCallID("call_fallback"),
		AccountID: 11, APIKeyID: 77, UserID: 5, GroupID: 20, OriginGroupID: 10,
		LeaseID: "lease", Model: "gpt-live-test",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), Controller: service.LiveControllerPending,
	}
	require.NoError(t, store.SaveLiveCall(ctx, record, time.Hour))
	loaded, err := store.GetLiveCall(ctx, record.CallHash)
	require.NoError(t, err)
	require.Equal(t, int64(20), loaded.GroupID)
	require.Equal(t, int64(10), loaded.OriginGroupID)

	svc := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, gatewayCache, &config.Config{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	caller, serving := int64(10), int64(20)
	_, err = svc.GetLiveCallForIdentity(ctx, record.CallID, service.LiveCallIdentity{APIKeyID: 77, UserID: 5, GroupID: &caller})
	require.NoError(t, err, "the caller's group owns the call")
	_, err = svc.GetLiveCallForIdentity(ctx, record.CallID, service.LiveCallIdentity{APIKeyID: 77, UserID: 5, GroupID: &serving})
	require.ErrorIs(t, err, service.ErrLiveIdentityMismatch)

	// A call saved before the upgrade has no origin and belongs to its group.
	legacy := *record
	legacy.CallID, legacy.CallHash, legacy.GroupID, legacy.OriginGroupID = "call_legacy", HashLiveCallID("call_legacy"), 10, 0
	require.NoError(t, store.SaveLiveCall(ctx, &legacy, time.Hour))
	_, err = svc.GetLiveCallForIdentity(ctx, legacy.CallID, service.LiveCallIdentity{APIKeyID: 77, UserID: 5, GroupID: &caller})
	require.NoError(t, err)
}
