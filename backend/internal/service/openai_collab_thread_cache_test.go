//go:build unit

package service

// Binding scenarios here exercise unfinished code through the test-only hook.
// They do not establish equivalence or enable thread bindings in this release.

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"
)

var collabFakeStorage sync.Mutex

// Give the existing scalar test stores the same atomic effective-key view as
// the repository scripts. Clearing the metadata keeps nested fake calls inside
// this operation. Real concurrency and generation fencing are tested in Redis.
func collabFakeThreadOperation(ctx context.Context, b StickyThreadBinding, cache GatewayCache, group int64, values map[string]int64, expires map[string]time.Time, operation string, target int64, ttl time.Duration) (context.Context, func()) {
	collabFakeStorage.Lock()
	ctx = context.WithValue(ctx, stickyThreadBindingKey{}, nil)
	prior, present := values[b.Key]
	until := expires[b.Key]
	if expires != nil && !time.Now().Before(until) {
		present = false
		delete(values, b.Key)
	}
	marker := b.Key + ":deleted"
	detached := values[marker] != 0 && (expires == nil || time.Now().Before(expires[marker]))
	owner := prior
	if !present {
		owner = 0
		if !detached {
			owner, _ = cache.GetSessionAccountID(ctx, group, b.Parent)
		}
	}
	if owner > 0 {
		values[b.Key] = owner
		if expires != nil && !present {
			expires[b.Key] = time.Now().Add(b.TTL)
		}
	} else {
		delete(values, b.Key)
		delete(expires, b.Key)
	}
	return ctx, func() {
		defer collabFakeStorage.Unlock()
		changed := operation == "set" || operation == "commit" || operation == "refresh" && owner > 0 || operation == "claim" && (owner <= 0 || owner == target)
		restore := operation == "read" || !present && (operation == "refresh" || operation == "claim" && owner > 0) || prior == -1 && values[b.Key] == 0 && operation != "delete"
		if operation == "delete" {
			if target > 0 && target != owner {
				restore = true
			} else {
				values[b.Key] = -1
				ttl = b.TTL
				if expires != nil {
					expires[b.Key] = time.Now().Add(ttl)
				}
			}
		}
		if restore {
			if present {
				values[b.Key] = prior
				if expires != nil {
					expires[b.Key] = until
				}
			} else {
				delete(values, b.Key)
				delete(expires, b.Key)
			}
		}
		if changed && b.RefreshTTL > 0 {
			_ = cache.RefreshSessionTTL(ctx, group, b.Parent, b.RefreshTTL)
		}
		account, known := values[b.Key]
		if known && changed || operation == "delete" && !restore {
			values[marker] = 1
			if expires != nil {
				until := time.Now().Add(b.TTL)
				for _, key := range []string{b.Key, b.Parent, marker} {
					if expires[key].After(until) {
						until = expires[key]
					}
				}
				expires[marker] = until
			}
			detached = true
		}
		remaining := ttl
		if expires != nil {
			remaining = time.Until(expires[b.Key])
		}
		if remaining <= 0 && expires == nil {
			remaining = b.TTL
		}
		if !known && detached {
			account, known = -1, true
			if expires != nil {
				remaining = time.Until(expires[marker])
			}
		}
		parent, _ := cache.GetSessionAccountID(ctx, group, b.Parent)
		if b.Observe != nil {
			b.Observe(account, remaining, known, account == -1 || !known && parent <= 0)
		}
	}
}

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
