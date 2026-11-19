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
func (c *selectionRebindCache) CommitSessionBinding(_ context.Context, _ int64, key string, expected StickySessionBinding, target int64, _ time.Duration, _ BindingCommitOptions) (BindingCommitResult, error) {
	c.calls++
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || c.fail || c.bindings[key] != expected.AccountID {
		return BindingCommitResult{Status: BindingChanged}, c.err
	}
	c.bindings[key] = target
	return BindingCommitResult{Status: BindingCommitted, ObservedGen: expected.Generation, NewGen: expected.Generation + "-next"}, nil
}

func (c *selectionRebindCache) GetSessionBinding(ctx context.Context, group int64, key string) (StickySessionBinding, error) {
	id, err := c.GetSessionAccountID(ctx, group, key)
	return StickySessionBinding{AccountID: id, Generation: "g1"}, err
}

func (c *selectionRebindCache) RecordSessionBindingFailure(context.Context, int64, string, string, StickySessionBinding, int64, time.Duration, ...time.Time) (int64, error) {
	return 0, nil
}
