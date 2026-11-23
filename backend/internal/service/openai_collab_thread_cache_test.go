//go:build unit

package service

// Binding scenarios here exercise unfinished code through the test-only hook.
// They do not establish equivalence or enable thread bindings in this release.

import (
	"context"
	"errors"
	"strconv"
	"time"
)

func (c *collabEffectsCache) GetSessionBinding(ctx context.Context, group int64, key string) (StickySessionBinding, error) {
	id, err := c.GetSessionAccountID(ctx, group, key)
	b := StickySessionBinding{AccountID: id}
	if id > 0 {
		b.Generation = strconv.FormatInt(id, 10)
	}
	return b, err
}

func (c *collabEffectsCache) CommitSessionBinding(ctx context.Context, group int64, key string, expected StickySessionBinding, next int64, ttl time.Duration, _ BindingCommitOptions) (BindingCommitResult, error) {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.values, c.until, "commit", next, ttl)
		defer finish()
	}
	current, err := c.GetSessionBinding(ctx, group, key)
	if err != nil && !errors.Is(err, ErrStickySessionNotFound) {
		return BindingCommitResult{Status: BindingCommitError}, err
	}
	if current.AccountID != expected.AccountID || current.Generation != expected.Generation {
		return BindingCommitResult{Status: BindingChanged}, nil
	}
	err = c.SetSessionAccountID(ctx, group, key, next, ttl)
	return BindingCommitResult{Status: BindingCommitted, NewGen: strconv.FormatInt(next, 10)}, err
}

func (c *collabEffectsCache) RecordSessionBindingFailure(ctx context.Context, group int64, key, scope string, expected StickySessionBinding, target int64, ttl time.Duration, reset ...time.Time) (int64, error) {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.values, c.until, "read", 0, 0)
		defer finish()
	}
	current, err := c.GetSessionBinding(ctx, group, key)
	if err != nil || current.AccountID != expected.AccountID || current.Generation != expected.Generation {
		return 0, err
	}
	if cache, ok := c.GatewayCache.(*selectionRuntimeCache); ok {
		return time.Now().Add(ttl).UnixMilli(), cache.store.AddSessionAvoid(ctx, group, scope, target, ttl, reset...)
	}
	return time.Now().Add(ttl).UnixMilli(), nil
}

func (c *collabBindingExpiryCache) RefreshSessionTTL(ctx context.Context, group int64, key string, ttl time.Duration) error {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.values, c.until, "refresh", 0, ttl)
		defer finish()
	}
	if _, ok := c.values[key]; ok {
		c.until[key] = time.Now().Add(ttl)
		return nil
	}
	return c.GatewayCache.RefreshSessionTTL(ctx, group, key, ttl)
}

func (c *collabBindingExpiryCache) DeleteSessionAccountIDAndReport(ctx context.Context, group int64, key string) (bool, error) {
	if c.failWrite {
		return false, errors.New("synthetic persistence failure")
	}
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.values, c.until, "delete", 0, 0)
		defer finish()
	}
	_, ok := c.values[key]
	delete(c.values, key)
	delete(c.until, key)
	return ok, nil
}

func (b *openAICollabThreadBinding) write(ctx context.Context, account int64, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = b.service.openAIStickyBindingTTL(&b.affinity.groupID)
	}
	if account == 0 {
		account = -1
	}
	return b.service.cache.SetSessionAccountID(b.storageContext(ctx, 0), b.affinity.groupID, b.key, account, ttl)
}
