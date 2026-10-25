//go:build unit

package service

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnchorV2InflightLimitReleaseAndCapacity(t *testing.T) {
	c := newCacheAnchorInflight(1)
	a, ok := c.acquire("a2:synthetic-A", 2)
	require.True(t, ok)
	b, ok := c.acquire("a2:synthetic-A", 2)
	require.True(t, ok)
	_, ok = c.acquire("a2:synthetic-A", 2)
	require.False(t, ok)
	_, ok = c.acquire("a2:synthetic-B", 2)
	require.False(t, ok, "map capacity fails closed")
	a.release()
	a.release()
	require.Equal(t, 1, c.entries["a2:synthetic-A"], "release is idempotent")
	b.release()
	require.Empty(t, c.entries)
	lease, ok := c.acquire("a2:synthetic-B", 2)
	require.True(t, ok, "zero count deletes the key")
	lease.release()
	require.Empty(t, c.entries)
}

func TestAnchorV2InflightFaultsFailClosed(t *testing.T) {
	var absent *cacheAnchorInflight
	_, ok := absent.acquire("a2:synthetic", 2)
	require.False(t, ok)
	for _, capacity := range []int{-1, 0} {
		_, ok := newCacheAnchorInflight(capacity).acquire("a2:synthetic", 2)
		require.False(t, ok)
	}
	c := newCacheAnchorInflight(2)
	for _, limit := range []int{0, -1, 17} {
		_, ok = c.acquire("a2:synthetic", limit)
		require.False(t, ok)
	}
	require.Nil(t, c.entries, "invalid input does not allocate a map")
	c.entries = map[string]int{"a2:synthetic": 0}
	_, ok = c.acquire("a2:synthetic", 2)
	require.False(t, ok)
	require.True(t, c.poisoned)
	_, ok = c.acquire("a2:other", 2)
	require.False(t, ok, "abnormal counters disable all future injection")
	c = newCacheAnchorInflight(2)
	lease, ok := c.acquire("a2:synthetic", 2)
	require.True(t, ok)
	delete(c.entries, "a2:synthetic") // fault injection before release
	lease.release()
	require.True(t, c.poisoned)
	require.Equal(t, cacheAnchorInflightHardCap, newCacheAnchorInflight(cacheAnchorInflightHardCap+1).capacity)
}

func TestAnchorV2InflightConcurrentFanIn(t *testing.T) {
	c := newCacheAnchorInflight(32)
	leases := make(chan *cacheAnchorLease, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if lease, ok := c.acquire("a2:synthetic", 2); ok {
				leases <- lease
			}
		}()
	}
	wg.Wait()
	close(leases)
	require.Len(t, leases, 2, "exactly the default two requests may use one anchor")
	for lease := range leases {
		lease.release()
	}
	require.Empty(t, c.entries)
	require.False(t, c.poisoned)
}
