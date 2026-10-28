package repository

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestUpstreamBalanceStoreRoundTripAndKeyNames(t *testing.T) {
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewUpstreamBalanceStore(r)
	n := 1.2
	rec := &service.UpstreamBalanceRecord{Version: 1, Fingerprint: "fp", Kind: "sub2api", AmountUSD: &n, ObservedAt: time.Now().UTC()}
	st := &service.UpstreamBalanceState{Version: 1, Fingerprint: "fp", LastAttemptAt: time.Now().UTC()}
	require.NoError(t, s.SaveSuccess(context.Background(), 7, rec, st))
	e, err := s.Load(context.Background(), []int64{7})
	require.NoError(t, err)
	require.Equal(t, "sub2api", e.Records[7].Kind)
	require.NotEmpty(t, mr.Keys())
}

func TestUpstreamBalanceStoreFailureDoesNotExtendSuccessTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewUpstreamBalanceStore(r)
	n := 1.2
	rec := &service.UpstreamBalanceRecord{Version: 1, Fingerprint: "fp", AmountUSD: &n, ObservedAt: time.Now().UTC()}
	st := &service.UpstreamBalanceState{Version: 1, Fingerprint: "fp"}
	require.NoError(t, s.SaveSuccess(context.Background(), 7, rec, st))
	mr.FastForward(10 * time.Hour)
	require.NoError(t, s.SaveAttempt(context.Background(), 7, st))
	require.InDelta(t, float64(14*time.Hour), float64(mr.TTL(ubKey(7))), float64(time.Minute))
}

func TestUpstreamBalanceStoreTTLs(t *testing.T) {
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewUpstreamBalanceStore(r)
	n := 1.0
	rec := &service.UpstreamBalanceRecord{Version: 1, Fingerprint: "fp", AmountUSD: &n, ObservedAt: time.Now().UTC()}
	st := &service.UpstreamBalanceState{Version: 1, Fingerprint: "fp"}
	require.NoError(t, s.SaveSuccess(context.Background(), 9, rec, st))
	require.InDelta(t, float64(24*time.Hour), float64(mr.TTL(ubKey(9))), float64(time.Second))
	require.InDelta(t, float64(48*time.Hour), float64(mr.TTL(ubStateKey(9))), float64(time.Second))
}

func TestUpstreamBalanceStoreMGETChunking(t *testing.T) {
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	hook := &cyberRedisCommandHook{}
	r.AddHook(hook)
	s := NewUpstreamBalanceStore(r)
	ids := make([]int64, 668)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	_, err := s.Load(context.Background(), ids)
	require.NoError(t, err)
	require.Equal(t, []int{999, 999, 6}, hook.mgetKeyCounts)
}

func TestUpstreamBalanceStoreSkipsUndecodableValues(t *testing.T) {
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewUpstreamBalanceStore(r)
	require.NoError(t, r.Set(context.Background(), ubStateKey(5), "{bad", time.Hour).Err())
	e, err := s.Load(context.Background(), []int64{5})
	require.NoError(t, err)
	require.NotContains(t, e.States, int64(5))
}

func TestUpstreamBalanceStoreOriginLockIsSetNX75s(t *testing.T) {
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewUpstreamBalanceStore(r)
	got, err := s.TryLockOrigin(context.Background(), "https://example.test", "owner", 75*time.Second)
	require.NoError(t, err)
	require.True(t, got)
	require.Equal(t, 75*time.Second, mr.TTL(ubCTKey("https://example.test")))
	got, err = s.TryLockOrigin(context.Background(), "https://example.test", "another-owner", 75*time.Second)
	require.NoError(t, err)
	require.False(t, got)
}

func TestUpstreamBalanceStoreCooldownExtendsOnlyLater(t *testing.T) {
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewUpstreamBalanceStore(r)
	first := time.Now().UTC().Add(20 * time.Minute)
	later := first.Add(10 * time.Minute)
	require.NoError(t, s.SaveCooldown(context.Background(), "https://example.test", first))
	require.NoError(t, s.SaveCooldown(context.Background(), "https://example.test", first.Add(-time.Minute)))
	got, err := s.GetCooldown(context.Background(), "https://example.test")
	require.NoError(t, err)
	require.WithinDuration(t, first, *got, time.Second)
	require.NoError(t, s.SaveCooldown(context.Background(), "https://example.test", later))
	got, err = s.GetCooldown(context.Background(), "https://example.test")
	require.NoError(t, err)
	require.WithinDuration(t, later, *got, time.Second)
}

func TestUpstreamBalanceStoreRedisDownReturnsError(t *testing.T) {
	mr := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewUpstreamBalanceStore(r)
	mr.Close()
	_, err := s.Load(context.Background(), []int64{1})
	require.Error(t, err)
}
