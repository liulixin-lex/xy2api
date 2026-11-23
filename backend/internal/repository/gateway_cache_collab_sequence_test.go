//go:build unit

package repository

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	r "github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type collabSequence struct {
	t          *testing.T
	ctx        context.Context
	memory     *r.MemoryCollabRecordsStore
	shared     *collabSharedCache
	server     *miniredis.Miniredis
	client     *redis.Client
	prior      map[r.CollabRecordScope]map[string]r.CollabRecordMutation
	deadlines  map[r.CollabRecordScope]map[string]float64
	voided     map[r.CollabIssuedToken]time.Time
	operations map[string]int
}

func newCollabSequence(t *testing.T) *collabSequence {
	server, client, shared := capacityCache(t)
	return &collabSequence{t: t, ctx: context.Background(), memory: r.NewMemoryCollabRecordsStore(), shared: shared, server: server, client: client, prior: make(map[r.CollabRecordScope]map[string]r.CollabRecordMutation), deadlines: make(map[r.CollabRecordScope]map[string]float64), voided: make(map[r.CollabIssuedToken]time.Time), operations: make(map[string]int)}
}
func (s *collabSequence) apply(m r.CollabRecordMutation) {
	s.t.Helper()
	local := s.memory.ApplyMutation(m)
	remoteErr := s.shared.Apply(s.ctx, m)
	remote := remoteErr == nil
	require.Equal(s.t, local, remote, "admission kind=%s err=%v scope=%s operations=%v", m.Kind, remoteErr, m.Token.Scope.Tree, s.operations)
	if local && m.Kind == r.CollabRecordMutationVoid {
		s.voided[m.Token] = m.Issued.ExpiresAt
	}
	s.operations[string(m.Kind)]++
}
func sequenceLookup(result r.CollabRecordBatchResult) []string {
	out := []string{fmt.Sprint("count:", result.TreeLocalEndings)}
	for q, records := range result.Records {
		prefix := fmt.Sprint(q)
		for _, v := range records.IssuedTokens {
			out = append(out, fmt.Sprint(prefix, " issue ", v.Token, " ", v.Record.Status, " ", v.Record.ExpiresAt.UnixNano()))
		}
		for _, v := range records.VoidedTokens {
			out = append(out, fmt.Sprint(prefix, " void ", v))
		}
		for _, v := range records.Plaintext {
			out = append(out, fmt.Sprint(prefix, " plain ", v.Call, " ", v.Arguments, " ", v.ExpiresAt.UnixNano()))
		}
		for _, v := range records.Consumed {
			out = append(out, fmt.Sprint(prefix, " consumed ", v.Key, " ", v.Account, " ", v.ExpiresAt.UnixNano()))
		}
	}
	for q, dispositions := range result.Dispositions {
		prefix := fmt.Sprint(q)
		for _, v := range dispositions.Replaced {
			out = append(out, fmt.Sprint(prefix, " replaced ", v.Key, " ", v.Account, " ", v.Placeholder, " ", v.ExpiresAt.UnixNano()))
		}
		for _, v := range dispositions.Terminated {
			out = append(out, fmt.Sprint(prefix, " terminated ", v.Key, " ", v.Event, " ", v.Copies, " ", v.ExpiresAt.UnixNano()))
		}
		for _, v := range dispositions.TerminalRejections {
			out = append(out, fmt.Sprint(prefix, " rejection ", v.Key, " ", v.Rejection, " ", v.Count))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
func sequenceQueries(key r.CollabDeliveryKey) []r.CollabRecordQuery {
	queries := []r.CollabRecordQuery{{Key: key}}
	for i := 0; i < 16; i++ {
		k := key
		k.Item = capacityDigest(1000 + i)
		queries = append(queries, r.CollabRecordQuery{Key: k, Call: capacityDigest(100 + i), Event: capacityDigest(500 + i)})
	}
	return queries
}
func (s *collabSequence) check(key r.CollabDeliveryKey) {
	s.t.Helper()
	scope := key.Scope
	rows := invariantRows(s.t, s.shared, scope)
	keys := collabCacheKeys(scope)
	for f, m := range rows {
		if old, exists := s.prior[scope][f]; exists {
			require.False(s.t, invariantExpiry(m).Before(invariantExpiry(old)), "row expiry shortened: %s", f)
		}
		score, err := s.client.ZScore(s.ctx, keys[2], f).Result()
		require.NoError(s.t, err)
		require.GreaterOrEqual(s.t, score, float64(invariantExpiry(m).UnixMilli()))
		if old, exists := s.deadlines[scope][f]; exists {
			require.GreaterOrEqual(s.t, score, old, "row TTL horizon shortened")
		}
		if s.deadlines[scope] == nil {
			s.deadlines[scope] = make(map[string]float64)
		}
		s.deadlines[scope][f] = score
	}
	s.prior[scope] = rows
	// Inspect stored local charges without adding a test-only production API.
	// All concurrent calls have joined before this observation.
	trees := reflect.ValueOf(s.memory).Elem().FieldByName("trees").MapRange()
	for trees.Next() {
		entries := trees.Value().Elem().FieldByName("entries").MapRange()
		counts := [3]int{}
		bytes := [3]int{}
		for entries.Next() {
			e := entries.Value()
			part := invariantPartition(r.CollabRecordMutationKind(e.FieldByName("mutation").FieldByName("Kind").String()))
			counts[part]++
			bytes[part] += int(e.FieldByName("charge").Int())
		}
		for part, limit := range []int{r.CollabRecordEvidenceBytes, r.CollabRecordDispositionBytes, r.CollabRecordCounterBytes} {
			require.LessOrEqual(s.t, bytes[part], limit)
			n := 64
			if part == 2 {
				n = 3
			}
			require.LessOrEqual(s.t, counts[part], n)
		}
	}
	b := r.CollabRecordBatch{Scope: scope, Queries: sequenceQueries(key), Records: true, Dispositions: true}
	a := s.memory.LookupBatch(s.ctx, b)
	z, err := s.shared.LookupBatch(s.ctx, b)
	require.NoError(s.t, err)
	if a.RecordsComplete && a.DispositionsComplete && z.RecordsComplete && z.DispositionsComplete {
		s.operations["complete_comparisons"]++
		left, right := sequenceLookup(a), sequenceLookup(z)
		if !slices.Equal(left, right) {
			for _, v := range left {
				if !slices.Contains(right, v) {
					s.t.Logf("memory only: %s", v)
				}
			}
			for _, v := range right {
				if !slices.Contains(left, v) {
					s.t.Logf("shared only: %s", v)
				}
			}
			s.t.Fatalf("complete replicas disagree: memory=%d shared=%d operations=%v", len(left), len(right), s.operations)
		}
	}
	if !a.RecordsComplete || !a.DispositionsComplete || !z.RecordsComplete || !z.DispositionsComplete {
		s.operations["incomplete_comparisons"]++
	}
	for _, got := range []r.CollabRecordBatchResult{a, z} {
		for _, records := range got.Records {
			for _, issued := range records.IssuedTokens {
				if s.voided[issued.Token].After(time.Now()) {
					require.NotEqual(s.t, r.CollabIssuedStatusConfirmed, issued.Record.Status)
				}
			}
		}
		for _, d := range got.Dispositions {
			for _, term := range d.Terminated {
				require.False(s.t, r.CollabTerminationEffective(term, term.Key, nil, time.Now()))
				require.True(s.t, r.CollabTerminationEffective(term, term.Key, []r.CollabDigest{term.Event}, time.Now()))
			}
		}
	}
}
func (s *collabSequence) ending(key r.CollabDeliveryKey, event r.CollabDigest, expiry time.Time) {
	s.t.Helper()
	record := r.CollabTerminatedRecord{Key: key, Event: event, ExpiresAt: expiry}
	// A separate disposition-only tree is the admission oracle: evidence must
	// never alter the count or space available to this exact concrete plan.
	control := r.NewMemoryCollabRecordsStore()
	for _, m := range invariantRows(s.t, s.shared, key.Scope) {
		if invariantPartition(m.Kind) != 0 {
			require.True(s.t, control.ApplyMutation(m))
		}
	}
	_, want := control.ReserveLocalEnding(s.ctx, []r.CollabTerminatedRecord{record})
	_, a := s.memory.ReserveLocalEnding(s.ctx, []r.CollabTerminatedRecord{record})
	_, b := s.shared.ReserveLocalEnding(s.ctx, []r.CollabTerminatedRecord{record})
	require.Equal(s.t, want, a)
	require.Equal(s.t, want, b)
	s.operations["reservation"]++
	if a && b {
		s.check(key)
		s.apply(r.CollabRecordMutation{Kind: r.CollabRecordMutationTerminated, Terminated: record})
		s.operations["reserved_publication"]++
	}
}
func (s *collabSequence) partial(m r.CollabRecordMutation) {
	now := time.Now()
	m, payload, ok := r.NormalizeCollabRecordMutation(m, now)
	require.True(s.t, ok)
	scope, field, expiry := r.CollabRecordStorageIdentity(m)
	source := strings.Replace(collabApplySource, "local current=tonumber", "if true then return redis.error_reply('injected interruption') end\nlocal current=tonumber", 1)
	require.NotEqual(s.t, source, collabApplySource)
	err := s.client.Eval(s.ctx, source, collabCacheKeys(scope), collabApplyArgs(now, expiry, field, m.Kind, payload, "")...).Err()
	require.Error(s.t, err)
	invariantRows(s.t, s.shared, scope)
	s.apply(m)
	s.operations["partial_script"]++
}
func TestCollabGeneratedStoreSequence(t *testing.T) {
	steps := 512
	if value := os.Getenv("COLLAB_INVARIANT_STEPS"); value != "" {
		n, err := strconv.Atoi(value)
		require.NoError(t, err)
		require.GreaterOrEqual(t, n, steps)
		steps = n
	}
	for _, seed := range []int64{7, 29, 101} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			s := newCollabSequence(t)
			rng := rand.New(rand.NewSource(seed))
			until := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
			for step := 0; step < steps; step++ {
				op := rng.Intn(19)
				if step < 19 {
					op = step
				}
				s.operations[fmt.Sprint("operation_", op)]++
				// Rotate trees while retaining prior trees, exercising fresh and crowded scopes.
				key := capacityKey(step/64*2 + rng.Intn(2))
				slot := rng.Intn(16)
				key.Item = capacityDigest(1000 + slot)
				token := r.CollabIssuedToken{Scope: key.Scope, Call: capacityDigest(100 + slot), Token: capacityDigest(200 + slot)}
				issue := r.CollabRecordMutation{Kind: r.CollabRecordMutationPending, Token: token, Issued: r.CollabIssuedRecord{Scope: key.Scope, Call: token.Call, Digest: key.Digest, Account: capacityDigest(7), Status: r.CollabIssuedStatusPending, ExpiresAt: until}}
				switch op {
				case 0:
					s.apply(issue)
				case 1:
					s.apply(r.CollabRecordMutation{Kind: r.CollabRecordMutationConfirm, Token: token})
				case 2:
					s.apply(r.CollabRecordMutation{Kind: r.CollabRecordMutationVoid, Token: token, Issued: r.CollabIssuedRecord{ExpiresAt: until}})
				case 3:
					s.apply(r.CollabRecordMutation{Kind: r.CollabRecordMutationPlaintext, Plaintext: r.CollabPlaintextRecord{Scope: key.Scope, Call: token.Call, Arguments: capacityDigest(8), ExpiresAt: until}})
				case 4:
					s.apply(r.CollabRecordMutation{Kind: r.CollabRecordMutationConsumed, Consumed: r.CollabConsumedRecord{Key: key, Account: capacityDigest(7), ExpiresAt: until}})
				case 5:
					s.apply(r.CollabRecordMutation{Kind: r.CollabRecordMutationReplaced, Replaced: r.CollabReplacedRecord{Key: key, Account: capacityDigest(7), Placeholder: r.CollabPlaceholderMessage, ExpiresAt: until}})
				case 6:
					s.apply(r.CollabRecordMutation{Kind: r.CollabRecordMutationTerminated, Terminated: r.CollabTerminatedRecord{Key: key, Event: capacityDigest(500 + slot), ExpiresAt: until}})
				case 7:
					s.ending(key, capacityDigest(500+slot), until)
				case 8:
					s.apply(r.CollabRecordMutation{Kind: r.CollabRecordMutationTerminalRejected, TerminalRejection: r.CollabTerminalRejectionRecord{Key: key, Rejection: capacityDigest(9), Event: capacityDigest(500 + slot), ExpiresAt: until}})
				case 9:
					for i := 0; i < 24; i++ {
						s.apply(r.CollabRecordMutation{Kind: r.CollabRecordMutationPlaintext, Plaintext: r.CollabPlaintextRecord{Scope: key.Scope, Call: capacityDigest(100 + i%16), Arguments: capacityDigest(2000 + step*24 + i), ExpiresAt: until}})
						s.check(key)
					}
				case 10:
					require.NoError(t, s.client.Del(s.ctx, collabCacheKeys(key.Scope)[2]).Err())
					s.apply(issue)
					s.operations["deleted_index"]++
				case 11:
					index := collabCacheKeys(key.Scope)[7]
					require.NoError(t, s.client.Del(s.ctx, index).Err())
					require.NoError(t, s.client.Set(s.ctx, index, "wrong-type", 0).Err())
					require.Error(t, s.shared.Apply(s.ctx, issue))
					require.NoError(t, s.client.Del(s.ctx, index).Err())
					s.apply(issue)
					s.operations["wrong_type"]++
				case 12:
					faultKey := capacityKey(100000 + step)
					fault := issue
					fault.Token.Scope = faultKey.Scope
					fault.Issued.Scope = faultKey.Scope
					s.partial(fault)
					s.check(faultKey)
				case 13:
					restarted := r.NewSafeCollabRecordsStore(r.NewMemoryCollabRecordsStore(), s.shared, 8)
					budget, cancel := r.WithCollabRecordReadBudget(s.ctx)
					b := r.CollabRecordBatch{Scope: key.Scope, Queries: sequenceQueries(key), Records: true, Dispositions: true}
					got := restarted.LookupBatch(budget, b)
					cancel()
					restarted.Close()
					want, err := s.shared.LookupBatch(s.ctx, b)
					require.NoError(t, err)
					if got.RecordsComplete && got.DispositionsComplete {
						require.Equal(t, sequenceLookup(want), sequenceLookup(got))
					}
					s.operations["restart"]++
				case 14:
					expiryKey := capacityKey(200000 + step)
					expires := time.Now().Add(8 * time.Millisecond)
					m := r.CollabRecordMutation{Kind: r.CollabRecordMutationPlaintext, Plaintext: r.CollabPlaintextRecord{Scope: expiryKey.Scope, Call: capacityDigest(100), Arguments: capacityDigest(8), ExpiresAt: expires}}
					s.apply(m)
					s.check(expiryKey)
					s.server.FastForward(10 * time.Millisecond)
					time.Sleep(10 * time.Millisecond)
					s.check(expiryKey)
					s.operations["fast_forward"]++
				case 15:
					var wg sync.WaitGroup
					for w := 0; w < 4; w++ {
						wg.Go(func() {
							k := capacityKey(300000 + w)
							m := issue
							m.Token.Scope = k.Scope
							m.Issued.Scope = k.Scope
							m.Token.Token = capacityDigest(10000 + step*4 + w)
							require.Equal(t, s.memory.ApplyMutation(m), s.shared.Apply(s.ctx, m) == nil)
						})
					}
					wg.Wait()
					for w := 0; w < 4; w++ {
						s.check(capacityKey(300000 + w))
					}
					s.operations["concurrent"]++
				case 16, 17:
					s.shutdown(step, op == 17)
					s.operations["shutdown"]++
				case 18:
					s.apply(issue)
					s.apply(r.CollabRecordMutation{Kind: r.CollabRecordMutationConfirm, Token: token})
					s.apply(r.CollabRecordMutation{Kind: r.CollabRecordMutationVoid, Token: token, Issued: r.CollabIssuedRecord{ExpiresAt: until}})
				}
				s.check(key)
			}
			t.Logf("seed=%d steps=%d coverage=%v", seed, steps, s.operations)
		})
	}
}

type sequenceShutdownStore struct {
	*collabSharedCache
	down bool
}

func (s *sequenceShutdownStore) WriteInvalidations(ctx context.Context, rows []r.CollabRecordMutation) error {
	if s.down {
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case <-time.After(time.Millisecond):
		return s.collabSharedCache.WriteInvalidations(ctx, rows)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *collabSequence) shutdown(step int, down bool) {
	s.t.Helper()
	key := capacityKey(400000 + step)
	token := r.CollabIssuedToken{Scope: key.Scope, Call: capacityDigest(100), Token: capacityDigest(200)}
	issue := r.CollabRecordMutation{Kind: r.CollabRecordMutationPending, Token: token, Issued: r.CollabIssuedRecord{Scope: key.Scope, Call: token.Call, Digest: key.Digest, Account: capacityDigest(7), Status: r.CollabIssuedStatusPending, ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)}}
	s.apply(issue)
	s.apply(r.CollabRecordMutation{Kind: r.CollabRecordMutationConfirm, Token: token})
	s.check(key)
	// Exercise the same local mutation in both adapters; one shared backend is
	// unavailable through shutdown. Only that explicit process-loss case may
	// recover a confirmed token after its unpersisted local state is discarded.
	local := r.NewMemoryCollabRecordsStore()
	require.True(s.t, local.ApplyMutation(issue))
	adapter := r.NewSafeCollabRecordsStore(local, &sequenceShutdownStore{s.shared, down}, 8)
	adapter.VoidIssued(s.ctx, token)
	s.memory.VoidIssued(s.ctx, token)
	adapter.Close()
	q := r.CollabRecordQuery{Key: key}
	b := r.CollabRecordBatch{Scope: key.Scope, Queries: []r.CollabRecordQuery{q}, Records: true}
	got, err := s.shared.LookupBatch(s.ctx, b)
	require.NoError(s.t, err)
	if down {
		require.Len(s.t, got.Records[q].Issued, 1)
		require.Equal(s.t, r.CollabIssuedStatusConfirmed, got.Records[q].Issued[0].Status)
		s.operations["documented_loss"]++
		return
	}
	for _, v := range got.Records[q].Issued {
		require.NotEqual(s.t, r.CollabIssuedStatusConfirmed, v.Status)
	}
	s.check(key)
}
