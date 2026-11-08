package resilience

import "time"

// MarkFirstBodyRead records when the gateway first calls the inbound body reader.
func (s *RequestState) MarkFirstBodyRead(at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bodyFirstRead.IsZero() {
		s.bodyFirstRead = at
	}
}

// MarkBodyReceived observes receipts on all requests, but Budget alone decides
// whether a text deadline may move. Replayed in-memory bodies never move it twice.
func (s *RequestState) MarkBodyReceived(at time.Time, basis string, bytes int64) {
	if s == nil || at.IsZero() {
		return
	}
	s.mu.Lock()
	if !s.bodyReceived.IsZero() {
		s.bodyReceiptRepeat++
		s.mu.Unlock()
		Count("body_receipt_repeat", s.GroupID(), 0, "")
		return
	}
	s.bodyReceived, s.bodyReceiptBasis, s.bodyBytes = at, basis, bytes
	if s.bodyFirstRead.IsZero() {
		s.bodyFirstRead = at
	}
	b := s.budget
	s.mu.Unlock()
	b.MarkBodyReceived(at, basis)
}

// BodyReceivedAt lets preread resetters retain the actual receipt time.
func (s *RequestState) BodyReceivedAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodyReceived
}
