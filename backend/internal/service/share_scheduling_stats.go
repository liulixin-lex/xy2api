package service

import (
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

const (
	shareStatsKindShare = "share"
	shareStatsKindCache = "cache"

	// Bounds keep memory flat when Redis is unreachable: excess statistics
	// are dropped, requests are never slowed down.
	maxShareStatsCounters       = 20000
	maxShareDecisionsPerFlush   = 100
	maxShareTraces              = 100000
	shareTraceLifetime          = 15 * time.Minute
	shareStatsDroppedField      = "dropped"
	shareStatsRequestsField     = "requests"
	shareStatsReasonPrefix      = "reason:"
	shareStatsServedPrefix      = "served:"
	shareStatsIdealPrefix       = "ideal:"
	shareStatsIdealSkipPrefix   = "ideal_skip:"
	shareStatsSkipPrefix        = "skip:" // + account + ":" + reason: tried but passed over
	shareStatsKeySourcePrefix   = "key:"
	shareStatsHotMigrationField = "hot_migration"
	shareStatsHotPrefix         = "hot:"
	shareStatsWaitCountField    = "wait_count"
	shareStatsWaitHitField      = "wait_hit"
	shareStatsWaitMsField       = "wait_ms"
	shareStatsAttemptsField     = "attempts"
	shareStatsShadowField       = "shadow"
	shareStatsShadowAgreeField  = "shadow_agree"
	shareStatsShadowPrefix      = "shadow_reason:"
	// shareStatsShadowSkippedField counts shadow comparisons skipped at the
	// concurrency bound; the agreement rate covers the rest.
	shareStatsShadowSkippedField = "shadow_skipped"
)

// shareStatsRecorder aggregates counters in memory until the next flush.
type shareStatsRecorder struct {
	mu        sync.Mutex
	counters  map[ShareSchedulingCounter]int64
	decisions map[int64][][]byte
	dropped   int64
}

func newShareStatsRecorder() *shareStatsRecorder {
	return &shareStatsRecorder{counters: map[ShareSchedulingCounter]int64{}, decisions: map[int64][][]byte{}}
}

func (r *shareStatsRecorder) add(kind string, groupID int64, at time.Time, field string, n int64) {
	if n == 0 {
		return
	}
	key := ShareSchedulingCounter{Kind: kind, GroupID: groupID, Minute: at.Unix() / 60, Field: field}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.counters[key]; !ok && len(r.counters) >= maxShareStatsCounters {
		r.dropped++
		return
	}
	r.counters[key] += n
}

// shareStatsDelta is one field increment of addAll.
type shareStatsDelta struct {
	field string
	n     int64
}

// addAll adds several fields of one group and minute under one lock: a
// decision updates about ten counters.
func (r *shareStatsRecorder) addAll(kind string, groupID int64, at time.Time, deltas []shareStatsDelta) {
	minute := at.Unix() / 60
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range deltas {
		if d.n == 0 {
			continue
		}
		key := ShareSchedulingCounter{Kind: kind, GroupID: groupID, Minute: minute, Field: d.field}
		if _, ok := r.counters[key]; !ok && len(r.counters) >= maxShareStatsCounters {
			r.dropped++
			continue
		}
		r.counters[key] += d.n
	}
}

// addDropped counts counters lost outside the recorder, such as a failed
// write; the next flush reports them with the in-memory drops.
func (r *shareStatsRecorder) addDropped(n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropped += n
}

// decisionRoom reports whether a decision sample of the group would be kept,
// so callers skip encoding samples the flush would drop.
func (r *shareStatsRecorder) decisionRoom(groupID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.decisions[groupID]) >= maxShareDecisionsPerFlush {
		r.dropped++
		return false
	}
	return true
}

func (r *shareStatsRecorder) addDecision(groupID int64, sample []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.decisions[groupID]) >= maxShareDecisionsPerFlush {
		r.dropped++
		return
	}
	r.decisions[groupID] = append(r.decisions[groupID], sample)
}

func (r *shareStatsRecorder) drain() ShareSchedulingStatsBatch {
	r.mu.Lock()
	defer r.mu.Unlock()
	batch := ShareSchedulingStatsBatch{Counters: r.counters, Decisions: r.decisions}
	if r.dropped > 0 {
		key := ShareSchedulingCounter{Kind: shareStatsKindShare, Minute: time.Now().Unix() / 60, Field: shareStatsDroppedField}
		batch.Counters[key] += r.dropped
		r.dropped = 0
	}
	r.counters = map[ShareSchedulingCounter]int64{}
	r.decisions = map[int64][][]byte{}
	return batch
}

// shareTrace links a selection to the usage it produces, so cache outcomes
// can be attributed to the scheduling decision that caused them.
type shareTrace struct {
	groupID    int64
	sessionKey string
	accountID  int64
	platform   string
	// reason is the share decision; empty for the original scheduler.
	reason sharesched.Reason
	// switched reports a request-level failover before this selection.
	switched bool
	at       time.Time
}

// shareTraceTable maps request ids to their latest selection. Entries are
// consumed by usage recording and swept after shareTraceLifetime.
type shareTraceTable struct {
	mu      sync.Mutex
	entries map[string]shareTrace
}

func newShareTraceTable() *shareTraceTable {
	return &shareTraceTable{entries: map[string]shareTrace{}}
}

func (t *shareTraceTable) put(requestID string, trace shareTrace) {
	if requestID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.entries[requestID]; !ok && len(t.entries) >= maxShareTraces {
		return
	}
	t.entries[requestID] = trace
}

func (t *shareTraceTable) take(requestID string) (shareTrace, bool) {
	if requestID == "" {
		return shareTrace{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	trace, ok := t.entries[requestID]
	if ok {
		delete(t.entries, requestID)
	}
	return trace, ok
}

func (t *shareTraceTable) sweep(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, trace := range t.entries {
		if now.Sub(trace.at) > shareTraceLifetime {
			delete(t.entries, id)
		}
	}
}
