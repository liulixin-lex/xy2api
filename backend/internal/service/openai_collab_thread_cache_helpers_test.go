package service

import (
	"context"
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
