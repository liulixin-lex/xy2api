package resilience

import (
	"log/slog"
	"runtime/debug"
	"sync/atomic"
)

var collabSafety struct {
	settingsCorrections atomic.Uint64
	consumerPanics      atomic.Uint64
	observerPanics      atomic.Uint64
	recordPanics        atomic.Uint64
	recordErrors        atomic.Uint64
	recordDropped       atomic.Uint64
}

// Fixed counters contain neither panic values nor upstream data.
type CollabSafetyCounts struct {
	SettingsCorrections uint64
	ConsumerPanics      uint64
	ObserverPanics      uint64
	RecordPanics        uint64
	RecordErrors        uint64
	RecordDropped       uint64
}

func CollabSafetySnapshot() CollabSafetyCounts {
	return CollabSafetyCounts{collabSafety.settingsCorrections.Load(), collabSafety.consumerPanics.Load(), collabSafety.observerPanics.Load(), collabSafety.recordPanics.Load(), collabSafety.recordErrors.Load(), collabSafety.recordDropped.Load()}
}

func collabRecover(counter *atomic.Uint64) {
	if recovered := recover(); recovered != nil {
		counter.Add(1)
		slog.Error("resilience.worker_panic", "panic", recovered, "stack", string(debug.Stack()))
	}
}

type SafeCollabObserver struct{ Observer CollabObserver }

func (s SafeCollabObserver) SealSend(send CollabSendObservation) (ok bool) {
	defer collabRecover(&collabSafety.observerPanics)
	if s.Observer != nil {
		return s.Observer.SealSend(send)
	}
	return false
}
func (s SafeCollabObserver) ObserveDelivery(delivery CollabDeliveryObservation) {
	defer collabRecover(&collabSafety.observerPanics)
	if s.Observer != nil {
		s.Observer.ObserveDelivery(delivery)
	}
}
func (s SafeCollabObserver) Freeze() (result *CollabObservation) {
	defer collabRecover(&collabSafety.observerPanics)
	if s.Observer != nil {
		return s.Observer.Freeze()
	}
	return nil
}

// RecoverCollabWorker must be deferred directly by every optional worker.
// Panics are operational errors, including when these workers run without
// collaboration enabled; preserve their diagnostic value and stack.
func RecoverCollabWorker() {
	if recovered := recover(); recovered != nil {
		NoteCollabWorkerPanic(recovered)
	}
}

func NoteCollabWorkerPanic(recovered any) {
	collabSafety.consumerPanics.Add(1)
	slog.Error("resilience.worker_panic", "panic", recovered, "stack", string(debug.Stack()))
}
