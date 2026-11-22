//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCollabFixSharedByteBoundsAndTreeCapacity(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	shared := (&gatewayCache{rdb: client}).CollabRecords()
	ctx := context.Background()
	scope := resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}
	copies := make([]resilience.CollabDigest, 4096)
	for i := range copies {
		copies[i] = resilience.CollabDigest(strings.Repeat("d", 64))
	}
	m := resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationTerminated, Terminated: resilience.CollabTerminatedRecord{Key: resilience.CollabDeliveryKey{Scope: scope, Thread: "thread", Item: "item", Delivery: "delivery", Digest: "cipher"}, Copies: copies, Event: "event", ExpiresAt: time.Now().Add(time.Hour)}}
	require.NoError(t, shared.Apply(ctx, m))
	rows := client.HVals(ctx, collabCacheKeys(scope)[1]).Val()
	for _, row := range rows {
		require.LessOrEqual(t, len(row), 4096, "shared storage must enforce normalized row bytes")
	}
	require.GreaterOrEqual(t, resilience.CollabRecordMaxTrees, 4096, "thousands of live trees must fit")
}

func TestCollabFixSharedTreeBytesAndNormalizedTTL(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	shared := (&gatewayCache{rdb: client}).CollabRecords()
	ctx := context.Background()
	scope := resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}
	copies := make([]resilience.CollabDigest, resilience.CollabRecordMaxCopies)
	for i := range copies {
		copies[i] = resilience.CollabDigest(strings.Repeat("d", 64))
	}
	m := resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationTerminated, Terminated: resilience.CollabTerminatedRecord{Key: resilience.CollabDeliveryKey{Scope: scope, Thread: "thread", Item: "item", Delivery: "delivery", Digest: "cipher"}, Copies: copies, Event: "event", ExpiresAt: time.Now().Add(2 * resilience.CollabRecordTTL)}}
	// Extraneous fields must be stripped before serializing, even for direct shared calls.
	m.Plaintext.Arguments = resilience.CollabDigest(strings.Repeat("x", 300000))
	for i := 0; i < resilience.CollabRecordMaxTreeEntries; i++ {
		m.Terminated.Key.Item = resilience.CollabDigest(fmt.Sprint(i))
		require.NoError(t, shared.Apply(ctx, m))
	}
	rows := client.HGetAll(ctx, collabCacheKeys(scope)[1]).Val()
	require.NotEmpty(t, rows)
	total := 0
	for field, row := range rows {
		require.LessOrEqual(t, len(row), resilience.CollabRecordMaxRowBytes)
		total += len(row) + 2*len(field) + 32
		var decoded resilience.CollabRecordMutation
		require.NoError(t, json.Unmarshal([]byte(row), &decoded))
		require.Empty(t, decoded.Plaintext)
		require.LessOrEqual(t, time.Until(decoded.Terminated.ExpiresAt), resilience.CollabRecordTTL)
	}
	require.LessOrEqual(t, total, resilience.CollabRecordMaxTreeBytes)
	require.Less(t, len(rows), resilience.CollabRecordMaxTreeEntries, "byte limit must bind before entry count")
	// Expiration frees BOTH capacity limits.
	server.FastForward(resilience.CollabRecordTTL + time.Second)
	m.Terminated.Key.Item = "after-expiry"
	m.Terminated.ExpiresAt = time.Now().Add(time.Hour)
	require.NoError(t, shared.Apply(ctx, m))
	require.Equal(t, int64(1), client.HLen(ctx, collabCacheKeys(scope)[1]).Val())
}

func TestCollabFixThousandsOfSharedTrees(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	shared := (&gatewayCache{rdb: client}).CollabRecords()
	writer, ok := shared.(resilience.CollabSharedRecordsBatchWriter)
	require.True(t, ok)
	ctx := context.Background()
	for start := 0; start < resilience.CollabRecordMaxTrees+1; start += 64 {
		var batch []resilience.CollabRecordMutation
		for i := start; i < min(start+64, resilience.CollabRecordMaxTrees+1); i++ {
			scope := resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: resilience.CollabDigest(fmt.Sprint(i))}
			batch = append(batch, resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationPlaintext, Plaintext: resilience.CollabPlaintextRecord{Scope: scope, Call: "call", Arguments: "args", ExpiresAt: time.Now().Add(time.Hour)}})
		}
		require.NoError(t, writer.ApplyBatch(ctx, batch))
	}
	require.Equal(t, int64(resilience.CollabRecordMaxTrees), client.ZCard(ctx, collabCachePrefix+"trees").Val())
	last := resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: resilience.CollabDigest(fmt.Sprint(resilience.CollabRecordMaxTrees - 1))}
	overflow := last
	overflow.Tree = resilience.CollabDigest(fmt.Sprint(resilience.CollabRecordMaxTrees))
	require.Equal(t, int64(1), client.HLen(ctx, collabCacheKeys(last)[1]).Val())
	require.Zero(t, client.HLen(ctx, collabCacheKeys(overflow)[1]).Val())
}
