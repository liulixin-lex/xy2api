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
// count and returns the mark. Call it only once every heartbeat is stopped.
func MarkDeliveryHeartbeats(c *gin.Context) DeliveryHeartbeatMark {
	w := deliveryWriterFrom(c)
	if w == nil {
		return DeliveryHeartbeatMark{}
	}
	// Added before forgotten: the probe may run at any time and must never
	// take a heartbeat for output.
	if k := w.sse.Load(); k != nil {
		k.mu.Lock()
		w.heartbeat.Add(int64(k.bytes))
		k.mu.Unlock()
		w.sse.Store(nil)
	}
	if k := w.images.Load(); k != nil {
		k.mu.Lock()
		w.heartbeat.Add(int64(k.bytes))
		k.mu.Unlock()
		w.images.Store(nil)
	}
	return DeliveryHeartbeatMark{writer: w, bytes: w.heartbeat.Load()}
}

// Restore drops the heartbeats counted since the mark, which were discarded.
func (m DeliveryHeartbeatMark) Restore() {
	if m.writer == nil {
		return
	}
	m.writer.sse.Store(nil)
	m.writer.images.Store(nil)
	m.writer.heartbeat.Store(m.bytes)
}
