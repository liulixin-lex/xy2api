//go:build unit

package service

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCachePolicyCohortViewsReadyBeforePublication(t *testing.T) {
	s := NewCachePolicyService(nil, nil)
	check := func(p *CachePolicySnapshot) {
		require.NotNil(t, p)
		require.Same(t, p, p.cohortViews[0].snapshot)
		require.Same(t, p, p.cohortViews[1].snapshot)
		require.False(t, p.cohortViews[0].treatment)
		require.True(t, p.cohortViews[1].treatment)
	}
	check(s.Snapshot())
	observed := 0
	s.subscribe(func(p *CachePolicySnapshot) {
		check(p)
		observed++
	})
	s.publish(map[string]string{})
	require.Equal(t, 2, observed, "initial subscription and publication both expose initialized views")
}

func TestCachePolicyCohortPreservesSnapshotUntilNextTurn(t *testing.T) {
	for _, treatment := range []bool{false, true} {
		name := "control"
		if treatment {
			name = "treatment"
		}
		t.Run(name, func(t *testing.T) {
			s := NewCachePolicyService(nil, nil)
			c := &gin.Context{Keys: map[string]any{"synthetic_auth": true}}
			original := CaptureCachePolicy(c, s)
			keyCount := len(c.Keys)
			value, _ := c.Get(cachePolicySnapshotGinKey)
			require.Same(t, original, value, "uncaptured cohort keeps the original fast path")
			_, fixed := cachePolicyL01CohortFromGin(c)
			require.False(t, fixed)

			setCachePolicyL01Cohort(c, original, treatment)
			got, fixed := cachePolicyL01CohortFromGin(c)
			require.True(t, fixed)
			require.Equal(t, treatment, got)
			require.Len(t, c.Keys, keyCount)
			require.NotContains(t, c.Keys, "openaiL01Cohort")
			require.Same(t, original, CachePolicyFromGin(c))
			require.Same(t, original, CaptureCachePolicy(c, s))

			s.publish(map[string]string{cachePolicySettings[0]: `{"rev":1,"spec":{"enabled":true}}`})
			current := s.Snapshot()
			require.NotSame(t, original, current)
			require.Same(t, original, CaptureCachePolicy(c, s), "publisher updates cannot replace a captured request")
			require.Same(t, original, CachePolicyFromGin(c))
			got, fixed = cachePolicyL01CohortFromGin(c)
			require.True(t, fixed)
			require.Equal(t, treatment, got)

			require.Same(t, current, CaptureCachePolicyTurn(c, s))
			value, _ = c.Get(cachePolicySnapshotGinKey)
			require.Same(t, current, value, "a new turn restores the plain current snapshot")
			_, fixed = cachePolicyL01CohortFromGin(c)
			require.False(t, fixed)
			setCachePolicyL01Cohort(c, current, !treatment)
			got, fixed = cachePolicyL01CohortFromGin(c)
			require.True(t, fixed)
			require.Equal(t, !treatment, got)
			require.Same(t, current, CachePolicyFromGin(c))
			require.Len(t, c.Keys, keyCount)
			require.False(t, original.cohortViews[0].treatment)
			require.True(t, original.cohortViews[1].treatment)
		})
	}
}

func TestCachePolicyCohortViewsSharedAcrossRequests(t *testing.T) {
	p := NewCachePolicyService(nil, nil).Snapshot()
	var wg sync.WaitGroup
	var failed atomic.Bool
	for i := 0; i < 64; i++ {
		c := &gin.Context{Keys: map[string]any{cachePolicySnapshotGinKey: p}}
		treatment := i%2 == 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				setCachePolicyL01Cohort(c, p, treatment)
				got, fixed := cachePolicyL01CohortFromGin(c)
				if !fixed || got != treatment || CachePolicyFromGin(c) != p || CaptureCachePolicy(c, nil) != p {
					failed.Store(true)
				}
			}
		}()
	}
	wg.Wait()
	require.False(t, failed.Load())
	require.False(t, p.cohortViews[0].treatment)
	require.True(t, p.cohortViews[1].treatment)
}

func TestCachePolicyCohortNilAndInvalidSnapshotValues(t *testing.T) {
	require.Nil(t, CachePolicyFromGin(nil))
	require.Nil(t, CaptureCachePolicy(nil, nil))
	_, fixed := cachePolicyL01CohortFromGin(nil)
	require.False(t, fixed)
	setCachePolicyL01Cohort(nil, nil, false)
	for _, value := range []any{(*CachePolicySnapshot)(nil), (*cachePolicyCohortView)(nil), "unexpected"} {
		c := &gin.Context{Keys: map[string]any{cachePolicySnapshotGinKey: value}}
		require.Nil(t, CachePolicyFromGin(c))
		require.Nil(t, CaptureCachePolicy(c, nil))
		_, fixed = cachePolicyL01CohortFromGin(c)
		require.False(t, fixed)
	}
}
