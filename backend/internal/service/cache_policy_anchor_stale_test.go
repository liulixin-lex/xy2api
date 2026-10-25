//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A-T22-1: the L-04 anchor and L-03 metadata accessors AND the published
// FX-POL-2 stale flag, so a stale-bounded snapshot returns both to baseline.
func TestCacheAnchorMetadataStaleSnapshotFailsSafe(t *testing.T) {
	ctx := context.Background()
	repo := newPolicyTestStore()
	repo.rows[cachePolicySettings[0]] = `{"rev":1,"spec":{"enabled":true}}`
	repo.rows[cachePolicySettings[4]] = `{"rev":2,"spec":{"anchor_group_ids":[31],"metadata_group_ids":[31],"rollout_percent":100,"metadata_rollout_percent":50}}`
	clock := &cachePolicyStaleClock{now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	cfg := policyTestConfig()
	cfg.Gateway.CachePolicy.MaxStaleSeconds = 60
	s := NewCachePolicyService(repo, cfg, WithCacheNorthstar(), WithCacheM4Policies(nil),
		WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{L04: true, L03: true}), WithCachePolicyClock(clock.Now))
	require.NoError(t, s.Refresh(ctx))
	fresh := s.Snapshot()
	require.False(t, fresh.behaviorStale())
	require.True(t, fresh.cacheAnchorEnabled())
	require.True(t, fresh.cacheMetadataEnabled())

	setPolicyTestReadErr(repo, errors.New("settings unavailable"))
	clock.Advance(60 * time.Second)
	require.Error(t, s.Refresh(ctx))
	require.Same(t, fresh, s.Snapshot(), "within the bound the last snapshot is kept")
	clock.Advance(time.Second)
	require.Error(t, s.Refresh(ctx))
	stale := s.Snapshot()
	require.True(t, stale.behaviorStale())
	require.True(t, stale.MasterEnabled(), "master is not bounded")
	require.False(t, stale.cacheAnchorEnabled())
	require.False(t, stale.cacheMetadataEnabled())
	require.Equal(t, fresh.revs, stale.revs)
	// Requests holding the earlier snapshot keep it.
	require.True(t, fresh.cacheAnchorEnabled())
	require.True(t, fresh.cacheMetadataEnabled())

	setPolicyTestReadErr(repo, nil)
	require.NoError(t, s.Refresh(ctx))
	recovered := s.Snapshot()
	require.False(t, recovered.behaviorStale())
	require.True(t, recovered.cacheAnchorEnabled())
	require.True(t, recovered.cacheMetadataEnabled())
}
