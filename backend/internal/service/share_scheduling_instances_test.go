//go:build unit

package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// shareBusStore shares one memory store between instances and delivers
// broadcasts to every subscriber, like the Redis channel.
type shareBusStore struct {
	*shareAdminStore
	mu       sync.Mutex
	handlers []func(int64)
}

func (b *shareBusStore) PublishChange(ctx context.Context, groupID int64) error {
	if err := b.shareAdminStore.PublishChange(ctx, groupID); err != nil {
		return err
	}
	b.mu.Lock()
	handlers := append([]func(int64){}, b.handlers...)
	b.mu.Unlock()
	for _, handle := range handlers {
		go handle(groupID)
	}
	return nil
}

func (b *shareBusStore) SubscribeChanges(_ context.Context, handler func(int64)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = append(b.handlers, handler)
}

// newShareInstancePair builds two gateway instances that share the policy
// table, settings, Redis bindings, concurrency and broadcasts.
func newShareInstancePair(t *testing.T, accounts ...Account) (a, b *shareAdminEnv) {
	t.Helper()
	a = newShareAdminEnv(t, accounts...)
	b = newShareAdminEnv(t, accounts...)
	bus := &shareBusStore{shareAdminStore: a.store}
	for _, env := range []*shareAdminEnv{a, b} {
		env.share.repo, env.share.store, env.share.settings = a.policies, bus, a.settings
		env.gateway.cache = a.cache
		env.gateway.concurrencyService = a.gateway.concurrencyService
		env.share.Start()
		t.Cleanup(env.share.Stop)
	}
	b.policies, b.store, b.settings, b.cache, b.conc = a.policies, a.store, a.settings, a.cache, a.conc
	return a, b
}

func TestShareSchedulingInstancesStayConsistent(t *testing.T) {
	a, b := newShareInstancePair(t, Account{ID: 1}, Account{ID: 2}, Account{ID: 3})
	require.Nil(t, b.share.groupPolicy(shareTestGroupID), "nothing is stored yet")
	// Load factors 5:3:2 in one priority, on both instances' account views.
	for _, env := range []*shareAdminEnv{a, b} {
		for id, load := range map[int64]int{1: 5, 2: 3, 3: 2} {
			for _, acc := range []*Account{env.repo.accountsByID[id], accountIn(env.repo.accounts, id)} {
				lf := load
				acc.Priority, acc.LoadFactor = 1, &lf
			}
		}
	}

	_, err := a.share.UpdateSettings(context.Background(), true)
	require.NoError(t, err)
	_, err = a.save(t, 0, shareUpdate)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		gp := b.share.groupPolicy(shareTestGroupID)
		return b.share.Settings().Enabled && gp != nil && gp.version == 1
	}, 2*time.Second, 10*time.Millisecond, "the other instance follows the broadcast")

	// A session placed on either instance stays on its account on both.
	ctx := cacheableContext()
	served := map[int64]int{}
	for i := range 300 {
		key := fmt.Sprintf("pair-%d", i)
		first, second := a, b
		if i%2 == 1 {
			first, second = b, a
		}
		placed := first.selectAccount(t, ctx, key, nil).Account.ID
		require.Equal(t, placed, second.selectAccount(t, ctx, key, nil).Account.ID, "session %s", key)
		served[placed]++

		// A new session lands on the same account whichever instance sees it.
		a.cache.mu.Lock()
		delete(a.cache.sessionBindings, key)
		a.cache.mu.Unlock()
		require.Equal(t, placed, second.selectAccount(t, ctx, key, nil).Account.ID, "fresh placement of %s", key)
	}
	require.InDelta(t, 150, served[1], 30)
	require.InDelta(t, 90, served[2], 30)
	require.InDelta(t, 60, served[3], 30)

	// A later save reaches the other instance too; switching back to the
	// original scheduler keeps every binding.
	a.cache.mu.Lock()
	bindings := len(a.cache.sessionBindings)
	a.cache.mu.Unlock()
	_, err = b.save(t, 1, sub2apiUpdate)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		gp := a.share.state.Load().groups[shareTestGroupID]
		return gp != nil && gp.version == 2
	}, 2*time.Second, 10*time.Millisecond)
	require.Nil(t, a.share.groupPolicy(shareTestGroupID), "the group is back on the original scheduler")
	a.cache.mu.Lock()
	defer a.cache.mu.Unlock()
	require.Len(t, a.cache.sessionBindings, bindings)
}
