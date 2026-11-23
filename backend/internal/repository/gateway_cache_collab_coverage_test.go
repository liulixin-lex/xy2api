//go:build unit

package repository

import (
	"context"
	"encoding/json"
	r "github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestCollabGeneratedRepeatedWholeTreeHorizon(t *testing.T) {
	server, client, shared := capacityCache(t)
	memory := r.NewMemoryCollabRecordsStore()
	ctx := context.Background()
	k := capacityKey(0)
	short := time.Now().Add(150 * time.Millisecond)
	long := time.Now().Add(time.Hour)
	marker := r.CollabRecordMutation{Kind: r.CollabRecordMutationVoid, Token: r.CollabIssuedToken{Scope: k.Scope}, Issued: r.CollabIssuedRecord{ExpiresAt: short}}
	require.True(t, memory.ApplyMutation(marker))
	require.NoError(t, shared.Apply(ctx, marker))
	marker.Issued.ExpiresAt = long
	require.True(t, memory.ApplyMutation(marker))
	require.NoError(t, shared.WriteInvalidations(ctx, []r.CollabRecordMutation{marker}))
	raw := client.HGet(ctx, collabCacheKeys(k.Scope)[1], "v:").Val()
	var stored r.CollabRecordMutation
	require.NoError(t, json.Unmarshal([]byte(raw), &stored))
	t.Logf("second whole-scope invalidation accepted; requested horizon=%s stored horizon=%s ttl=%s", time.Until(long), time.Until(stored.Issued.ExpiresAt), client.PTTL(ctx, collabCacheKeys(k.Scope)[1]).Val())
	if stored.Issued.ExpiresAt.Before(long) {
		t.Error("acknowledged whole-tree update discarded its extended coverage")
	}
	time.Sleep(180 * time.Millisecond)
	server.FastForward(180 * time.Millisecond)
	tok := r.CollabIssuedToken{Scope: k.Scope, Call: capacityDigest(10), Token: capacityDigest(11)}
	issue := r.CollabRecordMutation{Kind: r.CollabRecordMutationPending, Token: tok, Issued: r.CollabIssuedRecord{Scope: k.Scope, Call: tok.Call, Digest: k.Digest, Account: capacityDigest(7), ExpiresAt: long}}
	require.True(t, memory.ApplyMutation(issue))
	require.NoError(t, shared.Apply(ctx, issue))
	localConfirm := memory.ConfirmIssued(ctx, tok)
	remoteConfirm := shared.Apply(ctx, r.CollabRecordMutation{Kind: r.CollabRecordMutationConfirm, Token: tok}) == nil
	q := r.CollabRecordQuery{Key: k}
	b := r.CollabRecordBatch{Scope: k.Scope, Queries: []r.CollabRecordQuery{q}, Records: true}
	a := memory.LookupBatch(ctx, b)
	z, err := shared.LookupBatch(ctx, b)
	require.NoError(t, err)
	t.Logf("during acknowledged long coverage: localConfirm=%t sharedConfirm=%t localComplete=%t sharedComplete=%t localIssues=%v sharedIssues=%v", localConfirm, remoteConfirm, a.RecordsComplete, z.RecordsComplete, a.Records[q].Issued, z.Records[q].Issued)
	require.False(t, localConfirm || remoteConfirm, "a delayed observation may not become confirmed during acknowledged invalidation coverage")
}

func TestCollabGeneratedPartialWriteRetainsLongRowTTL(t *testing.T) {
	_, client, shared := capacityCache(t)
	ctx := context.Background()
	k := capacityKey(0)
	initial := r.CollabRecordMutation{Kind: r.CollabRecordMutationPlaintext, Plaintext: r.CollabPlaintextRecord{Scope: k.Scope, Call: capacityDigest(10), Arguments: capacityDigest(11), ExpiresAt: time.Now().Add(time.Minute)}}
	require.NoError(t, shared.Apply(ctx, initial))
	now := time.Now()
	long := now.Add(time.Hour)
	m := r.CollabRecordMutation{Kind: r.CollabRecordMutationPlaintext, Plaintext: r.CollabPlaintextRecord{Scope: k.Scope, Call: capacityDigest(12), Arguments: capacityDigest(13), ExpiresAt: long}}
	m, payload, ok := r.NormalizeCollabRecordMutation(m, now)
	require.True(t, ok)
	scope, field, expiry := r.CollabRecordStorageIdentity(m)
	source := strings.Replace(collabApplySource, "local current=tonumber", "if true then return redis.error_reply('injected interruption') end\nlocal current=tonumber", 1)
	err := client.Eval(ctx, source, collabCacheKeys(scope), collabApplyArgs(now, expiry, field, m.Kind, payload, "")...).Err()
	require.Error(t, err)
	ttl := client.PTTL(ctx, collabCacheKeys(scope)[1]).Val()
	t.Logf("partial long write into live short hash: rows=%d TTL=%s new row remaining=%s err=%v", client.HLen(ctx, collabCacheKeys(scope)[1]).Val(), ttl, time.Until(long), err)
	require.True(t, client.HExists(ctx, collabCacheKeys(scope)[1], field).Val())
	require.GreaterOrEqual(t, ttl, time.Until(long)-20*time.Millisecond, "partial-script guard must cover every row it leaves")
}

func TestCollabGeneratedInvalidationUpdateMetadata(t *testing.T) {
	for _, whole := range []bool{false, true} {
		_, client, shared := capacityCache(t)
		ctx := context.Background()
		key := capacityKey(0)
		token := r.CollabIssuedToken{Scope: key.Scope, Call: capacityDigest(10), Token: capacityDigest(11)}
		if whole {
			token = r.CollabIssuedToken{Scope: key.Scope}
		}
		long := time.Now().UTC().Add(time.Hour)
		for _, expiry := range []time.Time{time.Now().Add(time.Minute), long, long.Add(-time.Nanosecond), time.Now().Add(time.Second)} {
			m := r.CollabRecordMutation{Kind: r.CollabRecordMutationVoid, Token: token, Issued: r.CollabIssuedRecord{ExpiresAt: expiry}}
			require.NoError(t, shared.WriteInvalidations(ctx, []r.CollabRecordMutation{m}))
		}
		keys := collabCacheKeys(key.Scope)
		rows := invariantRows(t, shared, key.Scope)
		require.True(t, rows["v:"+string(token.Token)].Issued.ExpiresAt.Equal(long))
		require.GreaterOrEqual(t, client.ZScore(ctx, keys[2], "v:"+string(token.Token)).Val(), float64(long.UnixMilli()))
		protection, err := client.Get(ctx, keys[6]).Int64()
		require.NoError(t, err)
		require.GreaterOrEqual(t, protection, long.UnixMilli())
		for _, index := range []int{0, 3, 4, 5} {
			require.GreaterOrEqual(t, client.ZScore(ctx, keys[index], keys[1]).Val(), float64(long.UnixMilli()))
		}
	}
}
