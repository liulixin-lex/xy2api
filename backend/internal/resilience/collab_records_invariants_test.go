package resilience

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func invariantVoid(scope CollabRecordScope, token int, expiry time.Time) CollabRecordMutation {
	return CollabRecordMutation{Kind: CollabRecordMutationVoid, Token: CollabIssuedToken{Scope: scope, Call: capacityDigest(token + 100), Token: capacityDigest(token + 200)}, Issued: CollabIssuedRecord{ExpiresAt: expiry}}
}
func TestCollabGeneratedInvalidationQueue(t *testing.T) {
	for _, seed := range []int64{7, 29, 101} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := &SafeCollabRecordsStore{shared: &collabSharedTestStore{}, stop: ctx, cancel: cancel, wake: make(chan struct{}, 1), invalidations: make(map[CollabRecordScope][]CollabRecordMutation)}
			// A worker-free queue models a stalled store deterministically.
			expiry := time.Now().Add(time.Hour)
			order := rng.Perm(CollabRecordMaxTrees)
			for _, i := range order {
				s.deferInvalidation(invariantVoid(capacityKey(i).Scope, 0, expiry))
				require.LessOrEqual(t, len(s.invalidations), CollabRecordMaxTrees)
			}
			for attempt := 0; attempt < 24; attempt++ {
				scope := capacityKey(order[attempt]).Scope
				latest := expiry
				for token := 1; token <= 16; token++ {
					until := expiry.Add(time.Duration(rng.Intn(300)) * time.Second)
					if token == 16 {
						until = time.Now().Add(time.Minute)
					}
					if until.After(latest) {
						latest = until
					}
					before := CollabSafetySnapshot().RecordDropped
					s.deferInvalidation(invariantVoid(scope, token, until))
					require.Equal(t, before, CollabSafetySnapshot().RecordDropped, "replacement cannot consume a scope slot")
					rows := s.invalidations[scope]
					require.LessOrEqual(t, len(rows), 16)
					require.Len(t, s.invalidations, CollabRecordMaxTrees)
					if token == 16 {
						require.Len(t, rows, 1)
						require.Empty(t, rows[0].Token.Token)
						require.False(t, rows[0].Issued.ExpiresAt.Before(latest), "marker shortened its coverage")
					}
				}
			}
		})
	}
}
func TestCollabGeneratedLocalInvalidationHorizon(t *testing.T) {
	for _, seed := range []int64{7, 29, 101} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			s := NewMemoryCollabRecordsStore()
			scope := capacityKey(0).Scope
			latest := time.Now().Add(time.Hour)
			collapsed := false
			for i := 0; i < 120; i++ {
				until := latest.Add(time.Duration(rng.Intn(20)) * time.Second)
				m := invariantVoid(scope, i, until)
				_, payload, ok := NormalizeCollabRecordMutation(m, time.Now())
				require.True(t, ok)
				_, field, _ := CollabRecordStorageIdentity(m)
				tree := s.tree(scope, false, time.Now())
				used := 0
				if tree != nil {
					_, used = tree.partitionSize(0)
				}
				collapse := used+collabRecordCharge(field, payload) > CollabRecordEvidenceBytes
				if collapse {
					m.Issued.ExpiresAt = time.Now().Add(time.Minute)
				} else if until.After(latest) {
					latest = until
				}
				require.True(t, s.ApplyMutation(m))
				tree = s.tree(scope, false, time.Now())
				count, bytes := tree.partitionSize(0)
				require.LessOrEqual(t, count, 64)
				require.LessOrEqual(t, bytes, CollabRecordEvidenceBytes)
				if collapse {
					marker := tree.entries["v:"].mutation
					require.False(t, marker.Issued.ExpiresAt.Before(latest), "marker shortened earlier invalidations")
					collapsed = true
					break
				}
			}
			require.True(t, collapsed)
		})
	}
}
