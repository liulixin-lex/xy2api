//go:build unit

package service

import (
	"context"
	"time"
)

func selectionFixtureAccount(id int64, priority int) Account {
	return Account{ID: id, Priority: priority, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{7}}
}

type selectionRebindCache struct {
	*atomicStickyTestCache
	fail  bool
	err   error
	calls int
}

func (c *selectionRebindCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (c *selectionRebindCache) CompareAndSwapSessionAccountID(_ context.Context, _ int64, key string, owner, target int64, _ time.Duration) (bool, error) {
	c.calls++
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || c.fail || c.bindings[key] != owner {
		return false, c.err
	}
	c.bindings[key] = target
	return true, nil
}
