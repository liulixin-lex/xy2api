package resilience

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollabGeneratedInvalidationUpdates(t *testing.T) {
	for _, whole := range []bool{false, true} {
		s := NewMemoryCollabRecordsStore()
		m := invariantVoid(capacityKey(0).Scope, 1, time.Now().Add(time.Minute))
		if whole {
			m.Token = CollabIssuedToken{Scope: m.Token.Scope}
		}
		long := time.Now().Add(time.Hour)
		for _, expiry := range []time.Time{m.Issued.ExpiresAt, long, time.Now().Add(time.Second)} {
			m.Issued.ExpiresAt = expiry
			require.True(t, s.ApplyMutation(m))
		}
		tree := s.tree(m.Token.Scope, false, time.Now())
		_, field, _ := CollabRecordStorageIdentity(m)
		require.True(t, tree.entries[field].mutation.Issued.ExpiresAt.Equal(long))
		require.GreaterOrEqual(t, tree.protectedUntil.Load(), long.UnixNano())
		require.GreaterOrEqual(t, tree.expires.Load(), long.UnixNano())
		payload, err := json.Marshal(tree.entries[field].mutation)
		require.NoError(t, err)
		require.Equal(t, len(payload)+3*len(field)+32, tree.bytes)
	}
}

func TestCollabGeneratedInvalidationQueueUpdatesDuringFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &SafeCollabRecordsStore{stop: ctx, cancel: cancel, wake: make(chan struct{}, 1), invalidations: make(map[CollabRecordScope][]CollabRecordMutation)}
	long := time.Now().Add(time.Hour)
	var writes []CollabRecordMutation
	s.shared = &collabSharedTestStore{apply: func(_ context.Context, m CollabRecordMutation) error {
		writes = append(writes, m)
		if len(writes) == 1 {
			// A newer collapse arrives while the older marker is in flight.
			m.Issued.ExpiresAt = long
			s.deferInvalidation(m)
			m.Issued.ExpiresAt = time.Now().Add(time.Second)
			s.deferInvalidation(m)
		}
		return nil
	}}
	scope := capacityKey(0).Scope
	for i := 0; i < 17; i++ {
		s.deferInvalidation(invariantVoid(scope, i, time.Now().Add(time.Minute)))
	}
	require.Len(t, s.invalidations[scope], 1)
	s.flushInvalidations(ctx)
	require.Len(t, writes, 2)
	require.Empty(t, writes[1].Token.Token)
	require.True(t, writes[1].Issued.ExpiresAt.Equal(long))
	require.Empty(t, s.invalidations)
}
