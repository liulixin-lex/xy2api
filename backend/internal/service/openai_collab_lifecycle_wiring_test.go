//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type collabConstructionFaultCache struct {
	GatewayCache
	calls int
}

func TestCollabWiringAffinityConstructionFailure(t *testing.T) {
	original := newCollabAffinityStore
	t.Cleanup(func() { newCollabAffinityStore = original })
	calls := 0
	newCollabAffinityStore = func(int, time.Duration) *resilience.CollabTreeAffinity {
		calls++
		panic("synthetic affinity construction fault")
	}
	s := &OpenAIGatewayService{}
	before := collabConstructionCount()
	for i := 0; i < 3; i++ {
		require.NotPanics(t, func() { require.Nil(t, s.CollabTreeAffinity()) })
	}
	require.Equal(t, 1, calls)
	require.Equal(t, before+1, collabConstructionCount())
	require.Nil(t, s.CollabRecords())
}

type collabContinuationTestCache struct {
	GatewayCache
	store             CollabContinuationStore
	panicConstruction bool
}

func (c collabContinuationTestCache) CollabContinuations() CollabContinuationStore {
	if c.panicConstruction {
		panic("synthetic continuation construction fault")
	}
	return c.store
}

func TestCollabWiringContinuationAssembly(t *testing.T) {
	for _, fault := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "construction_fault"}[fault], func(t *testing.T) {
			shared := &collabMemoryAliases{}
			cache := collabContinuationTestCache{store: shared, panicConstruction: fault}
			s := NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, cache, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			t.Cleanup(s.StopOpenAICodexTicketHarvester)
			t.Cleanup(s.CloseCollabRecords)
			require.Equal(t, !fault, s.CanContinueCollabResponse(context.Background()))
			if fault {
				return
			}
			scope := CollabContinuationScope{Group: 1, User: 2}
			alias := CollabContinuationAlias{Account: 3, Upstream: "resp_upstream", Expires: time.Now().Add(time.Hour)}
			require.NoError(t, s.collabAliasStore().Put(context.Background(), scope, "resp_client", alias))
			other := &OpenAIGatewayService{cache: cache}
			other.initCollabContinuation()
			got, ok, err := other.collabAliasStore().Get(context.Background(), scope, "resp_client")
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, alias, got)
		})
	}
}

func (c *collabConstructionFaultCache) CollabRecords() resilience.CollabSharedRecordsStore {
	c.calls++
	panic("synthetic store construction fault")
}

func collabConstructionCount() int64 {
	var count int64
	for _, row := range resilience.Snapshot(time.Hour) {
		if row.Name == "collab_construction_failed" {
			count += row.Count
		}
	}
	return count
}

func TestCollabWiringConstructionFailure(t *testing.T) {
	cache := &collabConstructionFaultCache{}
	s := &OpenAIGatewayService{cache: cache}
	before := collabConstructionCount()
	for i := 0; i < 3; i++ {
		require.NotPanics(t, func() { require.Nil(t, s.CollabRecords()) })
	}
	require.Equal(t, 1, cache.calls, "construction failure disables further attempts")
	require.Equal(t, before+1, collabConstructionCount())
	require.NotPanics(t, s.CloseCollabRecords)
}

func TestCollabWiringStoreLifecycle(t *testing.T) {
	s := &OpenAIGatewayService{}
	var wg sync.WaitGroup
	stores := make(chan *resilience.SafeCollabRecordsStore, 8)
	affinities := make(chan *resilience.CollabTreeAffinity, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); stores <- s.CollabRecords(); affinities <- s.CollabTreeAffinity() }()
	}
	wg.Wait()
	close(stores)
	close(affinities)
	for store := range stores {
		require.Same(t, s.CollabRecords(), store)
	}
	for affinity := range affinities {
		require.Same(t, s.CollabTreeAffinity(), affinity)
	}
	s.CloseCollabRecords()
	s.CloseCollabRecords()
	require.Nil(t, s.CollabRecords())
	require.Nil(t, s.CollabTreeAffinity())
}
