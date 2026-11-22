package resilience

import (
	"sync"
	"time"
)

type collabRejectionKey struct {
	delivery  CollabDeliveryKey
	rejection CollabDigest
}
type collabRejectionEntry struct {
	events  [CollabTerminalRejectionThreshold]CollabDigest
	count   int
	expires time.Time
}

// Bounded local disposition component for a records implementation. It stores
// only the first two distinct terminal events and expires after the record TTL.
// Full capacity drops a new key (absence), never evicts useful state or blocks
// on shared storage. Include Counts in LookupBatch; enqueue through the adapter.
type CollabTerminalRejectionCounter struct {
	mu       sync.Mutex
	entries  map[collabRejectionKey]collabRejectionEntry
	capacity int
}

func NewCollabTerminalRejectionCounter(capacity int) *CollabTerminalRejectionCounter {
	return &CollabTerminalRejectionCounter{entries: make(map[collabRejectionKey]collabRejectionEntry), capacity: min(max(capacity, 1), CollabRecordMaxEntries)}
}
func (c *CollabTerminalRejectionCounter) Record(record CollabTerminalRejectionRecord, now time.Time) (int, bool) {
	if c == nil || record.Key.Delivery == "" || record.Key.Item == "" || record.Rejection == "" || record.Event == "" || !record.ExpiresAt.After(now) {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := collabRejectionKey{record.Key, record.Rejection}
	entry, exists := c.entries[key]
	if exists && !entry.expires.After(now) {
		delete(c.entries, key)
		exists = false
		entry = collabRejectionEntry{}
	}
	if !exists {
		if len(c.entries) >= c.capacity {
			// Cleanup is bounded by the same hard capacity as storage.
			for k, v := range c.entries {
				if !v.expires.After(now) {
					delete(c.entries, k)
				}
			}
		}
		if len(c.entries) >= c.capacity {
			return 0, false
		}
		entry.expires = collabEarlierTime(record.ExpiresAt, now.Add(CollabRecordTTL))
	}
	for i := 0; i < entry.count; i++ {
		if entry.events[i] == record.Event {
			return entry.count, true
		}
	}
	if entry.count < CollabTerminalRejectionThreshold {
		entry.events[entry.count] = record.Event
		entry.count++
	}
	c.entries[key] = entry
	return entry.count, true
}
func (c *CollabTerminalRejectionCounter) Counts(key CollabDeliveryKey, now time.Time) []CollabTerminalRejectionCount {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var counts []CollabTerminalRejectionCount
	for k, entry := range c.entries {
		if !entry.expires.After(now) {
			delete(c.entries, k)
			continue
		}
		if k.delivery == key {
			counts = append(counts, CollabTerminalRejectionCount{Key: key, Rejection: k.rejection, Count: entry.count, ExpiresAt: entry.expires})
		}
	}
	return counts
}
func collabEarlierTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
