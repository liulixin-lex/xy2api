//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCachePolicyM31M4OptionsDoNotBlockOtherPolicyPublication(t *testing.T) {
	accounts := &policyBlockedAccountOptions{entered: make(chan struct{}), release: make(chan struct{})}
	groups := &m4PolicyGroups{forbid: true}
	store := newPolicyTestStore()
	store.rows[cachePolicySettings[0]] = `{"rev":1,"spec":{"enabled":true}}`
	store.rows[cachePolicySettings[5]] = `{"rev":1,"spec":{"keep_sticky_on_busy":{"enabled":true,"group_ids":[999],"rollout_percent":20}}}`
	s := NewCachePolicyService(store, policyTestConfig(), WithCacheNorthstar(), WithCachePolicyAccounts(accounts), WithCacheM4Policies(groups))
	require.NoError(t, s.Refresh(context.Background()))
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(accounts.release) }) })
	type result struct {
		view CachePolicyView
		err  error
	}
	first := upstreamPolicyWrite(t, 0, `{"mode":"uuid_fill","rollout_percent":50,"max_inflight_per_key":4}`)
	firstDone := make(chan result, 1)
	go func() {
		view, _, err := s.Update(context.Background(), "upstream_identity", first, nil, CachePolicyActor{})
		firstDone <- result{view: view, err: err}
	}()
	select {
	case <-accounts.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("committed M31 write did not reach candidate lookup")
	}
	require.EqualValues(t, 1, s.Snapshot().revs[3])
	require.Equal(t, 4, s.Snapshot().UpstreamIdentity().MaxInflightPerKey)

	// The dormant M4 behavior remains reversible while M31 waits for options.
	rollback := policyWrite(t, `{"expected_rev":1,"spec":{"keep_sticky_on_busy":{"enabled":false}}}`)
	rollbackDone := make(chan result, 1)
	go func() {
		view, _, err := s.Update(context.Background(), "routing", rollback, nil, CachePolicyActor{})
		rollbackDone <- result{view: view, err: err}
	}()
	select {
	case got := <-rollbackDone:
		require.NoError(t, got.err)
		require.EqualValues(t, 2, got.view.Stored.Rev)
		require.Equal(t, "off", got.view.Effective.State)
		require.Contains(t, got.view.Effective.Warnings, cachePolicyGroupOptionsUnavailable)
	case <-time.After(5 * time.Second):
		t.Fatal("M4 rollback waited for unrelated M31 account options")
	}
	require.EqualValues(t, 1, s.Snapshot().revs[3])
	require.EqualValues(t, 2, s.Snapshot().revs[5])
	require.Equal(t, 4, s.Snapshot().UpstreamIdentity().MaxInflightPerKey)

	// A later M31 disable publishes without the older caller rolling rev back.
	disable := upstreamPolicyWrite(t, 1, `{"mode":"off","max_inflight_per_key":1}`)
	disableDone := make(chan error, 1)
	go func() {
		_, _, err := s.Update(context.Background(), "upstream_identity", disable, nil, CachePolicyActor{})
		disableDone <- err
	}()
	select {
	case err := <-disableDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("M31 disable waited for older account options")
	}
	release.Do(func() { close(accounts.release) })
	got := <-firstDone
	require.NoError(t, got.err)
	require.EqualValues(t, 1, got.view.Stored.Rev)
	require.Equal(t, 4, got.view.Stored.Spec["max_inflight_per_key"])
	require.Contains(t, got.view.Effective.Warnings, cachePolicyAccountOptionsUnavailable)
	require.Contains(t, got.view.Effective.Warnings, "l01_all_accounts_unverified")
	require.EqualValues(t, 2, s.Snapshot().revs[3])
	require.EqualValues(t, 2, s.Snapshot().revs[5])
	require.Equal(t, "off", s.Snapshot().UpstreamIdentity().Mode)
	require.Equal(t, 1, s.Snapshot().UpstreamIdentity().MaxInflightPerKey)
	require.Zero(t, groups.calls)
}
