//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	r "github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func capacityDigest(i int) r.CollabDigest { return r.CollabDigest(fmt.Sprintf("%064x", i+1)) }
func capacityKey(i int) r.CollabDeliveryKey {
	return r.CollabDeliveryKey{Scope: r.CollabRecordScope{Tenant: capacityDigest(0), Group: capacityDigest(1), Tree: capacityDigest(i + 10)}, Thread: capacityDigest(2), Item: capacityDigest(3), Delivery: capacityDigest(4), Digest: capacityDigest(5)}
}
func capacityCache(t *testing.T) (*miniredis.Miniredis, *redis.Client, *collabSharedCache) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), ContextTimeoutEnabled: true, MaxRetries: -1})
	t.Cleanup(func() { client.Close() })
	return server, client, &collabSharedCache{rdb: client}
}

type capacityReserver interface {
	ReserveLocalEnding(context.Context, []r.CollabTerminatedRecord) (int, bool)
}

func TestCollabCapacitySharedAdmissionFailure(t *testing.T) {
	_, client, s := capacityCache(t)
	ctx := context.Background()
	k := capacityKey(0)
	failed := false
	for i := 0; i < 64; i++ {
		x := k
		x.Item = capacityDigest(100 + i)
		m := r.CollabRecordMutation{Kind: r.CollabRecordMutationTerminalRejected, TerminalRejection: r.CollabTerminalRejectionRecord{Key: x, Rejection: capacityDigest(7), Event: capacityDigest(200 + i), ExpiresAt: time.Now().Add(time.Hour)}}
		err := s.Apply(ctx, m)
		_, field, _ := r.CollabRecordStorageIdentity(m)
		stored := client.HExists(ctx, collabCacheKeys(k.Scope)[1], field).Val()
		if !stored {
			require.Error(t, err, "refused row was acknowledged")
			failed = true
			break
		}
		require.NoError(t, err)
	}
	require.True(t, failed)
}
func TestCollabCapacitySharedConcreteReservation(t *testing.T) {
	_, _, s := capacityCache(t)
	reserver, ok := any(s).(capacityReserver)
	require.True(t, ok)
	ctx := context.Background()
	k := capacityKey(0)
	records := []r.CollabTerminatedRecord{{Key: k, Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Hour)}}
	second := records[0]
	second.Key.Item = capacityDigest(91)
	records = append(records, second)
	n, ok := reserver.ReserveLocalEnding(ctx, records)
	require.True(t, ok)
	require.Equal(t, 1, n)
	q := r.CollabRecordQuery{Key: k}
	b := r.CollabRecordBatch{Scope: k.Scope, Queries: []r.CollabRecordQuery{q}, Dispositions: true}
	before, err := s.LookupBatch(ctx, b)
	require.NoError(t, err)
	require.Empty(t, before.Dispositions[q].Terminated)
	for i := 0; i < 64; i++ {
		x := k
		x.Item = capacityDigest(200 + i)
		s.Apply(ctx, r.CollabRecordMutation{Kind: r.CollabRecordMutationTerminalRejected, TerminalRejection: r.CollabTerminalRejectionRecord{Key: x, Event: capacityDigest(300 + i), Rejection: capacityDigest(8), ExpiresAt: time.Now().Add(time.Hour)}})
	}
	for _, v := range records {
		require.NoError(t, s.Apply(ctx, r.CollabRecordMutation{Kind: r.CollabRecordMutationTerminated, Terminated: v}))
	}
	got, err := s.LookupBatch(ctx, b)
	require.NoError(t, err)
	require.True(t, got.DispositionsComplete)
	require.Len(t, got.Dispositions[q].Terminated, 1)
}
func TestCollabCapacitySharedRealReservationsAndEvents(t *testing.T) {
	_, _, s := capacityCache(t)
	ctx := context.Background()
	k := capacityKey(0)
	for i := 0; i < 4; i++ {
		n, ok := s.ReserveTreeLocalEnding(ctx, k.Scope, capacityDigest(100+i))
		require.Equal(t, i < 3, ok)
		require.Equal(t, min(i+1, 3), n)
	}
	for i := 0; i < 2; i++ {
		require.NoError(t, s.Apply(ctx, r.CollabRecordMutation{Kind: r.CollabRecordMutationTerminated, Terminated: r.CollabTerminatedRecord{Key: k, Event: capacityDigest(200 + i), ExpiresAt: time.Now().Add(time.Hour)}}))
	}
	q := r.CollabRecordQuery{Key: k}
	got, err := s.LookupBatch(ctx, r.CollabRecordBatch{Scope: k.Scope, Queries: []r.CollabRecordQuery{q}, Dispositions: true})
	require.NoError(t, err)
	require.Len(t, got.Dispositions[q].Terminated, 2)
}
func TestCollabCapacitySharedExpiredProtection(t *testing.T) {
	server, client, s := capacityCache(t)
	ctx := context.Background()
	for i := 0; i < r.CollabRecordMaxTenantTrees; i++ {
		k := capacityKey(i)
		require.NoError(t, s.ApplyBatch(ctx, []r.CollabRecordMutation{{Kind: r.CollabRecordMutationPlaintext, Plaintext: r.CollabPlaintextRecord{Scope: k.Scope, Call: capacityDigest(7), Arguments: capacityDigest(8), ExpiresAt: time.Now().Add(r.CollabRecordTTL)}}, {Kind: r.CollabRecordMutationTerminated, Terminated: r.CollabTerminatedRecord{Key: k, Event: capacityDigest(9), ExpiresAt: time.Now().Add(time.Minute)}}}))
	}
	server.FastForward(2 * time.Minute)
	_, ok := s.ReserveTreeLocalEnding(ctx, capacityKey(10000).Scope, capacityDigest(99))
	require.True(t, ok)
	require.LessOrEqual(t, client.ZCard(ctx, collabCachePrefix+"trees").Val(), int64(r.CollabRecordMaxTrees))
}
func TestCollabCapacitySharedRetention(t *testing.T) {
	_, client, s := capacityCache(t)
	ctx := context.Background()
	k := capacityKey(0)
	queries := make([]r.CollabRecordQuery, 128)
	maxCharge := 0
	for i := range queries {
		call := capacityDigest(100 + i)
		queries[i] = r.CollabRecordQuery{Key: k, Call: call}
		m := r.CollabRecordMutation{Kind: r.CollabRecordMutationPlaintext, Plaintext: r.CollabPlaintextRecord{Scope: k.Scope, Call: call, Arguments: capacityDigest(8), ExpiresAt: time.Now().Add(time.Hour)}}
		require.NoError(t, s.Apply(ctx, m))
		_, field, _ := r.CollabRecordStorageIdentity(m)
		b, _ := json.Marshal(m)
		maxCharge = max(maxCharge, len(b)+3*len(field)+32)
	}
	got, err := s.LookupBatch(ctx, r.CollabRecordBatch{Scope: k.Scope, Queries: queries, Records: true})
	require.NoError(t, err)
	require.Empty(t, got.Records[queries[0]].Plaintext)
	require.Len(t, got.Records[queries[len(queries)-1]].Plaintext, 1)
	require.GreaterOrEqual(t, int(client.HLen(ctx, collabCacheKeys(k.Scope)[1]).Val()), min(r.CollabRecordMaxTreeEntries, r.CollabRecordEvidenceBytes/maxCharge)-1)
}

func TestCollabCapacitySharedSkewedRetention(t *testing.T) {
	_, client, s := capacityCache(t)
	ctx := context.Background()
	counts := [3]int{}
	for i := 0; i < 3500; i++ {
		tenant := 0
		if i%100 >= 90 {
			tenant = 1
		}
		if i%100 == 99 {
			tenant = 2
		}
		k := capacityKey(i)
		k.Scope.Tenant = capacityDigest(tenant)
		expiry := time.Now().Add(r.CollabRecordTTL)
		batch := make([]r.CollabRecordMutation, 0, 37)
		for call := 0; call < 32; call++ {
			batch = append(batch, r.CollabRecordMutation{Kind: r.CollabRecordMutationPlaintext, Plaintext: r.CollabPlaintextRecord{Scope: k.Scope, Call: capacityDigest(10000 + call), Arguments: capacityDigest(8), ExpiresAt: expiry}})
		}
		for d := 0; d < 4; d++ {
			x := k
			x.Item = capacityDigest(700 + d)
			batch = append(batch, r.CollabRecordMutation{Kind: r.CollabRecordMutationConsumed, Consumed: r.CollabConsumedRecord{Key: x, Account: capacityDigest(7), ExpiresAt: expiry}})
		}
		if i%10 == 0 {
			batch = append(batch, r.CollabRecordMutation{Kind: r.CollabRecordMutationTerminalRejected, TerminalRejection: r.CollabTerminalRejectionRecord{Key: k, Event: capacityDigest(90), Rejection: capacityDigest(8), ExpiresAt: expiry}})
		}
		require.NoError(t, s.ApplyBatch(ctx, batch))
		counts[tenant]++
		if i == 1499 {
			require.Equal(t, int64(1500), client.ZCard(ctx, collabCachePrefix+"trees").Val())
			t.Logf("three-day admitted=%v", counts)
		}
	}
	retained := [3]int{}
	for j := 0; j < 3; j++ {
		k := capacityKey(10000 + j)
		k.Scope.Tenant = capacityDigest(j)
		retained[j] = int(client.ZCard(ctx, collabCacheKeys(k.Scope)[4]).Val())
		n, ok := s.ReserveLocalEnding(ctx, []r.CollabTerminatedRecord{{Key: k, Event: capacityDigest(99), ExpiresAt: time.Now().Add(r.CollabRecordTTL)}})
		require.True(t, ok)
		require.Equal(t, 1, n)
	}
	require.Equal(t, counts, retained)
	t.Logf("seven-day retained=%v new reservations=3/3", retained)
}

func TestCollabCapacitySharedReservationRestartAndCopies(t *testing.T) {
	_, _, shared := capacityCache(t)
	ctx := context.Background()
	k := capacityKey(0)
	stores := []*r.SafeCollabRecordsStore{r.NewSafeCollabRecordsStore(r.NewMemoryCollabRecordsStore(), shared, 8), r.NewSafeCollabRecordsStore(r.NewMemoryCollabRecordsStore(), shared, 8)}
	var wg sync.WaitGroup
	var accepted atomic.Int64
	events := make(chan r.CollabDigest, 50)
	for i := 0; i < 50; i++ {
		wg.Go(func() {
			record := r.CollabTerminatedRecord{Key: k, Event: capacityDigest(100 + i), ExpiresAt: time.Now().Add(time.Hour)}
			_, ok := stores[i%2].ReserveLocalEnding(ctx, []r.CollabTerminatedRecord{record})
			if ok {
				accepted.Add(1)
				events <- record.Event
			}
		})
	}
	wg.Wait()
	close(events)
	for _, s := range stores {
		s.Close()
	}
	require.Equal(t, int64(3), accepted.Load())
	restarted := r.NewSafeCollabRecordsStore(r.NewMemoryCollabRecordsStore(), shared, 8)
	defer restarted.Close()
	for event := range events {
		record := r.CollabTerminatedRecord{Key: k, Event: event, ExpiresAt: time.Now().Add(time.Hour)}
		n, ok := restarted.ReserveLocalEnding(ctx, []r.CollabTerminatedRecord{record})
		require.True(t, ok)
		require.Equal(t, 3, n)
		require.True(t, restarted.Terminated(ctx, record))
	}
	q := r.CollabRecordQuery{Key: k}
	got, err := shared.LookupBatch(ctx, r.CollabRecordBatch{Scope: k.Scope, Queries: []r.CollabRecordQuery{q}, Dispositions: true})
	require.NoError(t, err)
	require.True(t, got.DispositionsComplete)
	require.Len(t, got.Dispositions[q].Terminated, 3)
	require.Equal(t, 3, got.LocalEndingCount())
	copies := make([]r.CollabDigest, 17)
	for i := range copies {
		copies[i] = capacityDigest(300 + i)
	}
	copyKey := capacityKey(1)
	require.True(t, restarted.Terminated(ctx, r.CollabTerminatedRecord{Key: copyKey, Copies: copies, Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Hour)}))
	copyKey.Item = copies[16]
	q = r.CollabRecordQuery{Key: copyKey}
	got, err = shared.LookupBatch(ctx, r.CollabRecordBatch{Scope: copyKey.Scope, Queries: []r.CollabRecordQuery{q}, Dispositions: true})
	require.NoError(t, err)
	require.False(t, got.DispositionsComplete)
	restarted.Close()
	require.False(t, restarted.Terminated(ctx, r.CollabTerminatedRecord{Key: capacityKey(2), Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Hour)}))
}

func TestCollabCapacitySharedDuplicateKeepsDeadline(t *testing.T) {
	_, client, s := capacityCache(t)
	ctx := context.Background()
	k := capacityKey(0)
	record := r.CollabTerminatedRecord{Key: k, Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Minute)}
	_, ok := s.ReserveLocalEnding(ctx, []r.CollabTerminatedRecord{record})
	require.True(t, ok)
	before := client.Get(ctx, collabCacheKeys(k.Scope)[6]).Val()
	record.ExpiresAt = time.Now().Add(time.Hour)
	_, ok = s.ReserveLocalEnding(ctx, []r.CollabTerminatedRecord{record})
	require.True(t, ok)
	require.Equal(t, before, client.Get(ctx, collabCacheKeys(k.Scope)[6]).Val(), "idempotent admission cannot extend protection")
}
