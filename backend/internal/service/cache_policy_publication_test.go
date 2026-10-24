//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

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
