package resilience

import (
	"reflect"
	"slices"
)

// Clone known enum buckets by lookup rather than walking caller-controlled maps.
// The finite schema bounds allocation and prevents arbitrary map keys persisting.
func cloneCollabCounts(in *CollabObservationCounts) *CollabObservationCounts {
	if in == nil {
		return nil
	}
	v := *in
	v.CallCount = collabCopyCount(in.CallCount)
	v.Marker = cloneCollabMarkerCounts(in.Marker)
	dst, src := reflect.ValueOf(&v).Elem(), reflect.ValueOf(in).Elem()
	for i := 0; i < dst.NumField(); i++ {
		field := dst.Field(i)
		if field.Kind() != reflect.Map || field.IsNil() {
			continue
		}
		next := reflect.MakeMap(field.Type())
		for _, key := range collabObservationEnumValues[field.Type().Key()] {
			k := reflect.New(field.Type().Key()).Elem()
			k.SetString(key)
			if count := src.Field(i).MapIndex(k); count.IsValid() && count.Int() >= 0 {
				next.SetMapIndex(k, count)
			}
		}
		field.Set(next)
	}
	return &v
}

func cloneCollabMarkerCounts(in CollabMarkerCounts) CollabMarkerCounts {
	return CollabMarkerCounts{collabCopyCount(in.UpstreamEmpty), collabCopyCount(in.DeliveredEmpty), collabCopyCount(in.Add), collabCopyCount(in.Keep), collabCopyCount(in.Veto)}
}

func collabCount[K ~string](counts *map[K]int, key K) {
	known := collabObservationEnumValues[reflect.TypeFor[K]()]
	index := slices.Index(known, string(key))
	if index < 0 {
		return
	}
	if *counts == nil {
		*counts = make(map[K]int)
	}
	canonical := K(known[index])
	(*counts)[canonical] = collabAdd((*counts)[canonical], 1)
}

func collabAdd(a, b int) int {
	a, b = max(a, 0), max(b, 0)
	limit := int(^uint(0) >> 1)
	if b > limit-a {
		return limit
	}
	return a + b
}

func collabAddKnown(dst **int, src *int) {
	if src == nil {
		return
	}
	if *dst == nil {
		*dst = new(int)
	}
	**dst = collabAdd(**dst, *src)
}

func (c *CollabObservationCounts) send(s CollabSendObservation) {
	collabCount(&c.Actions, s.Action)
	collabCount(&c.Results, s.Result)
	collabCount(&c.RawPlans, s.RawPlan)
	collabCount(&c.RawReasons, s.RawReason)
	collabCount(&c.Plans, s.Plan)
	collabCount(&c.PlanReasons, s.PlanReason)
	collabCount(&c.Eligibility, s.Eligibility)
	collabCount(&c.RestoreErrors, s.RestoreError)
	// Reasons count one occurrence per reason per send, independent of detail cap.
	for _, known := range collabObservationEnumValues[reflect.TypeFor[CollabMarkerReason]()] {
		reason := CollabMarkerReason(known)
		if slices.Contains(s.MarkerReasons, reason) {
			collabCount(&c.MarkerReasons, reason)
		}
	}
	collabAddKnown(&c.CallCount, s.CallCount)
	collabAddKnown(&c.Marker.UpstreamEmpty, s.Marker.UpstreamEmpty)
	collabAddKnown(&c.Marker.DeliveredEmpty, s.Marker.DeliveredEmpty)
	collabAddKnown(&c.Marker.Add, s.Marker.Add)
	collabAddKnown(&c.Marker.Keep, s.Marker.Keep)
	collabAddKnown(&c.Marker.Veto, s.Marker.Veto)
}

func (c *CollabObservationCounts) delivery(d CollabDeliveryObservation) {
	collabCount(&c.DeliveryPositions, d.Position)
	collabCount(&c.DeliveryForms, d.Form)
	collabCount(&c.DeliveryKinds, d.Kind)
	collabCount(&c.DeliveryClasses, d.Class)
	collabCount(&c.DeliveryOutcomes, d.Outcome)
	collabCount(&c.DeliveryEvidence, d.Evidence)
	collabCount(&c.DeliveryStages, d.Stage)
	collabCount(&c.DeliveryScopes, d.Scope)
	limit := int64(^uint64(0) >> 1)
	c.DeliveryBytes += min(max(d.Bytes, 0), limit-max(c.DeliveryBytes, 0))
}

func (c *CollabObservationCounts) event(e CollabEventObservation) {
	collabCount(&c.EventSources, e.Source)
}
func (c *CollabObservationCounts) hint(h CollabHintObservation) {
	collabCount(&c.HintKinds, h.Kind)
	collabCount(&c.HintCooldowns, h.Cooldown)
	collabCount(&c.HintAttempts, h.Attempt)
	if h.Selectable {
		c.HintSelectable = collabAdd(c.HintSelectable, 1)
	}
	if h.IsCurrent {
		c.HintIsCurrent = collabAdd(c.HintIsCurrent, 1)
	}
}

// Uncapped typed documents can be encoded directly. Derive summaries before
// trimming, but never invent counts for old documents already missing detail.
func collabCountsBeforeTrim(v *CollabObservation) *CollabObservationCounts {
	if v.Counts != nil {
		return cloneCollabCounts(v.Counts)
	}
	if v.Truncated || v.SendOverflow > 0 || v.DeliveriesOverflow > 0 || v.EventsOverflow > 0 || v.HintsOverflow > 0 {
		return nil
	}
	counts := &CollabObservationCounts{}
	for _, s := range v.Sends {
		counts.send(s)
	}
	for _, d := range v.Deliveries {
		counts.delivery(d)
	}
	for _, e := range v.Events {
		counts.event(e)
	}
	for _, h := range v.Hints {
		counts.hint(h)
	}
	return counts
}
