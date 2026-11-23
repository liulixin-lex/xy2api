package resilience

import "sync/atomic"

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
	if recover() != nil {
		counter.Add(1)
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
// It counts fixed events only; panic values never leave the worker.
func RecoverCollabWorker() {
	if recover() != nil {
		collabSafety.consumerPanics.Add(1)
	}
}

func NoteCollabWorkerPanic() { collabSafety.consumerPanics.Add(1) }
