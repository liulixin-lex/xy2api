//go:build unit

package repository

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompensationStoreFreezesOnceAndExpires(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := &resilienceStore{rdb: client}
	ctx := context.Background()
	at := time.Now().UTC()
	ref := service.OpenAICompensationReference{CacheRead: 80, Total: 100, At: at}
	require.NoError(t, store.SetSessionCacheRatio(ctx, "fixture", .3, time.Hour))
	require.NoError(t, store.SetCompensationReference(ctx, "fixture", ref))
	require.NoError(t, store.FreezeCompensationAction(ctx, "fixture", service.OpenAICompensationAction{Kind: "migrate", From: 1, To: 2, At: at}))
	cold := ref
	cold.CacheRead = 0
	cold.At = at.Add(time.Second)
	require.NoError(t, store.SetCompensationReference(ctx, "fixture", cold))
	require.NoError(t, store.FreezeCompensationAction(ctx, "fixture", service.OpenAICompensationAction{Kind: "takeover", From: 2, To: 3, At: cold.At}))
	action, err := store.TakeCompensationAction(ctx, "fixture", "next", 2)
	require.NoError(t, err)
	require.Equal(t, "migrate", action.Kind)
	require.Equal(t, ref, *action.Reference)
	action, err = store.TakeCompensationAction(ctx, "fixture", "next", 2)
	require.NoError(t, err)
	require.Nil(t, action)
	ratio, ok, err := store.SessionCacheRatio(ctx, "fixture")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, .3, ratio)
	// A late old settlement cannot replace a newer cold reference.
	require.NoError(t, store.SetCompensationReference(ctx, "fixture", ref))
	require.NoError(t, store.FreezeCompensationAction(ctx, "fixture", service.OpenAICompensationAction{Kind: "migrate", From: 1, To: 2, At: cold.At}))
	action, err = store.TakeCompensationAction(ctx, "fixture", "next", 2)
	require.NoError(t, err)
	require.Equal(t, 0, action.Reference.CacheRead)
	mr.FastForward(10 * time.Minute)
	require.NoError(t, store.FreezeCompensationAction(ctx, "fixture", service.OpenAICompensationAction{Kind: "migrate", From: 1, To: 2, At: at}))
	action, err = store.TakeCompensationAction(ctx, "fixture", "next", 2)
	require.NoError(t, err)
	require.Nil(t, action.Reference)
	require.NoError(t, store.FreezeCompensationAction(ctx, "fixture", service.OpenAICompensationAction{Kind: "migrate", From: 1, To: 2, At: at}))
	mr.FastForward(time.Hour)
	action, err = store.TakeCompensationAction(ctx, "fixture", "next", 2)
	require.NoError(t, err)
	require.Nil(t, action)
}

func TestCompensationStoreConcurrentConsumption(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := &resilienceStore{rdb: client}
	ctx := context.Background()
	require.NoError(t, store.FreezeCompensationAction(ctx, "fixture", service.OpenAICompensationAction{Kind: "owner_released", From: 1, At: time.Now()}))
	var wg sync.WaitGroup
	var claims atomic.Int32
	for range 16 {
		wg.Go(func() {
			a, e := store.TakeCompensationAction(ctx, "fixture", "next", 2)
			if e != nil {
				t.Error(e)
			}
			if a != nil {
				claims.Add(1)
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, 1, claims.Load())
}

func TestCompensationStorePostOutputTriggerDoesNotConsumeNextBill(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := &resilienceStore{rdb: client}
	ctx := context.Background()
	require.NoError(t, store.FreezeCompensationAction(ctx, "fixture", service.OpenAICompensationAction{RequestID: "trigger", Kind: "owner_released", From: 1, At: time.Now()}))
	action, err := store.TakeCompensationAction(ctx, "fixture", "trigger", 1)
	require.NoError(t, err)
	require.Nil(t, action)
	action, err = store.TakeCompensationAction(ctx, "fixture", "next", 2)
	require.NoError(t, err)
	require.NotNil(t, action)
}
