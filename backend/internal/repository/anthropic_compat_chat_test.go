package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func chatRedisFixture(t *testing.T) (*gatewayCache, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, ok := NewGatewayCache(client).(*gatewayCache)
	require.True(t, ok)
	return cache, server
}

func chatTestKey(kind, label string) string {
	digest := sha256.Sum256([]byte(label))
	return service.AnthropicCompatChatKeyPrefix + kind + ":" + hex.EncodeToString(digest[:])
}

func TestAnthropicCompatChatRedisAliasesTTLAndImmutableRegistration(t *testing.T) {
	ctx := context.Background()
	cache, server := chatRedisFixture(t)
	turn, handles := chatTestKey("t", "one"), []string{chatTestKey("h", "one"), chatTestKey("h", "two")}
	value := []byte("encrypted assistant turn")
	require.NoError(t, cache.RegisterChat(ctx, turn, handles, value, time.Hour))
	out, err := cache.LookupChat(ctx, handles)
	require.NoError(t, err)
	require.Equal(t, value, out[0])
	require.Equal(t, value, out[1])
	require.True(t, &out[0][0] == &out[1][0], "parallel handles share one immutable ciphertext allocation")
	require.ErrorIs(t, cache.RegisterChat(ctx, turn, []string{handles[0], chatTestKey("h", "new")}, value, time.Hour), service.AnthropicCompatRegistrationConflict)
	require.False(t, server.Exists(chatTestKey("h", "new")))
	child, childAlias := chatTestKey("t", "child"), chatTestKey("c", "child")
	require.NoError(t, cache.RegisterChat(ctx, child, []string{childAlias}, []byte("encrypted child"), time.Minute))
	out, err = cache.LookupChat(ctx, []string{childAlias})
	require.NoError(t, err)
	require.Equal(t, []byte("encrypted child"), out[0])
	server.FastForward(30 * time.Minute)
	require.NoError(t, cache.RegisterChat(ctx, turn, handles, value, time.Hour))
	server.FastForward(40 * time.Minute)
	out, err = cache.LookupChat(ctx, []string{handles[0], childAlias})
	require.NoError(t, err)
	require.Equal(t, value, out[0])
	require.Nil(t, out[1])
	server.FastForward(61 * time.Minute)
	out, err = cache.LookupChat(ctx, handles)
	require.NoError(t, err)
	require.Nil(t, out[0])
	require.Nil(t, out[1])
}

func TestAnthropicCompatChatRedisWrongTypeAndCapacityAreAtomic(t *testing.T) {
	for _, badKey := range []string{anthropicCompatChatIndex, anthropicCompatChatSizes, anthropicCompatChatBytes} {
		t.Run(badKey, func(t *testing.T) {
			cache, server := chatRedisFixture(t)
			ctx := context.Background()
			if badKey == anthropicCompatChatBytes {
				require.NoError(t, cache.rdb.HSet(ctx, badKey, "x", "y").Err())
			} else {
				require.NoError(t, server.Set(badKey, "wrong"))
			}
			before := server.Dump()
			err := cache.RegisterChat(ctx, chatTestKey("t", "turn"), []string{chatTestKey("h", "handle")}, []byte("cipher"), time.Hour)
			require.Error(t, err)
			require.Equal(t, before, server.Dump())
		})
	}
	cache, server := chatRedisFixture(t)
	ctx := context.Background()
	turn, handle := chatTestKey("t", "turn"), chatTestKey("h", "handle")
	require.NoError(t, cache.RegisterChat(ctx, turn, []string{handle}, []byte("cipher"), time.Hour))
	before := server.Dump()
	err := registerAnthropicCompatChat.Run(ctx, cache.rdb, []string{anthropicCompatChatIndex, anthropicCompatChatSizes, anthropicCompatChatBytes, chatTestKey("t", "second"), chatTestKey("h", "second")}, "cipher", 60000, 1, 1000000, 100, "digest").Err()
	require.ErrorContains(t, err, "capacity")
	require.Equal(t, before, server.Dump())
	err = registerAnthropicCompatChat.Run(ctx, cache.rdb, []string{anthropicCompatChatIndex, anthropicCompatChatSizes, anthropicCompatChatBytes, chatTestKey("t", "second"), chatTestKey("h", "second")}, "cipher", 60000, 100, 1, 100, "digest").Err()
	require.ErrorContains(t, err, "capacity")
	require.Equal(t, before, server.Dump())
	require.ErrorIs(t, cache.RegisterChat(ctx, chatTestKey("t", "overflow"), []string{chatTestKey("h", "overflow")}, []byte(strings.Repeat("x", service.AnthropicCompatChatRecordLimit+1)), time.Hour), service.AnthropicCompatRegistrationInvalid)
}
