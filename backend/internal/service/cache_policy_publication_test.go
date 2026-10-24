//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type policyReadOnlyStore struct{ SettingRepository }

func (policyReadOnlyStore) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func TestCachePolicyUpdateWithoutTransactionStoreReturnsError(t *testing.T) {
	s := NewCachePolicyService(policyReadOnlyStore{}, policyTestConfig())
	write := policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true}}`)
	require.NotPanics(t, func() {
		_, _, err := s.Update(context.Background(), "observability", write, nil, CachePolicyActor{})
		require.EqualError(t, err, "cache policy transaction store unavailable")
	})
	require.False(t, s.Snapshot().TraceEnabled())
}

type policyDelayedCommitStore struct {
	*policyTestStore
	once      sync.Once
	committed chan struct{}
	release   chan struct{}
}

func (r *policyDelayedCommitStore) UpdateCachePolicy(ctx context.Context, key string, fn func(string) (string, error)) error {
	err := r.policyTestStore.UpdateCachePolicy(ctx, key, fn)
	first := false
	r.once.Do(func() { first = true })
	if first {
		close(r.committed)
		<-r.release
	}
	return err
}

func TestCachePolicySlowCommitDoesNotBlockSubscriptionOrRegressRevision(t *testing.T) {
	r := &policyDelayedCommitStore{policyTestStore: newPolicyTestStore(), committed: make(chan struct{}), release: make(chan struct{})}
	s := NewCachePolicyService(r, policyTestConfig())
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(r.release) }) })
	first := policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true}}`)
	second := policyWrite(t, `{"expected_rev":1,"spec":{"enabled":false}}`)
	done := make(chan error, 1)
	go func() {
		_, _, err := s.Update(context.Background(), "observability", first, nil, CachePolicyActor{})
		done <- err
	}()
	select {
	case <-r.committed:
	case <-time.After(time.Second):
		t.Fatal("first write did not commit")
	}
	subscribed := make(chan struct{})
	go func() { s.subscribe(func(*CachePolicySnapshot) {}); close(subscribed) }()
	select {
	case <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("subscription waited for storage")
	}
	_, _, err := s.Update(context.Background(), "observability", second, nil, CachePolicyActor{})
	require.NoError(t, err)
	require.EqualValues(t, 2, s.Snapshot().revs[1])
	release.Do(func() { close(r.release) })
	require.NoError(t, <-done)
	require.EqualValues(t, 2, s.Snapshot().revs[1])
	require.False(t, s.Snapshot().TraceEnabled())
}

type policyDelayedReadStore struct {
	*policyTestStore
	read    chan struct{}
	release chan struct{}
}

func (r *policyDelayedReadStore) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	rows, err := r.policyTestStore.GetMultiple(ctx, keys)
	close(r.read)
	<-r.release
	return rows, err
}

func TestCachePolicyRefreshDoesNotOverwriteConcurrentWrite(t *testing.T) {
	r := &policyDelayedReadStore{policyTestStore: newPolicyTestStore(), read: make(chan struct{}), release: make(chan struct{})}
	s := NewCachePolicyService(r, policyTestConfig())
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(r.release) }) })
	done := make(chan error, 1)
	go func() { done <- s.Refresh(context.Background()) }()
	select {
	case <-r.read:
	case <-time.After(time.Second):
		t.Fatal("refresh did not read")
	}
	write := policyWrite(t, `{"expected_rev":0,"spec":{"enabled":true}}`)
	_, _, err := s.Update(context.Background(), "observability", write, nil, CachePolicyActor{})
	require.NoError(t, err)
	release.Do(func() { close(r.release) })
	require.NoError(t, <-done)
	require.True(t, s.Snapshot().TraceEnabled())
	require.EqualValues(t, 1, s.Snapshot().revs[1])
}

type policyBlockedAccountOptions struct {
	CachePolicyAccountLookup
	entered chan struct{}
	release chan struct{}
}

func (r *policyBlockedAccountOptions) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, string, int64, string) ([]Account, *pagination.PaginationResult, error) {
	close(r.entered)
	<-r.release
	return nil, nil, errors.New("options unavailable")
}

func TestCachePolicySlowAccountOptionsDoesNotBlockPublication(t *testing.T) {
	accounts := &policyBlockedAccountOptions{entered: make(chan struct{}), release: make(chan struct{})}
	s := NewCachePolicyService(newPolicyTestStore(), policyTestConfig(), WithCachePolicyAccounts(accounts))
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(accounts.release) }) })
	type result struct {
		view CachePolicyView
		err  error
	}
	done := make(chan result, 1)
	first := policyWrite(t, `{"expected_rev":0,"spec":{"mode":"uuid_fill","rollout_percent":50},"reason":"enable","confirm":{"checkbox":true}}`)
	go func() {
		view, _, err := s.Update(context.Background(), "upstream_identity", first, nil, CachePolicyActor{})
		done <- result{view, err}
	}()
	select {
	case <-accounts.entered:
	case <-time.After(time.Second):
		t.Fatal("committed update did not reach candidate lookup")
	}
	require.EqualValues(t, 1, s.Snapshot().revs[3])
	subscribed := make(chan struct{})
	go func() { s.subscribe(func(*CachePolicySnapshot) {}); close(subscribed) }()
	select {
	case <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("subscription waited for account lookup")
	}
	second := policyWrite(t, `{"expected_rev":1,"spec":{"mode":"off"}}`)
	disabled := make(chan error, 1)
	go func() {
		_, _, err := s.Update(context.Background(), "upstream_identity", second, nil, CachePolicyActor{})
		disabled <- err
	}()
	select {
	case err := <-disabled:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("rollback waited for account lookup")
	}
	require.EqualValues(t, 2, s.Snapshot().revs[3])
	release.Do(func() { close(accounts.release) })
	firstResult := <-done
	require.NoError(t, firstResult.err)
	require.EqualValues(t, 1, firstResult.view.Stored.Rev)
	require.Contains(t, firstResult.view.Effective.Warnings, cachePolicyAccountOptionsUnavailable)
	require.EqualValues(t, 2, s.Snapshot().revs[3])
	require.Equal(t, "off", s.Snapshot().UpstreamIdentity().Mode)
}
