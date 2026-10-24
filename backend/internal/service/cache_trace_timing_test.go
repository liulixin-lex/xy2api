package service

import (
	"testing"
	"time"
	"unsafe"
)

// DRAFT ONLY: compile after applying the state-pointer prerequisite in the
// companion draft. Helpers are exercised under the same mutex as production.
func TestCacheTraceTimingGapCases(t *testing.T) {
	for _, tc := range []struct {
		name, want                                          string
		previousSettledBeforeSend, previousSettledAfterSend bool
		noPrevious, noSession                               bool
		wantMS                                              int64
	}{
		{name: "settled", want: "settled", previousSettledBeforeSend: true, wantMS: 1000},
		{name: "negative", want: "settled", previousSettledAfterSend: true, wantMS: -1000},
		{name: "in_flight", want: "prev_unsettled"},
		{name: "first_known", want: "no_prev", noPrevious: true},
		{name: "no_session", want: "no_session", noSession: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			telemetry := &openAICacheTelemetry{sessions: map[string]openAICacheHistory{"s": {account: 7, prefix: "fixed"}}}
			t0 := time.Now()
			telemetry.mu.Lock()
			defer telemetry.mu.Unlock()
			session := "s"
			if tc.noSession {
				session = ""
			}
			var first cacheTraceTimingToken
			if !tc.noPrevious && !tc.noSession {
				first = cacheTraceSendLocked(telemetry, session, 1, t0, cacheTraceWindow{})
				if tc.previousSettledBeforeSend {
					cacheTraceSettleTimingLocked(telemetry, session, first, t0.Add(time.Second))
				}
			}
			second := cacheTraceSendLocked(telemetry, session, 2, t0.Add(2*time.Second), cacheTraceWindow{})
			if tc.previousSettledAfterSend {
				cacheTraceSettleTimingLocked(telemetry, session, first, t0.Add(3*time.Second))
			}
			got, _ := cacheTraceSettleTimingLocked(telemetry, session, second, t0.Add(4*time.Second))
			if got.source != tc.want || got.source == "settled" && got.milliseconds != tc.wantMS {
				t.Fatalf("got %#v, want %s/%d", got, tc.want, tc.wantMS)
			}
			if len(telemetry.sessions) != 1 || telemetry.sessions["s"].account != 7 || telemetry.sessions["s"].prefix != "fixed" {
				t.Fatal("observation changed legacy session state")
			}
		})
	}
}

func TestCacheTraceTimingColdOverlapDoesNotCreateLegacySession(t *testing.T) {
	telemetry := &openAICacheTelemetry{sessions: make(map[string]openAICacheHistory)}
	t0 := time.Now()
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	first := cacheTraceSendLocked(telemetry, "s", 1, t0, cacheTraceWindow{})
	second := cacheTraceSendLocked(telemetry, "s", 2, t0.Add(time.Second), cacheTraceWindow{})
	if len(telemetry.sessions) != 0 || first.owner != nil || second.owner != nil {
		t.Fatal("send inserted a legacy session")
	}
	// Stand-in for the existing observeCacheUsage insertion.
	telemetry.sessions["s"] = openAICacheHistory{account: 7, prefix: "fixed", at: t0.Add(2 * time.Second)}
	for _, token := range []cacheTraceTimingToken{first, second} {
		gap, owner := cacheTraceSettleTimingLocked(telemetry, "s", token, t0.Add(3*time.Second))
		if gap.source != "no_prev" || owner == nil {
			t.Fatalf("cold overlap must remain conservative, got %#v/%p", gap, owner)
		}
	}
}

func TestCacheTraceTimingEvictionDoesNotResurrectOrUseReplacement(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "deleted", true: "same_digest_new_epoch"}[replacement], func(t *testing.T) {
			telemetry := &openAICacheTelemetry{sessions: map[string]openAICacheHistory{"s": {}}}
			t0 := time.Now()
			telemetry.mu.Lock()
			defer telemetry.mu.Unlock()
			token := cacheTraceSendLocked(telemetry, "s", 1, t0, cacheTraceWindow{})
			delete(telemetry.sessions, "s")
			var next *cacheTraceSessionState
			if replacement {
				next = new(cacheTraceSessionState)
				telemetry.sessions["s"] = openAICacheHistory{traceChains: next}
			}
			gap, owner := cacheTraceSettleTimingLocked(telemetry, "s", token, t0.Add(time.Second))
			if gap.source != "no_prev" || owner != nil {
				t.Fatalf("stale epoch used: %#v/%p", gap, owner)
			}
			if !replacement && len(telemetry.sessions) != 0 {
				t.Fatal("deleted session resurrected")
			}
			if replacement && (telemetry.sessions["s"].traceChains != next || next.recent[0].sequence != 0) {
				t.Fatal("replacement session modified")
			}
		})
	}
}

func TestCacheTraceTimingUsesSendOrderAcrossOutOfOrderSettlements(t *testing.T) {
	telemetry := &openAICacheTelemetry{sessions: map[string]openAICacheHistory{"s": {}}}
	t0 := time.Now()
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	first := cacheTraceSendLocked(telemetry, "s", 1, t0, cacheTraceWindow{})
	second := cacheTraceSendLocked(telemetry, "s", 2, t0.Add(time.Second), cacheTraceWindow{})
	cacheTraceSettleTimingLocked(telemetry, "s", second, t0.Add(2*time.Second))
	cacheTraceSettleTimingLocked(telemetry, "s", first, t0.Add(3*time.Second))
	third := cacheTraceSendLocked(telemetry, "s", 3, t0.Add(4*time.Second), cacheTraceWindow{})
	gap, _ := cacheTraceSettleTimingLocked(telemetry, "s", third, t0.Add(5*time.Second))
	if gap.source != "settled" || gap.milliseconds != 2000 {
		t.Fatalf("picked most recently settled instead of sent: %#v", gap)
	}
}

func TestCacheTraceTimingWindowCases(t *testing.T) {
	for _, tc := range []struct {
		name              string
		previous, current cacheTraceWindow
		want              int64
	}{
		{"same_thread", cacheTraceWindow{thread: 7, sequence: 1, valid: true}, cacheTraceWindow{thread: 7, sequence: 4, valid: true}, 3},
		{"missing", cacheTraceWindow{thread: 7, sequence: 1, valid: true}, cacheTraceWindow{}, -1},
		{"no_comparable", cacheTraceWindow{}, cacheTraceWindow{thread: 7, sequence: 4, valid: true}, -1},
		{"thread_changed", cacheTraceWindow{thread: 7, sequence: 1, valid: true}, cacheTraceWindow{thread: 8, sequence: 4, valid: true}, -2},
		{"sequence_decreased", cacheTraceWindow{thread: 7, sequence: 4, valid: true}, cacheTraceWindow{thread: 7, sequence: 1, valid: true}, -3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			telemetry := &openAICacheTelemetry{sessions: map[string]openAICacheHistory{"s": {}}}
			t0 := time.Now()
			telemetry.mu.Lock()
			defer telemetry.mu.Unlock()
			cacheTraceSendLocked(telemetry, "s", 1, t0, tc.previous)
			// Missing/unparseable intervening requests must not reset S4 state.
			for i := uint64(2); i < 8; i++ {
				cacheTraceSendLocked(telemetry, "s", i, t0.Add(time.Duration(i)*time.Second), cacheTraceWindow{})
			}
			last := cacheTraceSendLocked(telemetry, "s", 8, t0.Add(8*time.Second), tc.current)
			if last.windowDelta != tc.want {
				t.Fatalf("got delta %d, want %d", last.windowDelta, tc.want)
			}
		})
	}
}

func TestCacheTraceTimingMapValueKeepsOriginalPointerLayout(t *testing.T) {
	var previousLayout struct {
		traceChains *[cachePrefixHistoryCount]cachePrefixChain
		account     int64
		prefix      string
		at          time.Time
	}
	if got, want := unsafe.Sizeof(openAICacheHistory{}), unsafe.Sizeof(previousLayout); got != want || got > 128 {
		t.Fatalf("session map layout changed: got %d, prior %d", got, want)
	}
}

func TestCacheTraceTimingRetryKeepsLogicalSequenceAndLastSend(t *testing.T) {
	telemetry := &openAICacheTelemetry{sessions: map[string]openAICacheHistory{"s": {}}}
	t0 := time.Now()
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	cacheTraceSendLocked(telemetry, "s", 1, t0, cacheTraceWindow{thread: 7, sequence: 2, valid: true})
	first := cacheTraceSendLocked(telemetry, "s", 2, t0.Add(time.Second), cacheTraceWindow{thread: 7, sequence: 5, valid: true})
	if first.windowDelta != 3 {
		t.Fatalf("first window delta = %d, want 3", first.windowDelta)
	}
	var last cacheTraceTimingToken
	for sequence := uint64(3); sequence <= 8; sequence++ {
		last = cacheTraceSendLocked(telemetry, "s", sequence, t0.Add(time.Duration(sequence)*time.Second), cacheTraceWindow{thread: 7, sequence: int64(sequence + 10), valid: true})
	}
	cacheTraceSettleTimingLocked(telemetry, "s", last, t0.Add(9*time.Second))
	retry := cacheTraceSendLocked(telemetry, "s", first.sequence, t0.Add(10*time.Second), first.window, first)
	if retry.sequence != first.sequence || !retry.sentAt.Equal(t0.Add(10*time.Second)) || retry.windowDelta != 3 {
		t.Fatalf("retry changed logical identity/window or lost final send: %#v", retry)
	}
	gap, state := cacheTraceSettleTimingLocked(telemetry, "s", retry, t0.Add(11*time.Second))
	if gap.source != "settled" || gap.milliseconds != 1000 {
		t.Fatalf("retry gap did not use final dispatch: %#v", gap)
	}
	for i, want := range []uint64{2, 8, 7, 6} {
		if state.recent[i].sequence != want {
			t.Fatalf("recent[%d] = %d, want %d", i, state.recent[i].sequence, want)
		}
	}
}

func TestCacheTraceTimingLateOldSettlementDoesNotDisplaceRecentSends(t *testing.T) {
	telemetry := &openAICacheTelemetry{sessions: map[string]openAICacheHistory{"s": {}}}
	t0 := time.Now()
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	var tokens [6]cacheTraceTimingToken
	for i := range tokens {
		tokens[i] = cacheTraceSendLocked(telemetry, "s", uint64(i+1), t0.Add(time.Duration(i)*time.Second), cacheTraceWindow{})
	}
	for _, i := range []int{4, 0, 5, 1, 3, 2} {
		cacheTraceSettleTimingLocked(telemetry, "s", tokens[i], t0.Add(time.Duration(10+i)*time.Second))
	}
	state := telemetry.sessions["s"].traceChains
	for i, want := range []uint64{6, 5, 4, 3} {
		if got := state.recent[i].sequence; got != want {
			t.Fatalf("settlement reordered recent sends: recent[%d]=%d, want %d", i, got, want)
		}
	}
}

func TestCacheTraceTimingColdConcurrentSendAndSettlement(t *testing.T) {
	telemetry := &openAICacheTelemetry{sessions: make(map[string]openAICacheHistory)}
	t0 := time.Now()
	tokens := make(chan cacheTraceTimingToken, 2)
	for sequence := uint64(1); sequence <= 2; sequence++ {
		go func(sequence uint64) {
			telemetry.mu.Lock()
			token := cacheTraceSendLocked(telemetry, "s", sequence, t0.Add(time.Duration(sequence)*time.Second), cacheTraceWindow{})
			telemetry.mu.Unlock()
			tokens <- token
		}(sequence)
	}
	first, second := <-tokens, <-tokens
	telemetry.mu.Lock()
	if len(telemetry.sessions) != 0 {
		telemetry.mu.Unlock()
		t.Fatal("cold concurrent sends created a legacy session")
	}
	telemetry.sessions["s"] = openAICacheHistory{account: 7, prefix: "legacy", at: t0}
	telemetry.mu.Unlock()
	gaps := make(chan cacheTraceGap, 2)
	for _, token := range []cacheTraceTimingToken{first, second} {
		go func(token cacheTraceTimingToken) {
			telemetry.mu.Lock()
			gap, _ := cacheTraceSettleTimingLocked(telemetry, "s", token, t0.Add(4*time.Second))
			telemetry.mu.Unlock()
			gaps <- gap
		}(token)
	}
	for range 2 {
		if gap := <-gaps; gap.source != "no_prev" {
			t.Fatalf("cold overlap fabricated a predecessor: %#v", gap)
		}
	}
}

func TestCacheTraceTimingDoesNotChangeLegacyStateOrScores(t *testing.T) {
	t0 := time.Now()
	legacy := openAICacheHistory{account: 17, prefix: "legacy-prefix", at: t0.Add(-time.Hour)}
	score := openAICacheSample{count: 3, sampleCursor: 2, input: 2048, read: 1024, write: 512, ttft: 123, at: t0}
	telemetry := &openAICacheTelemetry{
		sessions: map[string]openAICacheHistory{"s": legacy, "other": {account: 18, prefix: "other", at: t0}},
		scores:   map[string]openAICacheSample{"score": score},
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	token := cacheTraceSendLocked(telemetry, "s", 1, t0, cacheTraceWindow{})
	cacheTraceSettleTimingLocked(telemetry, "s", token, t0.Add(time.Second))
	got := telemetry.sessions["s"]
	got.traceChains = nil
	if got != legacy || len(telemetry.sessions) != 2 || telemetry.sessions["other"].account != 18 ||
		len(telemetry.scores) != 1 || telemetry.scores["score"] != score {
		t.Fatal("timing observation changed legacy routing/eviction inputs")
	}
}

func TestCacheTraceTimingMapOffsetsAreUnchanged(t *testing.T) {
	var current openAICacheHistory
	var previous struct {
		traceChains *[cachePrefixHistoryCount]cachePrefixChain
		account     int64
		prefix      string
		at          time.Time
	}
	if unsafe.Offsetof(current.traceChains) != unsafe.Offsetof(previous.traceChains) ||
		unsafe.Offsetof(current.account) != unsafe.Offsetof(previous.account) ||
		unsafe.Offsetof(current.prefix) != unsafe.Offsetof(previous.prefix) ||
		unsafe.Offsetof(current.at) != unsafe.Offsetof(previous.at) ||
		unsafe.Alignof(current) != unsafe.Alignof(previous) {
		t.Fatal("default map value field layout changed")
	}
}
