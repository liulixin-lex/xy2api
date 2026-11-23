//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"

	r "github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func invariantExpiry(m r.CollabRecordMutation) time.Time {
	switch m.Kind {
	case r.CollabRecordMutationPending, r.CollabRecordMutationVoid, r.CollabRecordMutationLocalEnding:
		return m.Issued.ExpiresAt
	case r.CollabRecordMutationPlaintext:
		return m.Plaintext.ExpiresAt
	case r.CollabRecordMutationConsumed:
		return m.Consumed.ExpiresAt
	case r.CollabRecordMutationReplaced:
		return m.Replaced.ExpiresAt
	case r.CollabRecordMutationTerminated:
		return m.Terminated.ExpiresAt
	case r.CollabRecordMutationTerminalRejected:
		return m.TerminalRejection.ExpiresAt
	}
	return time.Time{}
}
func invariantPartition(kind r.CollabRecordMutationKind) int {
	switch kind {
	case r.CollabRecordMutationPending, r.CollabRecordMutationVoid, r.CollabRecordMutationPlaintext, r.CollabRecordMutationConsumed:
		return 0
	case r.CollabRecordMutationLocalEnding:
		return 2
	}
	return 1
}
func invariantRows(t *testing.T, s *collabSharedCache, scope r.CollabRecordScope) map[string]r.CollabRecordMutation {
	t.Helper()
	ctx := context.Background()
	keys := collabCacheKeys(scope)
	raw, err := s.rdb.HGetAll(ctx, keys[1]).Result()
	require.NoError(t, err)
	rows := make(map[string]r.CollabRecordMutation)
	counts := [3]int{}
	bytes := [3]int{}
	for field, value := range raw {
		var m r.CollabRecordMutation
		require.NoError(t, json.Unmarshal([]byte(value), &m))
		part := invariantPartition(m.Kind)
		counts[part]++
		bytes[part] += len(value) + 3*len(field) + 32
		rows[field] = m
	}
	for part, limit := range []int{r.CollabRecordEvidenceBytes, r.CollabRecordDispositionBytes, r.CollabRecordCounterBytes} {
		require.LessOrEqual(t, bytes[part], limit, "partition=%d counts=%v bytes=%v", part, counts, bytes)
		rowLimit := 64
		if part == 2 {
			rowLimit = 3
		}
		require.LessOrEqual(t, counts[part], rowLimit)
	}
	if len(rows) > 0 {
		ttl := s.rdb.PTTL(ctx, keys[1]).Val()
		require.Positive(t, ttl, "live rows need a hash TTL")
		for _, m := range rows {
			require.GreaterOrEqual(t, ttl, time.Until(invariantExpiry(m))-20*time.Millisecond, "hash TTL shortened a surviving row")
		}
	}
	return rows
}
func TestCollabGeneratedSharedInvalidationBounds(t *testing.T) {
	for _, seed := range []int64{7, 29, 101} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			_, _, s := capacityCache(t)
			ctx := context.Background()
			rng := rand.New(rand.NewSource(seed))
			k := capacityKey(0)
			for i := 0; i < 60; i++ {
				token := r.CollabIssuedToken{Scope: k.Scope, Call: capacityDigest(100 + i), Token: capacityDigest(200 + i)}
				issue := r.CollabIssuedRecord{Scope: k.Scope, Call: token.Call, Digest: k.Digest, Account: capacityDigest(7), ExpiresAt: time.Now().Add(time.Duration(60+rng.Intn(60)) * time.Minute)}
				for _, m := range []r.CollabRecordMutation{{Kind: r.CollabRecordMutationPending, Issued: issue, Token: token}, {Kind: r.CollabRecordMutationConfirm, Token: token}, {Kind: r.CollabRecordMutationVoid, Token: token}} {
					s.Apply(ctx, m)
					invariantRows(t, s, k.Scope)
				}
				// Evidence cannot veto an otherwise identical disposition partition.
				rows := invariantRows(t, s, k.Scope)
				reference := r.NewMemoryCollabRecordsStore()
				for _, m := range rows {
					if invariantPartition(m.Kind) != 0 {
						require.True(t, reference.ApplyMutation(m))
					}
				}
				record := r.CollabTerminatedRecord{Key: k, Event: capacityDigest(900 + i), ExpiresAt: time.Now().Add(time.Hour)}
				_, want := reference.ReserveLocalEnding(ctx, []r.CollabTerminatedRecord{record})
				_, got := s.ReserveLocalEnding(ctx, []r.CollabTerminatedRecord{record})
				require.Equal(t, want, got)
				invariantRows(t, s, k.Scope)
			}
		})
	}
}
func TestCollabGeneratedSharedInvalidationHorizon(t *testing.T) {
	for _, seed := range []int64{7, 29, 101} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			_, _, s := capacityCache(t)
			ctx := context.Background()
			rng := rand.New(rand.NewSource(seed))
			k := capacityKey(0)
			latest := time.Now().Add(time.Hour)
			collapsed := false
			for i := 0; i < 100; i++ {
				until := latest.Add(time.Duration(rng.Intn(20)) * time.Second)
				m := r.CollabRecordMutation{Kind: r.CollabRecordMutationVoid, Token: r.CollabIssuedToken{Scope: k.Scope, Call: capacityDigest(100 + i), Token: capacityDigest(200 + i)}, Issued: r.CollabIssuedRecord{ExpiresAt: until}}
				_, payload, ok := r.NormalizeCollabRecordMutation(m, time.Now())
				require.True(t, ok)
				_, field, _ := r.CollabRecordStorageIdentity(m)
				used := 0
				for f, v := range s.rdb.HGetAll(ctx, collabCacheKeys(k.Scope)[1]).Val() {
					used += len(v) + 3*len(f) + 32
				}
				collapse := used+len(payload)+3*len(field)+32 > r.CollabRecordEvidenceBytes
				if collapse {
					m.Issued.ExpiresAt = time.Now().Add(time.Minute)
				} else if until.After(latest) {
					latest = until
				}
				require.NoError(t, s.Apply(ctx, m))
				rows := invariantRows(t, s, k.Scope)
				if collapse {
					require.False(t, rows["v:"].Issued.ExpiresAt.Before(latest))
					require.Greater(t, s.rdb.PTTL(ctx, collabCacheKeys(k.Scope)[1]).Val(), 50*time.Minute)
					collapsed = true
					break
				}
			}
			require.True(t, collapsed)
		})
	}
}
func TestCollabGeneratedSharedExpiryRepair(t *testing.T) {
	for _, zone := range []int{0, 90, -330} {
		t.Run(fmt.Sprint(zone), func(t *testing.T) {
			_, client, s := capacityCache(t)
			ctx := context.Background()
			k := capacityKey(0)
			until := time.Now().Add(time.Hour).In(time.FixedZone("fixture", zone*60))
			token := r.CollabIssuedToken{Scope: k.Scope, Call: capacityDigest(8), Token: capacityDigest(9)}
			require.NoError(t, s.Apply(ctx, r.CollabRecordMutation{Kind: r.CollabRecordMutationPending, Token: token, Issued: r.CollabIssuedRecord{Scope: k.Scope, Call: token.Call, Digest: k.Digest, Account: capacityDigest(7), ExpiresAt: until}}))
			keys := collabCacheKeys(k.Scope)
			require.NoError(t, client.Del(ctx, keys[2]).Err())
			require.NoError(t, s.Apply(ctx, r.CollabRecordMutation{Kind: r.CollabRecordMutationConfirm, Token: token}))
			invariantRows(t, s, k.Scope)
			require.GreaterOrEqual(t, client.ZScore(ctx, keys[2], "i:"+string(token.Token)).Val(), float64(until.UnixMilli()))
			require.Greater(t, client.PTTL(ctx, keys[1]).Val(), 59*time.Minute)
		})
	}
}
