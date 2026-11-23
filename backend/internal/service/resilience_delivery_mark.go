package service

import "github.com/gin-gonic/gin"

// DeliveryHeartbeatMark is what the delivery count deducts as heartbeats at
// one moment. Heartbeats written after it into output that is then discarded
// were never delivered, so deducting them would hide what is delivered next.
type DeliveryHeartbeatMark struct {
	writer *deliveryWriter
	bytes  int64
}

// MarkDeliveryHeartbeats folds the stopped heartbeats of the request into its
// count and returns the mark. Call it on the request goroutine, only once
// every heartbeat is stopped.
func MarkDeliveryHeartbeats(c *gin.Context) DeliveryHeartbeatMark {
	w := deliveryWriterFrom(c)
	if w == nil {
		return DeliveryHeartbeatMark{}
	}
	var stopped int64
	if k := w.sse.Load(); k != nil {
		k.mu.Lock()
		stopped += int64(k.bytes)
		k.mu.Unlock()
	}
	if k := w.images.Load(); k != nil {
		k.mu.Lock()
		stopped += int64(k.bytes)
		k.mu.Unlock()
	}
	w.moveHeartbeats(w.heartbeat.Load() + stopped)
	return DeliveryHeartbeatMark{writer: w, bytes: w.heartbeat.Load()}
}

// moveHeartbeats replaces all sources by one count, as one step for the probe.
// It takes no lock: the probe may already hold a heartbeat's.
func (w *deliveryWriter) moveHeartbeats(total int64) {
	w.heartbeatEpoch.Add(1)
	w.heartbeat.Store(total)
	w.sse.Store(nil)
	w.images.Store(nil)
	w.heartbeatEpoch.Add(1)
}

// Restore drops the heartbeats counted since the mark, which were discarded.
func (m DeliveryHeartbeatMark) Restore() {
	if m.writer == nil {
		return
	}
	m.writer.moveHeartbeats(m.bytes)
}
