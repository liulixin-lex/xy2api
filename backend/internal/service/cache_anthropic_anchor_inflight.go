package service

import (
	"sync"
	"sync/atomic"
)

const cacheAnchorInflightHardCap = 65536

// Only an armed, correctly scoped candidate reaches this bounded process-local
// counter. It contains HMAC identities, never prompt text or legacy seeds.
type cacheAnchorInflight struct {
	mu       sync.Mutex
	entries  map[string]int
	capacity int
	poisoned bool
}

type cacheAnchorLease struct {
	owner    *cacheAnchorInflight
	key      string
	released atomic.Bool
}

//nolint:unused // Used by the unit-tagged tests; production embeds the zero value in GatewayService and sets capacity once.
func newCacheAnchorInflight(capacity int) *cacheAnchorInflight {
	return &cacheAnchorInflight{capacity: min(capacity, cacheAnchorInflightHardCap)}
}

func (c *cacheAnchorInflight) acquire(key string, limit int) (*cacheAnchorLease, bool) {
	if c == nil || key == "" || limit < 1 || limit > 16 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned || c.capacity <= 0 {
		return nil, false
	}
	count, exists := c.entries[key]
	if exists && count <= 0 {
		c.poisoned = true
		return nil, false
	}
	if count >= limit || (!exists && len(c.entries) >= c.capacity) {
		return nil, false
	}
	if c.entries == nil {
		c.entries = make(map[string]int)
	}
	c.entries[key] = count + 1
	return &cacheAnchorLease{owner: c, key: key}, true
}

func (l *cacheAnchorLease) release() {
	if l == nil || l.owner == nil || !l.released.CompareAndSwap(false, true) {
		return
	}
	c := l.owner
	c.mu.Lock()
	defer c.mu.Unlock()
	count := c.entries[l.key]
	if count <= 0 {
		c.poisoned = true
		return
	}
	if count == 1 {
		delete(c.entries, l.key)
	} else {
		c.entries[l.key] = count - 1
	}
}
