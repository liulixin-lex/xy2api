package mcp

import (
	"container/list"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// keyLimiter bounds both request rate and cardinality. An unauthenticated
// source can exhaust only its own bucket, never a token holder's bucket.
type keyLimiter struct {
	mu        sync.Mutex
	entries   map[string]*list.Element
	order     list.List
	limit     rate.Limit
	burst     int
	lastSweep time.Time
}

type limitEntry struct {
	key    string
	bucket *rate.Limiter
	seen   time.Time
}

func newKeyLimiter(limit rate.Limit, burst int) *keyLimiter {
	return &keyLimiter{entries: make(map[string]*list.Element), limit: limit, burst: burst}
}

func (l *keyLimiter) allow(key string) bool {
	return l.allowAt(key, time.Now())
}

func (l *keyLimiter) allowAt(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= time.Minute {
		for tail := l.order.Back(); tail != nil; tail = l.order.Back() {
			entry, _ := tail.Value.(*limitEntry)
			if now.Sub(entry.seen) <= 5*time.Minute {
				break
			}
			delete(l.entries, entry.key)
			l.order.Remove(tail)
		}
		l.lastSweep = now
	}
	element := l.entries[key]
	if element == nil {
		if len(l.entries) >= 1024 {
			oldest := l.order.Back()
			oldEntry, _ := oldest.Value.(*limitEntry)
			delete(l.entries, oldEntry.key)
			l.order.Remove(oldest)
		}
		entry := &limitEntry{key: key, bucket: rate.NewLimiter(l.limit, l.burst)}
		element = l.order.PushFront(entry)
		l.entries[key] = element
	}
	entry, _ := element.Value.(*limitEntry)
	entry.seen = now
	l.order.MoveToFront(element)
	return entry.bucket.AllowN(now, 1)
}
