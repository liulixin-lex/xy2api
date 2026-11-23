package resilience

const collabObservationTrackingLimit = 256

// Send/generation numbers increase during a request. Remember a fixed window
// for out-of-order completion, without allocating for the length of the stream.
type collabSequenceWindow struct {
	max   uint64
	slots [collabObservationTrackingLimit]uint64
}

// Old numbers outside the window cannot be classified as new or duplicate.
// Callers count those occurrences as untracked, never as verified new sends.
func (w *collabSequenceWindow) remember(sequence uint64) (fresh, tracked bool) {
	if sequence == 0 {
		return false, false
	}
	if sequence <= w.max && w.max-sequence >= collabObservationTrackingLimit {
		return false, false
	}
	if sequence > w.max {
		w.max = sequence
	}
	slot := &w.slots[sequence%collabObservationTrackingLimit]
	if *slot == sequence {
		return false, true
	}
	*slot = sequence
	return true, true
}
