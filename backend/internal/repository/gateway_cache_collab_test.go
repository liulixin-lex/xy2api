//go:build unit

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCollabSharedCacheLifecycleAndBounds(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client)
	optional, ok := cache.(service.CollabRecordsCache)
	require.True(t, ok)
	shared := optional.CollabRecords()
	ctx := context.Background()
	scope := resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}
	key := resilience.CollabDeliveryKey{Scope: scope, Thread: "thread", Item: "item", Delivery: "delivery", Digest: "cipher"}
	token := resilience.CollabIssuedToken{Token: "token", Scope: scope, Call: "call", Generation: 1}
	issue := resilience.CollabIssuedRecord{Scope: scope, Call: "call", Digest: key.Digest, Account: "account", Site: "site", Generation: 1, ExpiresAt: time.Now().Add(time.Hour)}
	pending := resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationPending, Issued: issue, Token: token}
	confirm := resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationConfirm, Token: token}
	void := resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationVoid, Token: token}
	q := resilience.CollabRecordQuery{Key: key, Call: "call"}
	b := resilience.CollabRecordBatch{Scope: scope, Queries: []resilience.CollabRecordQuery{q}, Records: true, Dispositions: true}
	require.NoError(t, shared.Apply(ctx, confirm))
	result, err := shared.LookupBatch(ctx, b)
	require.NoError(t, err)
	require.Empty(t, result.Records[q].Issued)
	batch, ok := shared.(resilience.CollabSharedRecordsBatchWriter)
	require.True(t, ok)
	require.NoError(t, batch.ApplyBatch(ctx, []resilience.CollabRecordMutation{
		pending, confirm,
		{Kind: resilience.CollabRecordMutationConsumed, Consumed: resilience.CollabConsumedRecord{Key: key, Account: "consumer", ExpiresAt: issue.ExpiresAt}},
		{Kind: resilience.CollabRecordMutationTerminated, Terminated: resilience.CollabTerminatedRecord{Key: key, Event: "event", Parent: "parent", Copies: []resilience.CollabDigest{"copy"}, ExpiresAt: issue.ExpiresAt}},
		{Kind: resilience.CollabRecordMutationTerminalRejected, TerminalRejection: resilience.CollabTerminalRejectionRecord{Key: key, Rejection: "reject", Event: "terminal-a", ExpiresAt: issue.ExpiresAt}},
		{Kind: resilience.CollabRecordMutationTerminalRejected, TerminalRejection: resilience.CollabTerminalRejectionRecord{Key: key, Rejection: "reject", Event: "terminal-b", ExpiresAt: issue.ExpiresAt}},
		{Kind: resilience.CollabRecordMutationLocalEnding, Scope: scope, Event: "event"},
		{Kind: resilience.CollabRecordMutationLocalEnding, Scope: scope, Event: "event"},
	}))
	result, err = shared.LookupBatch(ctx, b)
	require.NoError(t, err)
	require.Len(t, result.Records[q].Issued, 1)
	require.Equal(t, resilience.CollabIssuedStatusConfirmed, result.Records[q].Issued[0].Status)
	require.Len(t, result.Records[q].Consumed, 1)
	require.Len(t, result.Dispositions[q].Terminated, 1)
	require.Equal(t, 2, result.Dispositions[q].TerminalRejections[0].Count)
	require.Equal(t, 1, result.TreeLocalEndings)
	require.NoError(t, batch.ApplyBatch(ctx, []resilience.CollabRecordMutation{void, pending, confirm}))
	result, err = shared.LookupBatch(ctx, b)
	require.NoError(t, err)
	require.Equal(t, resilience.CollabIssuedStatusVoid, result.Records[q].Issued[0].Status)
	b.Records = false
	result, err = shared.LookupBatch(ctx, b)
	require.NoError(t, err)
	require.Empty(t, result.Records)
	require.Len(t, result.Dispositions[q].Terminated, 1)
	other := b
	other.Scope.Group = "other"
	result, err = shared.LookupBatch(ctx, other)
	require.NoError(t, err)
	require.Empty(t, result)
	// Event-based lookup works even when the parent has no child delivery key.
	notice := resilience.CollabRecordQuery{Key: resilience.CollabDeliveryKey{Scope: scope}, Event: "event"}
	b.Queries = []resilience.CollabRecordQuery{notice}
	result, err = shared.LookupBatch(ctx, b)
	require.NoError(t, err)
	require.Len(t, result.Dispositions[notice].Terminated, 1)
	for i := 0; i < resilience.CollabRecordMaxTreeEntries+10; i++ {
		require.NoError(t, shared.Apply(ctx, resilience.CollabRecordMutation{Kind: resilience.CollabRecordMutationPlaintext, Plaintext: resilience.CollabPlaintextRecord{Scope: scope, Call: resilience.CollabDigest(fmt.Sprint(i)), Arguments: "args", ExpiresAt: time.Now().Add(time.Hour)}}))
	}
	require.Equal(t, int64(resilience.CollabRecordMaxTreeEntries), client.HLen(ctx, collabCacheKeys(scope)[1]).Val())
	server.FastForward(resilience.CollabRecordTTL + time.Second)
	result, err = shared.LookupBatch(ctx, b)
	require.NoError(t, err)
	require.Empty(t, result)
}

func TestCollabSharedCachePendingConfirmVoidTokenIsolation(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer func() { _ = client.Close() }()
	shared := (&gatewayCache{rdb: client}).CollabRecords()
	scope := resilience.CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"}
	issue := resilience.CollabIssuedRecord{Scope: scope, Call: "call", Digest: "cipher", Account: "account", Generation: 1, ExpiresAt: time.Now().Add(time.Hour)}
	a := resilience.CollabIssuedToken{Token: "a", Scope: scope, Call: "call", Generation: 1}
	b := a
	b.Token = "b"
	ctx := context.Background()
	for _, m := range []resilience.CollabRecordMutation{
		{Kind: resilience.CollabRecordMutationPending, Token: a, Issued: issue},
		{Kind: resilience.CollabRecordMutationPending, Token: b, Issued: issue},
		{Kind: resilience.CollabRecordMutationVoid, Token: a},
		{Kind: resilience.CollabRecordMutationConfirm, Token: b},
	} {
		require.NoError(t, shared.Apply(ctx, m))
	}
	q := resilience.CollabRecordQuery{Key: resilience.CollabDeliveryKey{Scope: scope, Digest: "cipher"}}
	for i := 0; i < 10; i++ {
		result, err := shared.LookupBatch(ctx, resilience.CollabRecordBatch{Scope: scope, Queries: []resilience.CollabRecordQuery{q}, Records: true})
		require.NoError(t, err)
		require.Len(t, result.Records[q].Issued, 1)
		require.Equal(t, resilience.CollabIssuedStatusConfirmed, result.Records[q].Issued[0].Status)
	}
}
