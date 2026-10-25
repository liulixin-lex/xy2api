//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type cachePolicyStaleClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *cachePolicyStaleClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *cachePolicyStaleClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func cachePolicyStaleFixture(t *testing.T, maxStaleSeconds int) (*CachePolicyService, *policyTestStore, *cachePolicyStaleClock) {
	t.Helper()
	repo := newPolicyTestStore()
	repo.rows[cachePolicySettings[0]] = `{"rev":1,"spec":{"enabled":true}}`
	repo.rows[cachePolicySettings[1]] = `{"rev":2,"spec":{"enabled":true}}`
	repo.rows[cachePolicySettings[2]] = `{"rev":1,"spec":{"rollup_enabled":true}}`
	repo.rows[cachePolicySettings[3]] = `{"rev":3,"spec":{"mode":"uuid_fill","rollout_percent":100}}`
	clock := &cachePolicyStaleClock{now: time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)}
	cfg := policyTestConfig()
	cfg.Gateway.CachePolicy.MaxStaleSeconds = maxStaleSeconds
	s := NewCachePolicyService(repo, cfg, WithCacheNorthstar(), WithCachePolicyClock(clock.Now))
	require.NoError(t, s.Refresh(context.Background()))
	p := s.Snapshot()
	require.True(t, p.MasterEnabled())
	require.True(t, p.TraceEnabled())
	require.True(t, p.NorthstarEnabled())
	require.True(t, p.UpstreamIdentity().Enabled())
	require.False(t, p.behaviorStale())
	return s, repo, clock
}

func setPolicyTestReadErr(repo *policyTestStore, err error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.readErr = err
}

func cachePolicyListView(t *testing.T, s *CachePolicyService, key string) CachePolicyView {
	t.Helper()
	list, err := s.List(context.Background(), "test")
	require.NoError(t, err)
	for _, view := range list.Policies {
		if view.Key == key {
			return view
		}
	}
	t.Fatalf("missing policy %s", key)
	return CachePolicyView{}
}

func TestCachePolicyStaleSnapshotBoundsBehaviorPolicies(t *testing.T) {
	ctx := context.Background()
	s, repo, clock := cachePolicyStaleFixture(t, 900)
	fresh := s.Snapshot()
	setPolicyTestReadErr(repo, errors.New("settings unavailable"))

	// Exactly at the bound the last valid snapshot is kept, as before.
	clock.Advance(900 * time.Second)
	require.Error(t, s.Refresh(ctx))
	require.Same(t, fresh, s.Snapshot())

	clock.Advance(time.Second)
	require.Error(t, s.Refresh(ctx))
	stale := s.Snapshot()
	require.NotSame(t, fresh, stale)
	require.True(t, stale.behaviorStale())
	require.False(t, stale.UpstreamIdentity().Enabled())
	require.EqualValues(t, 3, stale.UpstreamIdentity().Revision())
	// Master, observability, northstar and revisions are untouched.
	require.True(t, stale.MasterEnabled())
	require.True(t, stale.TraceEnabled())
	require.True(t, stale.PersistTraceHourlyEnabled())
	require.True(t, stale.NorthstarEnabled())
	require.Equal(t, fresh.Revision(), stale.Revision())
	require.Equal(t, fresh.revs, stale.revs)
	// Published snapshots stay immutable; requests holding them are unaffected.
	require.True(t, fresh.UpstreamIdentity().Enabled())
	require.False(t, fresh.behaviorStale())

	// Published once per outage.
	clock.Advance(time.Hour)
	require.Error(t, s.Refresh(ctx))
	require.Same(t, stale, s.Snapshot())
	require.EqualValues(t, 1, s.staleTotal)

	// Another instance can still read settings: behavior keys report the bound.
	setPolicyTestReadErr(repo, nil)
	upstream := cachePolicyListView(t, s, "upstream_identity")
	require.Equal(t, "blocked", upstream.Effective.State)
	require.False(t, *upstream.Effective.Active)
	require.Equal(t, []string{"stale_snapshot"}, upstream.Effective.Blockers)
	for _, key := range []string{"master", "observability", "northstar"} {
		view := cachePolicyListView(t, s, key)
		require.Equal(t, "on", view.Effective.State, key)
		require.NotContains(t, view.Effective.Blockers, "stale_snapshot", key)
	}
	for _, key := range []string{"anthropic_identity", "routing"} {
		view := cachePolicyListView(t, s, key)
		require.Equal(t, "not_in_build", view.Effective.State, key)
		require.Empty(t, view.Effective.Blockers, key)
	}
	require.Same(t, stale, s.Snapshot(), "admin reads never refresh the snapshot")

	// A successful refresh restores the stored behavior.
	require.NoError(t, s.Refresh(ctx))
	recovered := s.Snapshot()
	require.False(t, recovered.behaviorStale())
	require.True(t, recovered.UpstreamIdentity().Enabled())
	require.Equal(t, "on", cachePolicyListView(t, s, "upstream_identity").Effective.State)

	// The bound restarts from the latest successful refresh.
	setPolicyTestReadErr(repo, errors.New("settings unavailable"))
	clock.Advance(900 * time.Second)
	require.Error(t, s.Refresh(ctx))
	require.Same(t, recovered, s.Snapshot())
	clock.Advance(time.Second)
	require.Error(t, s.Refresh(ctx))
	require.False(t, s.Snapshot().UpstreamIdentity().Enabled())
	require.EqualValues(t, 2, s.staleTotal)
}

func TestCachePolicyStaleWritesKeepFailSafeUntilRefresh(t *testing.T) {
	ctx := context.Background()
	s, repo, clock := cachePolicyStaleFixture(t, 60)
	setPolicyTestReadErr(repo, errors.New("settings unavailable"))
	clock.Advance(61 * time.Second)
	require.Error(t, s.Refresh(ctx))
	stale := s.Snapshot()
	require.False(t, stale.UpstreamIdentity().Enabled())

	// A failed write returns its error and publishes nothing.
	repo.mu.Lock()
	repo.writeErr = errors.New("write unavailable")
	repo.mu.Unlock()
	_, _, err := s.Update(ctx, "observability", policyWrite(t, `{"expected_rev":2,"spec":{"sample_percent":50}}`), nil, CachePolicyActor{})
	require.Error(t, err)
	require.Same(t, stale, s.Snapshot())

	// A successful write publishes as before but cannot lift the bound.
	repo.mu.Lock()
	repo.writeErr = nil
	repo.mu.Unlock()
	_, _, err = s.Update(ctx, "observability", policyWrite(t, `{"expected_rev":2,"spec":{"sample_percent":50}}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	written := s.Snapshot()
	require.NotSame(t, stale, written)
	require.Equal(t, 50, written.SamplePercent())
	require.True(t, written.behaviorStale())
	require.False(t, written.UpstreamIdentity().Enabled())
	v, _, err := s.Update(ctx, "upstream_identity", upstreamPolicyWrite(t, 3, `{"rollout_percent":50}`), nil, CachePolicyActor{})
	require.NoError(t, err)
	require.Equal(t, "blocked", v.Effective.State)
	require.Equal(t, []string{"stale_snapshot"}, v.Effective.Blockers)
	require.False(t, s.Snapshot().UpstreamIdentity().Enabled())

	setPolicyTestReadErr(repo, nil)
	require.NoError(t, s.Refresh(ctx))
	require.True(t, s.Snapshot().UpstreamIdentity().Enabled())
	require.Equal(t, 50, s.Snapshot().UpstreamIdentity().RolloutPercent)
}

func TestCachePolicyStaleValidationPreview(t *testing.T) {
	ctx := context.Background()
	s, repo, clock := cachePolicyStaleFixture(t, 60)
	setPolicyTestReadErr(repo, errors.New("settings unavailable"))
	clock.Advance(61 * time.Second)
	require.Error(t, s.Refresh(ctx))
	setPolicyTestReadErr(repo, nil)

	v, err := s.Validate(ctx, "upstream_identity", upstreamPolicyWrite(t, 3, `{"rollout_percent":40}`), nil)
	require.NoError(t, err)
	require.True(t, v.Valid)
	require.Equal(t, "blocked", v.EffectivePreview.State)
	require.False(t, *v.EffectivePreview.Active)
	require.Equal(t, []string{"stale_snapshot"}, v.EffectivePreview.Blockers)

	v, err = s.Validate(ctx, "master", policyWrite(t, `{"expected_rev":1,"spec":{"enabled":false}}`), nil)
	require.NoError(t, err)
	require.Equal(t, "off", v.EffectivePreview.State, "master itself is not bounded")
	for _, affected := range v.Affected {
		if affected.Key == "upstream_identity" {
			require.Equal(t, "blocked", affected.StateBefore)
			require.Equal(t, "blocked", affected.StateAfter)
			require.False(t, *affected.ActiveAfter)
		}
	}
}

func TestCachePolicyStaleBoundZeroKeepsLastSnapshot(t *testing.T) {
	ctx := context.Background()
	s, repo, clock := cachePolicyStaleFixture(t, 0)
	fresh := s.Snapshot()
	setPolicyTestReadErr(repo, errors.New("settings unavailable"))
	clock.Advance(30 * 24 * time.Hour)
	require.Error(t, s.Refresh(ctx))
	require.Same(t, fresh, s.Snapshot())
	require.True(t, s.Snapshot().UpstreamIdentity().Enabled())
	require.Zero(t, s.staleTotal)

	// A nil config, as in existing fixtures, is also unbounded.
	unbounded := NewCachePolicyService(repo, nil)
	require.Zero(t, unbounded.maxStale)
}

func TestCachePolicyStaleSnapshotDisabledAccessorsZeroAlloc(t *testing.T) {
	ctx := context.Background()
	s, repo, clock := cachePolicyStaleFixture(t, 60)
	setPolicyTestReadErr(repo, errors.New("settings unavailable"))
	clock.Advance(61 * time.Second)
	require.Error(t, s.Refresh(ctx))
	var enabled bool
	allocs := testing.AllocsPerRun(1000, func() {
		p := s.Snapshot()
		enabled = p.UpstreamIdentity().Enabled() || p.MasterEnabled() && p.behaviorStale() && !p.TraceEnabled()
	})
	require.Zero(t, allocs)
	require.False(t, enabled)
}

func TestCachePolicyStaleEffectiveOrderAndStates(t *testing.T) {
	stale := &CachePolicySnapshot{stale: true}
	fresh := &CachePolicySnapshot{}
	effective := func(state string, blockers ...string) CachePolicyEffective {
		active := state == "on"
		return CachePolicyEffective{State: state, Active: &active, Blockers: append([]string{}, blockers...), Warnings: []string{}, ClippedFields: []string{}}
	}
	cases := []struct {
		in           CachePolicyEffective
		state        string
		blockers     []string
		unchangedFor *CachePolicySnapshot
	}{
		{effective("on"), "blocked", []string{"stale_snapshot"}, nil},
		{effective("masked_by_master"), "blocked", []string{"stale_snapshot"}, nil},
		{effective("pending_reload"), "blocked", []string{"stale_snapshot"}, nil},
		{effective("off"), "blocked", []string{"stale_snapshot"}, nil},
		{effective("killed", "kill_switch"), "killed", []string{"kill_switch", "stale_snapshot"}, nil},
		{effective("parse_error", "kill_switch", "parse_error", "not_implemented_in_build"), "parse_error", []string{"kill_switch", "parse_error", "stale_snapshot", "not_implemented_in_build"}, nil},
		{effective("blocked", "not_implemented_in_build", "guard_tripped"), "blocked", []string{"stale_snapshot", "not_implemented_in_build", "guard_tripped"}, nil},
	}
	for _, tc := range cases {
		for _, i := range []int{3, 4, 5} {
			got := cachePolicyStaleEffective(i, stale, tc.in)
			require.Equal(t, tc.state, got.State)
			require.Equal(t, tc.blockers, got.Blockers)
			require.False(t, *got.Active)
		}
		for _, i := range []int{0, 1, 2} {
			require.Equal(t, tc.in, cachePolicyStaleEffective(i, stale, tc.in))
		}
		require.Equal(t, tc.in, cachePolicyStaleEffective(3, fresh, tc.in))
		require.Equal(t, tc.in, cachePolicyStaleEffective(3, nil, tc.in))
	}
	notInBuild := CachePolicyEffective{State: "not_in_build", Blockers: []string{}, Warnings: []string{}, ClippedFields: []string{}}
	require.Equal(t, notInBuild, cachePolicyStaleEffective(4, stale, notInBuild))
}
