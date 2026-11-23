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

func TestCollabRecoverySharedReceiptWithoutPublication(t *testing.T) {
	_, _, s := capacityCache(t)
	ctx := context.Background()
	record := r.CollabTerminatedRecord{Key: capacityKey(0), Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Hour)}
	_, ok := s.ReserveLocalEnding(ctx, []r.CollabTerminatedRecord{record})
	require.True(t, ok)
	restart := r.NewSafeCollabRecordsStore(r.NewMemoryCollabRecordsStore(), s, 8)
	defer restart.Close()
	budget, cancel := r.WithCollabRecordReadBudget(ctx)
	defer cancel()
	q := r.CollabRecordQuery{Key: record.Key}
	got := restart.LookupBatch(budget, r.CollabRecordBatch{Scope: record.Key.Scope, Queries: []r.CollabRecordQuery{q}, Dispositions: true})
	require.True(t, got.DispositionsComplete)
	require.Len(t, got.Dispositions[q].Terminated, 1)
	require.False(t, r.CollabTerminationEffective(got.Dispositions[q].Terminated[0], record.Key, nil, time.Now()))
	require.True(t, r.CollabTerminationEffective(got.Dispositions[q].Terminated[0], record.Key, []r.CollabDigest{record.Event}, time.Now()))
}
func TestCollabRecoverySharedMetadataRetry(t *testing.T) {
	for _, fault := range []string{"wrong_type", "after_rows"} {
		t.Run(fault, func(t *testing.T) {
			_, client, s := capacityCache(t)
			ctx := context.Background()
			record := r.CollabTerminatedRecord{Key: capacityKey(0), Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Hour)}
			keys := collabCacheKeys(record.Key.Scope)
			if fault == "wrong_type" {
				require.NoError(t, client.Set(ctx, keys[3], "wrong-type", 0).Err())
				_, ok := s.ReserveLocalEnding(ctx, []r.CollabTerminatedRecord{record})
				require.False(t, ok)
				require.NoError(t, client.Del(ctx, keys[3]).Err())
			} else {
				now := time.Now()
				rows, ok := r.PrepareCollabLocalEnding([]r.CollabTerminatedRecord{record}, now)
				require.True(t, ok)
				pending := []map[string]string{}
				for _, m := range rows[1:] {
					_, f, _ := r.CollabRecordStorageIdentity(m)
					p, _ := json.Marshal(m)
					pending = append(pending, map[string]string{"field": f, "value": string(p)})
				}
				holds, _ := json.Marshal(pending)
				payload, _ := json.Marshal(rows[0])
				_, field, expiry := r.CollabRecordStorageIdentity(rows[0])
				source := strings.Replace(collabApplySource, "local current=tonumber", "if true then return redis.error_reply('injected interruption') end\nlocal current=tonumber", 1)
				require.NotEqual(t, collabApplySource, source)
				require.Error(t, client.Eval(ctx, source, keys, collabApplyArgs(now, expiry, field, rows[0].Kind, payload, string(holds))...).Err())
				if client.Exists(ctx, keys[1]).Val() > 0 {
					require.Positive(t, client.PTTL(ctx, keys[1]).Val(), "even an interrupted script must bound row lifetime")
				}
			}
			n, ok := s.ReserveLocalEnding(ctx, []r.CollabTerminatedRecord{record})
			require.True(t, ok)
			require.Equal(t, 1, n)
			for _, index := range []int{0, 3, 4, 5} {
				require.Equal(t, int64(1), client.ZCard(ctx, keys[index]).Val())
			}
			require.Positive(t, client.PTTL(ctx, keys[1]).Val())
			require.Equal(t, int64(1), client.Exists(ctx, keys[6]).Val())
			// A duplicate also repairs lost eligibility metadata without extending its lifetime.
			deadline := client.Get(ctx, keys[6]).Val()
			client.Del(ctx, keys[3], keys[5])
			_, ok = s.ReserveLocalEnding(ctx, []r.CollabTerminatedRecord{record})
			require.True(t, ok)
			require.Equal(t, deadline, client.Get(ctx, keys[6]).Val())
			require.Equal(t, int64(1), client.ZCard(ctx, keys[3]).Val())
			require.Equal(t, int64(1), client.ZCard(ctx, keys[5]).Val())
		})
	}
}
func TestCollabRecoverySharedConflictingCopies(t *testing.T) {
	_, _, s := capacityCache(t)
	ctx := context.Background()
	a := r.CollabTerminatedRecord{Key: capacityKey(0), Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, s.Apply(ctx, r.CollabRecordMutation{Kind: r.CollabRecordMutationTerminated, Terminated: a}))
	a.Copies = []r.CollabDigest{capacityDigest(100)}
	require.Error(t, s.Apply(ctx, r.CollabRecordMutation{Kind: r.CollabRecordMutationTerminated, Terminated: a}))
}
func TestCollabRecoverySharedVoidAfterEvictionAndClose(t *testing.T) {
	_, _, shared := capacityCache(t)
	ctx := context.Background()
	local := r.NewMemoryCollabRecordsStore()
	k := capacityKey(0)
	record := r.CollabIssuedRecord{Scope: k.Scope, Call: capacityDigest(8), Digest: k.Digest, Account: capacityDigest(9), ExpiresAt: time.Now().Add(time.Hour)}
	token, ok := local.PendingIssued(ctx, record)
	require.True(t, ok)
	require.NoError(t, shared.ApplyBatch(ctx, []r.CollabRecordMutation{{Kind: r.CollabRecordMutationPending, Issued: record, Token: token}, {Kind: r.CollabRecordMutationConfirm, Token: token}}))
	for i := 0; i < 64; i++ {
		local.PlaintextDelivered(ctx, r.CollabPlaintextRecord{Scope: k.Scope, Call: capacityDigest(100 + i), Arguments: capacityDigest(9), ExpiresAt: time.Now().Add(time.Hour)})
	}
	s := r.NewSafeCollabRecordsStore(local, shared, 8)
	s.VoidIssued(ctx, token)
	s.Close()
	q := r.CollabRecordQuery{Key: k}
	got, err := shared.LookupBatch(ctx, r.CollabRecordBatch{Scope: k.Scope, Queries: []r.CollabRecordQuery{q}, Records: true})
	require.NoError(t, err)
	for _, v := range got.Records[q].Issued {
		require.NotEqual(t, r.CollabIssuedStatusConfirmed, v.Status)
	}
}

// The ordinary writer is stalled, but shutdown can use the dedicated
// invalidation capability without waiting behind that writer.
type recoveryStalledCache struct {
	*collabSharedCache
	entered chan struct{}
}

func (s *recoveryStalledCache) Apply(ctx context.Context, _ r.CollabRecordMutation) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}
func (s *recoveryStalledCache) ApplyBatch(ctx context.Context, rows []r.CollabRecordMutation) error {
	return s.Apply(ctx, rows[0])
}
func TestCollabRecoverySharedShutdownBypassesStalledWriter(t *testing.T) {
	_, _, shared := capacityCache(t)
	ctx := context.Background()
	k := capacityKey(0)
	local := r.NewMemoryCollabRecordsStore()
	record := r.CollabIssuedRecord{Scope: k.Scope, Call: capacityDigest(8), Digest: k.Digest, Account: capacityDigest(9), ExpiresAt: time.Now().Add(time.Hour)}
	token, ok := local.PendingIssued(ctx, record)
	require.True(t, ok)
	require.NoError(t, shared.ApplyBatch(ctx, []r.CollabRecordMutation{{Kind: r.CollabRecordMutationPending, Token: token, Issued: record}, {Kind: r.CollabRecordMutationConfirm, Token: token}}))
	stalled := &recoveryStalledCache{collabSharedCache: shared, entered: make(chan struct{}, 1)}
	safe := r.NewSafeCollabRecordsStore(local, stalled, 8)
	require.True(t, safe.PlaintextDelivered(ctx, r.CollabPlaintextRecord{Scope: k.Scope, Call: capacityDigest(100), Arguments: capacityDigest(9), ExpiresAt: time.Now().Add(time.Hour)}))
	<-stalled.entered
	start := time.Now()
	safe.VoidIssued(ctx, token)
	require.Less(t, time.Since(start), 10*time.Millisecond)
	safe.Close()
	restarted := r.NewSafeCollabRecordsStore(r.NewMemoryCollabRecordsStore(), shared, 8)
	defer restarted.Close()
	budget, cancel := r.WithCollabRecordReadBudget(ctx)
	defer cancel()
	q := r.CollabRecordQuery{Key: k}
	got := restarted.LookupBatch(budget, r.CollabRecordBatch{Scope: k.Scope, Queries: []r.CollabRecordQuery{q}, Records: true})
	require.True(t, got.RecordsComplete)
	for _, v := range got.Records[q].Issued {
		require.NotEqual(t, r.CollabIssuedStatusConfirmed, v.Status)
	}
}

type recoverySlowAdmission struct{ *collabSharedCache }

func (s *recoverySlowAdmission) ReserveLocalEnding(ctx context.Context, _ []r.CollabTerminatedRecord) (int, bool) {
	<-ctx.Done()
	return r.CollabTreeLocalEndingLimit, false
}
func TestCollabRecoveryReservationOutageDeadline(t *testing.T) {
	server, _, shared := capacityCache(t)
	ctx := context.Background()
	for _, down := range []bool{false, true} {
		var backend r.CollabSharedRecordsStore = &recoverySlowAdmission{shared}
		if down {
			server.Close()
			backend = shared
		}
		safe := r.NewSafeCollabRecordsStore(r.NewMemoryCollabRecordsStore(), backend, 8)
		start := time.Now()
		n, ok := safe.ReserveLocalEnding(ctx, []r.CollabTerminatedRecord{{Key: capacityKey(0), Event: capacityDigest(90), ExpiresAt: time.Now().Add(time.Hour)}})
		elapsed := time.Since(start)
		require.False(t, ok)
		require.Equal(t, 3, n)
		require.Less(t, elapsed, 500*time.Millisecond)
		t.Logf("pre-output admission down=%t elapsed=%s", down, elapsed)
		safe.Close()
	}
}
