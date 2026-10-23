package service

import (
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// Only admitted trace requests allocate a sequence. It is a process-local
// ordering tie-breaker, never a logged request identifier.
var cacheTraceSendSequence atomic.Uint64

// markCacheTraceSent runs immediately before the actual upstream dispatch.
// captureCacheTraceRequest has already selected the final wire request. Body
// scanning still starts only after Do returns, preserving the existing budget
// and async-worker contract. Retries retain the logical sequence but replace
// its send time. Lock order is always trace.mu, then telemetry.mu.
func (s *OpenAIGatewayService) markCacheTraceSent(c *gin.Context) {
	t := cacheTraceFromGin(c)
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.data.AccountID == 0 || t.data.ProtocolOut == "" {
		return
	}
	// chain_percent and plugin produce complete events even when body work is
	// unavailable. Other budget rejections remain history-free minimal events.
	if t.skipUsage && t.data.Transport != "plugin" && t.data.NAReason != "chain_percent" &&
		(t.async == nil || !t.async.chainExcluded) {
		t.timing = cacheTraceTimingToken{sequence: t.timing.sequence, windowDelta: -1}
		return
	}
	sequence := t.timing.sequence
	if sequence == 0 {
		sequence = cacheTraceSendSequence.Add(1)
	}
	telemetry := s.cacheTelemetry()
	telemetry.mu.Lock()
	// Take the monotonic timestamp after acquiring the shared telemetry lock:
	// contention before dispatch must not appear as upstream time.
	t.timing = cacheTraceSendLocked(telemetry, t.sessionKey, sequence, time.Now(), t.window, t.timing)
	if t.pending != nil {
		// Pin the epoch selected at actual send, including a state that was
		// installed after request construction but before dispatch.
		t.pending.history = t.timing.owner
		t.pending.sentAt = t.timing.sentAt
	}
	telemetry.mu.Unlock()
	t.data.WindowSequenceDelta = t.timing.windowDelta
}
