package resilience

import (
	"encoding/json"
	"reflect"
	"sync"
)

// CollabObservationAccumulator owns only observation data. Its callers provide
// typed facts after repair/restoration/delivery; it has no forwarding authority.
// One instance spans every send of an inbound request, including failover.
type CollabObservationAccumulator struct {
	mu              sync.Mutex
	value           CollabObservation
	sends           collabSequenceWindow
	deliveries      map[CollabDeliveryObservation]struct{}
	events          map[CollabEventObservation]struct{}
	transformations collabSequenceWindow
	frozen          bool
	raw             json.RawMessage
}

var _ CollabObserver = (*CollabObservationAccumulator)(nil)

// persist is true for declarations, encrypted agent/tool input, or controller
// adoption (including reasoning-only and zero-send requests). Observation is
// independent of the optional forwarding and shared-records switches.
func NewCollabObservationAccumulator(seed CollabObservation, persist bool) *CollabObservationAccumulator {
	if !persist {
		return nil
	}
	v := CloneCollabObservation(&seed)
	if v == nil {
		return nil
	}
	if v.Counts == nil {
		v.Counts = &CollabObservationCounts{}
	}
	return &CollabObservationAccumulator{value: *v, deliveries: make(map[CollabDeliveryObservation]struct{}), events: make(map[CollabEventObservation]struct{})}
}

func (a *CollabObservationAccumulator) SealSend(send CollabSendObservation) bool {
	defer recoverCollabObservation()
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.frozen {
		return false
	}
	fresh, tracked := a.sends.remember(uint64(send.Seq))
	if !tracked {
		a.value.Untracked.Sends = collabAdd(a.value.Untracked.Sends, 1)
		a.value.Truncated = true
	}
	if !fresh {
		return false
	}
	// An unsent reservation is not a physical send or part of its denominator.
	if send.Result == CollabSendResultNotSent {
		return true
	}
	a.value.SendTotal++
	a.value.Counts.send(send)
	if len(a.value.Sends) >= CollabObservationMaxSends {
		a.value.SendOverflow++
		a.value.Truncated = true
		return true
	}
	v := CloneCollabObservation(&CollabObservation{Sends: []CollabSendObservation{send}})
	if v == nil {
		return false
	}
	a.value.Truncated = a.value.Truncated || v.Truncated
	a.value.Sends = append(a.value.Sends, v.Sends[0])
	return true
}

func (a *CollabObservationAccumulator) ObserveDelivery(delivery CollabDeliveryObservation) {
	defer recoverCollabObservation()
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.frozen {
		return
	}
	// Deduplicate independently of retained detail, including overflow entries.
	collabSanitizeValue(reflect.ValueOf(&delivery).Elem(), &a.value.Truncated)
	if _, ok := a.deliveries[delivery]; ok {
		return
	}
	if len(a.deliveries) >= collabObservationTrackingLimit {
		a.value.Untracked.Deliveries = collabAdd(a.value.Untracked.Deliveries, 1)
		a.value.Truncated = true
		return
	}
	a.deliveries[delivery] = struct{}{}
	a.value.DeliveriesTotal++
	a.value.Counts.delivery(delivery)
	if len(a.value.Deliveries) >= CollabObservationMaxDeliveries {
		a.value.DeliveriesOverflow++
		a.value.Truncated = true
		return
	}
	a.value.Deliveries = append(a.value.Deliveries, delivery)
}

func (a *CollabObservationAccumulator) ObserveEvent(event CollabEventObservation) {
	defer recoverCollabObservation()
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.frozen {
		return
	}
	collabSanitizeValue(reflect.ValueOf(&event).Elem(), &a.value.Truncated)
	if _, ok := a.events[event]; ok {
		return
	}
	if len(a.events) >= collabObservationTrackingLimit {
		a.value.Untracked.Events = collabAdd(a.value.Untracked.Events, 1)
		a.value.Truncated = true
		return
	}
	a.events[event] = struct{}{}
	a.value.EventsTotal++
	a.value.Counts.event(event)
	if len(a.value.Events) >= CollabObservationMaxEvents {
		a.value.EventsOverflow++
		a.value.Truncated = true
		return
	}
	a.value.Events = append(a.value.Events, event)
}

func (a *CollabObservationAccumulator) ObserveHint(hint CollabHintObservation) {
	defer recoverCollabObservation()
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.frozen {
		return
	}
	a.value.HintsTotal++
	a.value.Counts.hint(hint)
	if len(a.value.Hints) >= CollabObservationMaxHints {
		a.value.HintsOverflow++
		a.value.Truncated = true
		return
	}
	a.value.Hints = append(a.value.Hints, hint)
}

// ObserveInputTransformations takes already classified counts, once per physical
// response. Nil means the report was not provided; an empty map means zero.
func (a *CollabObservationAccumulator) ObserveInputTransformations(generation CollabGeneration, counts *CollabInputTransformations) {
	defer recoverCollabObservation()
	if a == nil || counts == nil && generation != 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.frozen {
		return
	}
	fresh, tracked := a.transformations.remember(uint64(generation))
	if !tracked {
		a.value.Untracked.Transformations = collabAdd(a.value.Untracked.Transformations, 1)
		a.value.Truncated = true
	}
	if !fresh {
		return
	}
	v := CloneCollabObservation(&CollabObservation{InputTransformations: counts})
	if v == nil {
		return
	}
	a.value.Truncated = a.value.Truncated || v.Truncated
	if a.value.InputTransformations == nil {
		a.value.InputTransformations = &CollabInputTransformations{Counts: make(map[CollabTransformationReason]int, 4)}
	}
	for k, n := range v.InputTransformations.Counts {
		a.value.InputTransformations.Counts[k] = collabAdd(a.value.InputTransformations.Counts[k], n)
	}
}

// Finish records request totals once the owner has settled all sends/deliveries.
func (a *CollabObservationAccumulator) Finish(initial, stop CollabReason, mode CollabFinalMode, replacements CollabReplacementCounts, local int, execution CollabExecution) {
	defer recoverCollabObservation()
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.frozen {
		return
	}
	a.value.InitialReason, a.value.StopReason, a.value.FinalMode = initial, stop, mode
	a.value.Mixed = mode == CollabFinalModeMixed
	a.value.Replacements, a.value.LocalCount, a.value.Execution = replacements, local, execution
}

// Freeze never waits for a contended observer and never invokes user callbacks.
// Every returned snapshot owns its slices, pointers and maps.
func (a *CollabObservationAccumulator) Freeze() *CollabObservation {
	defer recoverCollabObservation()
	raw := a.freezeJSON()
	if len(raw) == 0 {
		return nil
	}
	var v CollabObservation
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return &v
}

func (a *CollabObservationAccumulator) freezeJSON() json.RawMessage {
	defer recoverCollabObservation()
	if a == nil || !a.mu.TryLock() {
		return nil
	}
	defer a.mu.Unlock()
	if !a.frozen {
		a.frozen = true
		a.raw = EncodeCollabObservation(&a.value)
	}
	return append(json.RawMessage(nil), a.raw...)
}

// Called under the request-state lock, before billing ownership changes. Only
// our bounded implementation is allowed here: an extension's Freeze method can
// panic or block indefinitely. Unknown observers are optional absence.
func freezeCollabObservationForBilling(state *CollabRequestState) json.RawMessage {
	if state == nil {
		return nil
	}
	source := state.Observer
	if safe, ok := source.(SafeCollabObserver); ok {
		source = safe.Observer
	}
	observer, ok := source.(*CollabObservationAccumulator)
	if !ok {
		return nil
	}
	return observer.freezeJSON()
}

// Observation is optional even when its own implementation has a defect.
func recoverCollabObservation() { _ = recover() }
