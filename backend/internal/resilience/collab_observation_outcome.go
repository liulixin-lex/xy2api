package resilience

import "time"

// CollabDeliveryRecord carries the request time separately from the persisted
// document. Consumers must scope records to the same tenant before merging.
type CollabDeliveryRecord struct {
	At          time.Time
	Observation *CollabObservation
}

type CollabDeliverySummary struct {
	Delivery       CollabDigest
	Outcome        string
	Form           CollabForm
	HistoricalLoss bool
}

// ReconcileCollabDelivery implements cross-request precedence for one delivery.
// Accepted original content always wins. Only a registered parent's completion
// notice confirms notification; seeing the token elsewhere does not.
func ReconcileCollabDelivery(delivery CollabDigest, records []CollabDeliveryRecord, at time.Time) CollabDeliverySummary {
	result := CollabDeliverySummary{Delivery: delivery, Outcome: "interrupted"}
	if delivery == "" {
		return result
	}
	accepted, placeholder, local, user, confirmed := false, false, false, false, false
	var firstAccepted, firstNotice time.Time
	issued := make(map[CollabDigest]struct{})
	seen := make(map[CollabDigest]struct{})
	for _, record := range records {
		v := record.Observation
		if v == nil {
			continue
		}
		for _, d := range v.Deliveries {
			if d.Delivery != delivery {
				continue
			}
			switch d.Outcome {
			case CollabDeliveryOutcomeAcceptedOriginal:
				if !accepted || record.At.Before(firstAccepted) {
					result.Form = d.Form
					firstAccepted = record.At
				}
				accepted = true
			case CollabDeliveryOutcomePlaceholder, CollabDeliveryOutcomePlaceholderReplay:
				placeholder = true
			case CollabDeliveryOutcomeNoticePending:
				local = true
				if firstNotice.IsZero() || record.At.Before(firstNotice) {
					firstNotice = record.At
				}
			case CollabDeliveryOutcomeNoticeWrittenUser:
				local = true
				user = true
			}
		}
		for _, event := range v.Events {
			if event.Delivery != delivery || event.Event == "" {
				continue
			}
			switch event.Source {
			case CollabEventSourceIssued:
				issued[event.Event] = struct{}{}
			case CollabEventSourceSeenParentNotice:
				seen[event.Event] = struct{}{}
			}
		}
	}
	if accepted {
		result.Outcome = "delivered"
		for _, record := range records {
			if record.Observation == nil || record.At.Before(firstAccepted) {
				continue
			}
			for _, d := range record.Observation.Deliveries {
				if d.Delivery == delivery && (d.Outcome == CollabDeliveryOutcomePlaceholder || d.Outcome == CollabDeliveryOutcomePlaceholderReplay) {
					result.HistoricalLoss = true
				}
			}
		}
		return result
	}
	for event := range issued {
		if _, ok := seen[event]; ok {
			confirmed = true
			break
		}
	}
	switch {
	case local && confirmed:
		result.Outcome = "notice_returned_parent"
	case user:
		result.Outcome = "notice_written_user"
	case local && !firstNotice.IsZero() && at.Before(firstNotice.Add(24*time.Hour)):
		result.Outcome = "notice_pending"
	case local || placeholder:
		result.Outcome = "silent_loss"
	}
	return result
}
