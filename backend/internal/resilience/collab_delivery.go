package resilience

import (
	"context"
	"sync"
)

// CollabDeliveryEvent identifies one physical response and one frame/write.
// Payload is an immutable copy of a relevant frame; it is transient, never logged.
// Written means the full write and required flush succeeded. Partial writes
// carry WriteError and must not confirm records, snapshots or leases.
// Completed is emitted once for success or failure, including aborted responses.
type CollabDeliveryEvent struct {
	Generation   CollabGeneration
	SendSeq      CollabSendSequence
	Phase        CollabDeliveryPhase
	Sequence     int64
	EventType    string
	Payload      string
	ItemID       string
	CallID       string
	ResponseID   string
	BytesWritten int
	WriteError   error
	Semantic     bool
	Executable   bool
	Successful   bool
}

type CollabDeliveryConsumer interface {
	Received(context.Context, CollabDeliveryEvent)
	AboutToWrite(context.Context, CollabDeliveryEvent)
	Written(context.Context, CollabDeliveryEvent)
	Completed(context.Context, CollabDeliveryEvent)
}

type NoopCollabDeliveryConsumer struct{}

func (NoopCollabDeliveryConsumer) Received(context.Context, CollabDeliveryEvent)     {}
func (NoopCollabDeliveryConsumer) AboutToWrite(context.Context, CollabDeliveryEvent) {}
func (NoopCollabDeliveryConsumer) Written(context.Context, CollabDeliveryEvent)      {}
func (NoopCollabDeliveryConsumer) Completed(context.Context, CollabDeliveryEvent)    {}

// CollabDeliveryHooks fans out synchronously in registration order. It owns no
// writer and cannot veto, rewrite, or fail delivery. Consumers enforce their
// own generation/sequence idempotency. Callbacks run outside the registry lock.
// Register before the first frame; each notification snapshots registrations.
type CollabDeliveryHooks struct {
	mu        sync.Mutex
	consumers []CollabDeliveryConsumer
}

func (h *CollabDeliveryHooks) Register(consumer CollabDeliveryConsumer) {
	if h == nil || consumer == nil {
		return
	}
	h.mu.Lock()
	// Copy on registration; notification snapshots are allocation-free.
	next := make([]CollabDeliveryConsumer, len(h.consumers)+1)
	copy(next, h.consumers)
	next[len(h.consumers)] = consumer
	h.consumers = next
	h.mu.Unlock()
}

// Payload is copied once, only for collaboration call completion and terminal
// frames. Deltas and unrelated/image output carry metadata only. The immutable
// string is shared by consumers and cannot corrupt another observer's view.
func (h *CollabDeliveryHooks) notify(ctx context.Context, event CollabDeliveryEvent, payload []byte, phase CollabDeliveryPhase) {
	if h == nil {
		return
	}
	h.mu.Lock()
	consumers := h.consumers
	h.mu.Unlock()
	if len(consumers) == 0 {
		return
	}
	event.Phase = phase
	event.Payload = ""
	if event.Executable && (event.EventType == "response.output_item.done" || event.EventType == "response.completed" || event.EventType == "response.done" || event.EventType == "response.json") {
		event.Payload = string(payload)
	}
	for _, consumer := range consumers {
		collabNotifyConsumer(ctx, consumer, event)
	}
}

func collabNotifyConsumer(ctx context.Context, consumer CollabDeliveryConsumer, event CollabDeliveryEvent) {
	defer collabRecover(&collabSafety.consumerPanics)
	switch event.Phase {
	case CollabDeliveryPhaseReceived:
		consumer.Received(ctx, event)
	case CollabDeliveryPhaseAboutToWrite:
		consumer.AboutToWrite(ctx, event)
	case CollabDeliveryPhaseWritten:
		consumer.Written(ctx, event)
	case CollabDeliveryPhaseCompleted:
		consumer.Completed(ctx, event)
	}
}

func (h *CollabDeliveryHooks) Received(ctx context.Context, event CollabDeliveryEvent, payload []byte) {
	h.notify(ctx, event, payload, CollabDeliveryPhaseReceived)
}
func (h *CollabDeliveryHooks) AboutToWrite(ctx context.Context, event CollabDeliveryEvent, payload []byte) {
	h.notify(ctx, event, payload, CollabDeliveryPhaseAboutToWrite)
}
func (h *CollabDeliveryHooks) Written(ctx context.Context, event CollabDeliveryEvent, payload []byte) {
	h.notify(ctx, event, payload, CollabDeliveryPhaseWritten)
}
func (h *CollabDeliveryHooks) Completed(ctx context.Context, event CollabDeliveryEvent, payload []byte) {
	h.notify(ctx, event, payload, CollabDeliveryPhaseCompleted)
}
