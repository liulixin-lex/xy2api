package service

import (
	"container/list"
	"sync"
	"time"
)

// Trace-only storage. No provider request can insert into, evict from, or
// publish a chain into the legacy OpenAI diagnostics/routing sessions map.
// The observer owns this store lazily; the zero value allocates no maps.
type cacheProviderTraceHistory struct {
	mu      sync.Mutex
	entries map[cacheProviderSessionKey]*cacheProviderHistoryEntry
	order   list.List
}

type cacheProviderHistoryEntry struct {
	key   cacheProviderSessionKey
	state *cacheTraceSessionState
	order *list.Element
}

func (key cacheProviderSessionKey) valid() bool {
	return key.digest != "" && (key.platform == PlatformAnthropic || key.platform == PlatformGemini)
}

// Called immediately before the actual send. The key is the same scoped HMAC
// used by the emitter. A retry retains its sequence but replaces its timestamp.
func (h *cacheProviderTraceHistory) send(key cacheProviderSessionKey, sequence uint64, at time.Time, caps CachePolicyCaps) cacheTraceTimingToken {
	token := cacheTraceTimingToken{sequence: sequence, sentAt: at, windowDelta: -1}
	if h == nil || !key.valid() || sequence == 0 {
		return token
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.entries == nil {
		h.entries = make(map[cacheProviderSessionKey]*cacheProviderHistoryEntry)
	}
	limit := max(1, min(caps.MaxSessions, 1024))
	for len(h.entries) > limit {
		h.evictOldest()
	}
	entry := h.entries[key]
	if entry == nil {
		if len(h.entries) >= limit {
			h.evictOldest()
		}
		entry = &cacheProviderHistoryEntry{key: key, state: new(cacheTraceSessionState)}
		entry.order = h.order.PushFront(entry)
		h.entries[key] = entry
	} else {
		h.order.MoveToFront(entry.order)
	}
	token.owner = entry.state
	entry.state.record(cacheTraceTimingEntry{sequence: sequence, sentAt: at, windowDelta: -1})
	return token
}

// Caller holds h.mu. Removing the map entry also invalidates every outstanding
// worker/timing owner; late publication must never recreate the old epoch.
func (h *cacheProviderTraceHistory) evictOldest() {
	if oldest := h.order.Back(); oldest != nil {
		entry := oldest.Value.(*cacheProviderHistoryEntry) //nolint:errcheck // send is the only writer of order and pushes *cacheProviderHistoryEntry.
		delete(h.entries, entry.key)
		h.order.Remove(oldest)
	}
}

func (h *cacheProviderTraceHistory) current(key cacheProviderSessionKey, owner *cacheTraceSessionState) bool {
	if h == nil || owner == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.entries[key]
	return entry != nil && entry.state == owner
}

// Worker-only snapshot. Never include chains settled after the current send;
// pointer identity prevents eviction followed by digest reuse from mixing epochs.
func (h *cacheProviderTraceHistory) snapshot(key cacheProviderSessionKey, owner *cacheTraceSessionState, cutoff time.Time) (chains [cachePrefixHistoryCount]cachePrefixChain) {
	if h == nil || owner == nil {
		return chains
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.entries[key]
	if entry == nil || entry.state != owner {
		return chains
	}
	n := 0
	for _, chain := range entry.state.chains {
		if chain.blocks > 0 && !chain.settledAt.After(cutoff) {
			chains[n] = chain
			n++
		}
	}
	return chains
}

// A failed or non-abuse undelivered request still closes its send timing.
// Publication remains a separate operation requiring a successful settlement.
func (h *cacheProviderTraceHistory) settle(key cacheProviderSessionKey, token cacheTraceTimingToken, at time.Time) (cacheTraceGap, *cacheTraceSessionState) {
	gap := cacheTraceGap{source: "no_prev"}
	if !key.valid() {
		gap.source = "no_session"
		return gap, nil
	}
	if h == nil || token.owner == nil || token.sequence == 0 || token.sentAt.IsZero() {
		return gap, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.entries[key]
	if entry == nil || entry.state != token.owner {
		return gap, nil
	}
	for _, previous := range entry.state.recent {
		if previous.sequence == 0 || previous.sequence == token.sequence ||
			!cacheTraceEarlier(previous.sentAt, previous.sequence, token.sentAt, token.sequence) {
			continue
		}
		gap.source = "prev_unsettled"
		if !previous.settledAt.IsZero() {
			gap.source = "settled"
			gap.milliseconds = token.sentAt.Sub(previous.settledAt).Milliseconds()
		}
		break
	}
	entry.state.record(cacheTraceTimingEntry{sequence: token.sequence, sentAt: token.sentAt, settledAt: at, windowDelta: -1})
	return gap, entry.state
}

func (h *cacheProviderTraceHistory) remember(key cacheProviderSessionKey, owner *cacheTraceSessionState, chain cachePrefixChain, prompt int, at time.Time, caps CachePolicyCaps) bool {
	if h == nil || owner == nil || !key.valid() || len(chain.hashes) == 0 {
		return false
	}
	chain = limitCachePrefixCheckpoints(chain, max(1, min(caps.MaxCheckpoints, cachePrefixMaxBlocks)))
	chain.prompt, chain.settledAt = prompt, at
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.entries[key]
	if entry == nil || entry.state != owner {
		return false
	}
	count := max(1, min(caps.ChainsPerSession, cachePrefixHistoryCount))
	for i := 0; i < count; i++ {
		if entry.state.chains[i].settledAt.After(at) {
			continue
		}
		copy(entry.state.chains[i+1:count], entry.state.chains[i:count-1])
		entry.state.chains[i] = chain
		return true
	}
	return false
}
