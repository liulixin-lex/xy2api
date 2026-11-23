//go:build unit

package repository

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
	"unsafe"

	r "github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type coverageTermID struct {
	key   r.CollabDeliveryKey
	event r.CollabDigest
}

// The ledger is written only from submitted operations and their acknowledgements.
// Neither stored rows, implementation charges nor lookup results populate it.
type coverageLedger struct {
	terms        map[coverageTermID]r.CollabTerminatedRecord
	reservations map[r.CollabRecordScope]map[r.CollabDigest]time.Time
	voids        map[r.CollabIssuedToken]time.Time
}

func newCoverageLedger() *coverageLedger {
	return &coverageLedger{terms: make(map[coverageTermID]r.CollabTerminatedRecord), reservations: make(map[r.CollabRecordScope]map[r.CollabDigest]time.Time), voids: make(map[r.CollabIssuedToken]time.Time)}
}

func (l *coverageLedger) accept(m r.CollabRecordMutation) {
	now := time.Now()
	switch m.Kind {
	case r.CollabRecordMutationVoid:
		expiry := m.Issued.ExpiresAt
		if expiry.IsZero() {
			expiry = now.Add(7 * 24 * time.Hour)
		}
		if expiry.After(l.voids[m.Token]) {
			l.voids[m.Token] = expiry
		}
	case r.CollabRecordMutationTerminated:
		id := coverageTermID{m.Terminated.Key, m.Terminated.Event}
		if !l.terms[id].ExpiresAt.After(now) {
			v := m.Terminated
			v.Copies = slices.Clone(v.Copies)
			l.terms[id] = v
		}
	}
}

func (l *coverageLedger) reserve(records []r.CollabTerminatedRecord) {
	first := records[0]
	if l.reservations[first.Key.Scope] == nil {
		l.reservations[first.Key.Scope] = make(map[r.CollabDigest]time.Time)
	}
	if !l.reservations[first.Key.Scope][first.Event].After(time.Now()) {
		l.reservations[first.Key.Scope][first.Event] = first.ExpiresAt
	}
	for _, record := range records {
		record.ExpiresAt = first.ExpiresAt
		l.accept(r.CollabRecordMutation{Kind: r.CollabRecordMutationTerminated, Terminated: record})
	}
}

// Read-only inspection after writers join. Unsafe is confined to exposing the
// private row value to JSON, avoiding a production test API or cached charges.
func coverageMemoryScopes(store *r.MemoryCollabRecordsStore) []r.CollabRecordScope {
	trees := reflect.ValueOf(store).Elem().FieldByName("trees")
	trees = reflect.NewAt(trees.Type(), unsafe.Pointer(trees.UnsafeAddr())).Elem()
	var out []r.CollabRecordScope
	for _, key := range trees.MapKeys() {
		out = append(out, key.Interface().(r.CollabRecordScope))
	}
	return out
}

func coverageMemoryRows(store *r.MemoryCollabRecordsStore, scope r.CollabRecordScope) map[string]string {
	out := make(map[string]string)
	trees := reflect.ValueOf(store).Elem().FieldByName("trees")
	trees = reflect.NewAt(trees.Type(), unsafe.Pointer(trees.UnsafeAddr())).Elem()
	tree := trees.MapIndex(reflect.ValueOf(scope))
	if !tree.IsValid() {
		return out
	}
	entries := tree.Elem().FieldByName("entries")
	entries = reflect.NewAt(entries.Type(), unsafe.Pointer(entries.UnsafeAddr())).Elem()
	iter := entries.MapRange()
	for iter.Next() {
		entry := reflect.New(iter.Value().Type()).Elem()
		entry.Set(iter.Value())
		mutation := entry.FieldByName("mutation")
		mutation = reflect.NewAt(mutation.Type(), unsafe.Pointer(mutation.UnsafeAddr())).Elem()
		payload, err := json.Marshal(mutation.Interface())
		if err != nil {
			panic(err)
		}
		out[iter.Key().String()] = string(payload)
	}
	return out
}

func coverageBounds(t *testing.T, rows map[string]string) {
	t.Helper()
	counts, bytes := [3]int{}, [3]int{}
	for field, payload := range rows {
		var m r.CollabRecordMutation
		require.NoError(t, json.Unmarshal([]byte(payload), &m))
		part := invariantPartition(m.Kind)
		counts[part]++
		bytes[part] += len(payload) + 3*len(field) + 32
		require.LessOrEqual(t, len(payload), 4096)
	}
	for part, limit := range []int{16 * 1024, 13 * 1024, 3 * 1024} {
		require.LessOrEqual(t, bytes[part], limit, "encoded partition %d: %v", part, bytes)
		require.LessOrEqual(t, counts[part], []int{64, 64, 3}[part])
	}
}

func coverageTermString(record r.CollabTerminatedRecord) string {
	record.ExpiresAt = record.ExpiresAt.UTC()
	if len(record.Copies) == 0 {
		record.Copies = nil
	}
	encoded, _ := json.Marshal(record)
	return string(encoded)
}

func coverageMatches(record r.CollabTerminatedRecord, q r.CollabRecordQuery) bool {
	return record.Key == q.Key || q.Event != "" && q.Event == record.Event ||
		record.Key.Scope == q.Key.Scope && record.Key.Thread == q.Key.Thread && record.Key.Delivery == q.Key.Delivery && slices.Contains(record.Copies, q.Key.Item)
}

func coverageReceipts(t *testing.T, record r.CollabTerminatedRecord, now time.Time) {
	t.Helper()
	wrong := capacityDigest(987654)
	for _, receipts := range [][]r.CollabDigest{nil, {""}, {wrong}, {wrong, wrong}} {
		require.False(t, r.CollabTerminationEffective(record, record.Key, receipts, now), "missing or foreign receipt")
	}
	for _, receipts := range [][]r.CollabDigest{{record.Event}, {record.Event, record.Event}, {wrong, record.Event}} {
		require.True(t, r.CollabTerminationEffective(record, record.Key, receipts, now))
	}
	for field := 0; field < 7; field++ {
		key := record.Key
		switch field {
		case 0:
			key.Scope.Tenant = wrong
		case 1:
			key.Scope.Group = wrong
		case 2:
			key.Scope.Tree = wrong
		case 3:
			key.Thread = wrong
		case 4:
			key.Delivery = wrong
		case 5:
			key.Item = wrong
		case 6:
			key.Digest = wrong
		}
		require.False(t, r.CollabTerminationEffective(record, key, []r.CollabDigest{record.Event}, now), "foreign delivery field %d", field)
	}
	for _, item := range record.Copies {
		key := record.Key
		key.Item = item
		key.Digest = wrong
		require.True(t, r.CollabTerminationEffective(record, key, []r.CollabDigest{record.Event}, now))
	}
	require.False(t, r.CollabTerminationEffective(record, record.Key, []r.CollabDigest{record.Event}, record.ExpiresAt))
}

func (l *coverageLedger) check(s *collabSequence, key r.CollabDeliveryKey, local map[string]string, shared map[string]r.CollabRecordMutation) {
	t := s.t
	t.Helper()
	now := time.Now()
	scope := key.Scope
	queries := []r.CollabRecordQuery{{Key: key}}
	for id, record := range l.terms {
		if id.key.Scope != scope {
			continue
		}
		queries = append(queries, r.CollabRecordQuery{Key: id.key}, r.CollabRecordQuery{Key: key, Event: id.event})
		for _, copy := range record.Copies {
			k := id.key
			k.Item = copy
			k.Digest = ""
			queries = append(queries, r.CollabRecordQuery{Key: k})
		}
	}
	// Expired ledger entries are retained to query their absence as well.
	queries = slices.CompactFunc(queries, func(a, b r.CollabRecordQuery) bool { return a == b })
	require.LessOrEqual(t, len(queries), 256)
	batch := r.CollabRecordBatch{Scope: scope, Queries: queries, Records: true, Dispositions: true}
	a := s.memory.LookupBatch(s.ctx, batch)
	b, err := s.shared.LookupBatch(s.ctx, batch)
	require.NoError(t, err)
	// An observation can expire between the Redis read and its adapter's
	// reconstruction. Demand a complete fresh read; never count unknown as empty.
	for attempt := 0; attempt < 3 && (!b.RecordsComplete || !b.DispositionsComplete); attempt++ {
		s.operations["ledger_incomplete_retry"]++
		time.Sleep(time.Millisecond)
		b, err = s.shared.LookupBatch(s.ctx, batch)
		require.NoError(t, err)
	}
	// Read the shared store through its Redis lookup script independently of
	// the MemoryCollabRecordsStore used internally by its public Go adapter.
	values, err := collabLookupScript.Run(s.ctx, s.client, collabCacheKeys(scope), now.UnixMilli(), r.CollabRecordMaxTrees, r.CollabRecordMaxTenantTrees, collabCachePrefix, r.CollabRecordTenantShare, r.CollabRecordSharedTrees).StringSlice()
	require.NoError(t, err)
	require.NotEmpty(t, values)
	require.Equal(t, "1", values[0], "shared native lookup must be complete")
	native := make(map[string]r.CollabRecordMutation)
	nativeCount := 0
	for i, value := range values[1:] {
		var m r.CollabRecordMutation
		require.NoError(t, json.Unmarshal([]byte(value), &m))
		native[fmt.Sprint(i)] = m
		if m.Kind == r.CollabRecordMutationLocalEnding && m.Issued.ExpiresAt.After(now) {
			nativeCount++
		}
	}
	wantCount := 0
	for _, expiry := range l.reservations[scope] {
		if expiry.After(now) {
			wantCount++
		}
	}
	require.Equal(t, wantCount, nativeCount, "accepted reservation count in native shared lookup")
	for backend, result := range []r.CollabRecordBatchResult{a, b} {
		require.True(t, result.RecordsComplete, "backend=%d scope=%s rows=%v now=%s", backend, scope.Tree, shared, time.Now())
		require.True(t, result.DispositionsComplete)
		require.True(t, result.TreeLocalEndingsKnown)
		require.Equal(t, wantCount, result.TreeLocalEndings)
	}
	for _, q := range queries {
		want := []string{}
		for _, record := range l.terms {
			if record.Key.Scope == scope && record.ExpiresAt.After(now) && coverageMatches(record, q) {
				want = append(want, coverageTermString(record))
				coverageReceipts(t, record, now)
			}
		}
		slices.Sort(want)
		for _, result := range []r.CollabRecordBatchResult{a, b} {
			got := []string{}
			for _, record := range result.Dispositions[q].Terminated {
				got = append(got, coverageTermString(record))
			}
			slices.Sort(got)
			require.Equal(t, want, got, "every accepted termination, including unpublished reservations, must be returned")
		}
		got := []string{}
		for _, m := range native {
			if m.Kind == r.CollabRecordMutationTerminated && m.Terminated.ExpiresAt.After(now) && coverageMatches(m.Terminated, q) {
				got = append(got, coverageTermString(m.Terminated))
			}
		}
		slices.Sort(got)
		require.Equal(t, want, got, "native shared lookup differs from accepted ledger")
	}
	localRows := make(map[string]r.CollabRecordMutation)
	for field, payload := range local {
		var m r.CollabRecordMutation
		require.NoError(t, json.Unmarshal([]byte(payload), &m))
		localRows[field] = m
	}
	for _, rows := range []map[string]r.CollabRecordMutation{localRows, shared, native} {
		for token, expiry := range l.voids {
			if token.Scope != scope || !expiry.After(now) {
				continue
			}
			covered := false
			for _, m := range rows {
				if m.Kind == r.CollabRecordMutationVoid && (m.Token.Token == "" || m.Token == token) && !m.Issued.ExpiresAt.Before(expiry) {
					covered = true
				}
			}
			require.True(t, covered, "accepted invalidation lost its coverage: token=%s expiry=%s", token.Token, expiry)
		}
	}
	for _, result := range []r.CollabRecordBatchResult{a, b} {
		for token, expiry := range l.voids {
			if token.Scope != scope || !expiry.After(now) {
				continue
			}
			covered := false
			for _, returned := range result.Records[r.CollabRecordQuery{Key: key}].VoidedTokens {
				if returned.Token == "" || returned == token {
					covered = true
				}
			}
			require.True(t, covered, "complete lookup omitted an accepted invalidation")
		}
		for _, records := range result.Records {
			for _, issued := range records.IssuedTokens {
				for token, expiry := range l.voids {
					if token.Scope == scope && expiry.After(now) && (token.Token == "" || token == issued.Token) {
						require.NotEqual(t, r.CollabIssuedStatusConfirmed, issued.Record.Status)
					}
				}
			}
		}
	}
	s.operations["ledger_checks"]++
}

func TestCollabGeneratedInteractingCoverage(t *testing.T) {
	rounds := 8
	if v := os.Getenv("COLLAB_INVARIANT_STEPS"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err)
		rounds = max(rounds, n/64)
	}
	for _, seed := range []int64{7, 29, 101} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			s := newCollabSequence(t)
			rng := rand.New(rand.NewSource(seed))
			long := time.Now().UTC().Add(time.Hour)
			keys := []r.CollabDeliveryKey{capacityKey(0), capacityKey(1)}
			check := func() {
				for _, key := range keys {
					s.check(key)
				}
			}
			for _, key := range keys {
				s.ending(key, capacityDigest(400), long)
				check()
			}
			for step := 0; step < rounds; step++ {
				key := keys[rng.Intn(len(keys))]
				marker := r.CollabRecordMutation{Kind: r.CollabRecordMutationVoid, Token: r.CollabIssuedToken{Scope: key.Scope}, Issued: r.CollabIssuedRecord{ExpiresAt: long.Add(time.Duration(step) * time.Second)}}
				s.apply(marker)
				check()
				marker.Issued.ExpiresAt = marker.Issued.ExpiresAt.Add(time.Duration(1+rng.Intn(100)) * time.Second)
				s.apply(marker)
				check()
				// The failed write extends an already populated tree's horizon.
				fault := r.CollabRecordMutation{Kind: r.CollabRecordMutationPlaintext, Plaintext: r.CollabPlaintextRecord{Scope: key.Scope, Call: capacityDigest(100 + step), Arguments: capacityDigest(200 + step), ExpiresAt: long.Add(time.Duration(step+1) * time.Hour)}}
				s.partial(fault)
				check()
				// Several short lifetimes expire among long-lived reservations and
				// invalidations in the same scope. Writers share the same identity.
				expiry := time.Now().UTC().Add(150 * time.Millisecond)
				record := r.CollabTerminatedRecord{Key: key, Event: capacityDigest(500 + step), Copies: []r.CollabDigest{capacityDigest(800 + step)}, ExpiresAt: expiry}
				var wg sync.WaitGroup
				type accepted struct {
					local, remote bool
					mutation      r.CollabRecordMutation
				}
				results := make(chan accepted, 4)
				for w := 0; w < 4; w++ {
					wg.Go(func() {
						_, a := s.memory.ReserveLocalEnding(s.ctx, []r.CollabTerminatedRecord{record})
						_, b := s.shared.ReserveLocalEnding(s.ctx, []r.CollabTerminatedRecord{record})
						m := marker
						m.Issued.ExpiresAt = long.Add(time.Duration(200+step*4+w) * time.Second)
						ok := s.memory.ApplyMutation(m)
						err := s.shared.WriteInvalidations(s.ctx, []r.CollabRecordMutation{m})
						results <- accepted{a && ok, b && err == nil, m}
					})
				}
				wg.Wait()
				close(results)
				for got := range results {
					require.True(t, got.local)
					require.True(t, got.remote)
					s.ledger.reserve([]r.CollabTerminatedRecord{record})
					s.ledger.accept(got.mutation)
				}
				check()
				// A receipt from a different accepted reservation is still foreign.
				require.False(t, r.CollabTerminationEffective(record, key, []r.CollabDigest{capacityDigest(400)}, time.Now()))
				s.apply(r.CollabRecordMutation{Kind: r.CollabRecordMutationTerminated, Terminated: record})
				check()
				wait := time.Until(expiry) + 10*time.Millisecond
				if wait > 0 {
					time.Sleep(wait)
					s.server.FastForward(wait)
				}
				check()
				s.operations["interacting_expiry"]++
				s.operations["same_scope_writers"]++
			}
			t.Logf("seed=%d rounds=%d coverage=%v", seed, rounds, s.operations)
		})
	}
}
