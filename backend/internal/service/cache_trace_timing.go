package service

import "time"

// The legacy map keeps its single optional pointer slot; only its target
// changes. The sessions map value stays 56 bytes on linux/amd64. Helpers run
// only after the trace gate and only while holding openAICacheTelemetry.mu.
// They never read/write account, prefix, at, scores, or eviction policy.
// The chains and this state share the existing telemetry mutex; no new lock.
type cacheTraceSessionState struct {
	chains [cachePrefixHistoryCount]cachePrefixChain
	recent [4]cacheTraceTimingEntry // newest send first, not newest settlement
	window cacheTraceWindowHistory  // last parseable request, including beyond recent[4]
}

type cacheTraceTimingEntry struct {
	sequence    uint64 // request-local ID, stable across retries, never logged
	sentAt      time.Time
	settledAt   time.Time
	window      cacheTraceWindow
	windowDelta int64
}

type cacheTraceWindowHistory struct {
	sequence uint64
	sentAt   time.Time
	window   cacheTraceWindow
}

type cacheTraceTimingToken struct {
	sequence    uint64
	sentAt      time.Time
	owner       *cacheTraceSessionState // identity/epoch only; fields need telemetry.mu
	window      cacheTraceWindow
	windowDelta int64
}

type cacheTraceGap struct {
	source       string
	milliseconds int64 // emit only when source == "settled"
}

func cacheTraceEarlier(leftAt time.Time, leftSeq uint64, rightAt time.Time, rightSeq uint64) bool {
	return leftAt.Before(rightAt) || leftAt.Equal(rightAt) && leftSeq < rightSeq
}

// The caller records sentAt immediately before Do, not when Do returns.
// sequence must be nonzero and stable for a logical request across retries.
// A missing legacy session stays missing; only observeCacheUsage creates it.
func cacheTraceSendLocked(telemetry *openAICacheTelemetry, session string, sequence uint64, sentAt time.Time, window cacheTraceWindow, prior ...cacheTraceTimingToken) cacheTraceTimingToken {
	token := cacheTraceTimingToken{sequence: sequence, sentAt: sentAt, window: window, windowDelta: -1}
	if telemetry == nil || session == "" || sequence == 0 {
		return token
	}
	history, exists := telemetry.sessions[session]
	if !exists {
		return token
	}
	if history.traceChains == nil {
		history.traceChains = new(cacheTraceSessionState)
		telemetry.sessions[session] = history
	}
	state := history.traceChains
	token.owner = state
	previousWindow := state.previousWindow(sentAt, sequence)
	token.windowDelta = cacheTraceWindowDelta(window, previousWindow)
	for _, entry := range state.recent {
		if entry.sequence == sequence {
			// One logical request is one S4 observation, even if its transport
			// attempt is retried later. Replace its send timestamp below.
			token.windowDelta = entry.windowDelta
			break
		}
	}
	if len(prior) > 0 && prior[0].sequence == sequence && prior[0].owner == state {
		// The request-local token survives more than four intervening sends.
		// Retrying must not turn one window observation into another one.
		token.windowDelta = prior[0].windowDelta
	}
	state.record(cacheTraceTimingEntry{sequence: sequence, sentAt: sentAt, window: window, windowDelta: token.windowDelta})
	state.rememberWindow(sequence, sentAt, window)
	return token
}

// Called after observeCacheUsage has installed/preserved the legacy entry.
// Return owner to the async settlement handle. Its publish path must compare
// current.traceChains == owner under telemetry.mu before writing any chain.
// A stale request must never resurrect a deleted entry or publish into an
// unrelated replacement with the same session digest.
func cacheTraceSettleTimingLocked(telemetry *openAICacheTelemetry, session string, token cacheTraceTimingToken, settledAt time.Time) (cacheTraceGap, *cacheTraceSessionState) {
	gap := cacheTraceGap{source: "no_prev"}
	if session == "" {
		gap.source = "no_session"
		return gap, nil
	}
	if telemetry == nil || token.sequence == 0 || token.sentAt.IsZero() {
		return gap, nil
	}
	history, exists := telemetry.sessions[session]
	if !exists || token.owner != nil && history.traceChains != token.owner {
		return gap, nil
	}
	if history.traceChains == nil {
		history.traceChains = new(cacheTraceSessionState)
		telemetry.sessions[session] = history
	}
	state := history.traceChains
	if token.owner != nil {
		for _, previous := range state.recent {
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
	}
	// When the legacy entry did not exist at send, no earlier request was
	// observable. Keep no_prev even if another cold request settled first.
	// This conservative cold-concurrency coverage limit belongs in IMPL_NOTES.
	state.record(cacheTraceTimingEntry{sequence: token.sequence, sentAt: token.sentAt, settledAt: settledAt,
		window: token.window, windowDelta: token.windowDelta})
	state.rememberWindow(token.sequence, token.sentAt, token.window)
	return gap, state
}

func (state *cacheTraceSessionState) record(entry cacheTraceTimingEntry) {
	for i, old := range state.recent {
		if old.sequence == entry.sequence {
			copy(state.recent[i:], state.recent[i+1:])
			state.recent[len(state.recent)-1] = cacheTraceTimingEntry{}
			break
		}
	}
	for i, old := range state.recent {
		if old.sequence == 0 || cacheTraceEarlier(old.sentAt, old.sequence, entry.sentAt, entry.sequence) {
			copy(state.recent[i+1:], state.recent[i:len(state.recent)-1])
			state.recent[i] = entry
			return
		}
	}
}

func (state *cacheTraceSessionState) rememberWindow(sequence uint64, sentAt time.Time, window cacheTraceWindow) {
	if window.valid && (state.window.sequence == 0 || state.window.sequence == sequence ||
		cacheTraceEarlier(state.window.sentAt, state.window.sequence, sentAt, sequence)) {
		state.window = cacheTraceWindowHistory{sequence: sequence, sentAt: sentAt, window: window}
	}
}

func (state *cacheTraceSessionState) previousWindow(sentAt time.Time, sequence uint64) cacheTraceWindow {
	var best cacheTraceWindowHistory
	if state.window.window.valid && state.window.sequence != sequence &&
		cacheTraceEarlier(state.window.sentAt, state.window.sequence, sentAt, sequence) {
		best = state.window
	}
	for _, entry := range state.recent {
		if !entry.window.valid || entry.sequence == sequence ||
			!cacheTraceEarlier(entry.sentAt, entry.sequence, sentAt, sequence) {
			continue
		}
		if best.sequence == 0 || cacheTraceEarlier(best.sentAt, best.sequence, entry.sentAt, entry.sequence) {
			best = cacheTraceWindowHistory{sequence: entry.sequence, sentAt: entry.sentAt, window: entry.window}
		}
	}
	return best.window
}
