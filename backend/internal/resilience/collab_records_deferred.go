package resilience

import (
	"context"
	"time"
)

// Dedicated invalidation persistence must not queue behind observation writes.
// The shared cache implements this optional capability for shutdown flushing.
type CollabSharedInvalidationWriter interface {
	WriteInvalidations(context.Context, []CollabRecordMutation) error
}

func (s *SafeCollabRecordsStore) signalDeferred() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *SafeCollabRecordsStore) deferMutation(m CollabRecordMutation) {
	if s.shared == nil {
		return
	}
	m, _, ok := NormalizeCollabRecordMutation(m, time.Now())
	if !ok {
		return
	}
	// Same storage identity with a different payload is rejected locally before
	// enqueue. Hash the normalized payload as well, so only exact plans coalesce.
	scope, field, _ := CollabRecordStorageIdentity(m)
	key := collabAdmissionPublicationKey([]CollabRecordMutation{m})
	key.scope = scope
	key.field = field + key.field
	s.deferredMu.Lock()
	tenant := 0
	for k := range s.deferred {
		if k.scope.Tenant == scope.Tenant {
			tenant++
		}
	}
	if _, exists := s.deferred[key]; exists {
		s.deferredMu.Unlock()
		return
	}
	if len(s.deferred) >= 256 || tenant >= 64 {
		s.deferredMu.Unlock()
		collabSafety.recordDropped.Add(1)
		return
	}
	s.deferred[key] = m
	s.deferredMu.Unlock()
	s.signalDeferred()
}
func (s *SafeCollabRecordsStore) deferInvalidation(m CollabRecordMutation) {
	if s.shared == nil {
		return
	}
	m, _, ok := NormalizeCollabRecordMutation(m, time.Now())
	if !ok {
		return
	}
	s.deferredMu.Lock()
	scope := m.Token.Scope
	rows, existing := s.invalidations[scope]
	for i, row := range rows {
		if row.Token.Token == "" || row.Token == m.Token {
			if m.Issued.ExpiresAt.After(row.Issued.ExpiresAt) {
				rows[i].Issued.ExpiresAt = m.Issued.ExpiresAt
			}
			s.deferredMu.Unlock()
			return
		}
	}
	if len(rows) >= 16 {
		// Overflow preserves the latest horizon of every replaced invalidation.
		for _, row := range rows {
			if row.Issued.ExpiresAt.After(m.Issued.ExpiresAt) {
				m.Issued.ExpiresAt = row.Issued.ExpiresAt
			}
		}
		m.Token.Token = ""
		m.Token.Call = ""
		m.Token.Generation = 0
		rows = nil
	}
	if !existing && len(s.invalidations) >= CollabRecordMaxTrees {
		s.deferredMu.Unlock()
		collabSafety.recordDropped.Add(1)
		return
	}
	s.invalidations[scope] = append(rows, m)
	s.deferredMu.Unlock()
	s.signalDeferred()
}
func (s *SafeCollabRecordsStore) invalidationBatch() []CollabRecordMutation {
	s.deferredMu.Lock()
	defer s.deferredMu.Unlock()
	var rows []CollabRecordMutation
	for scope, pending := range s.invalidations {
		kept := pending[:0]
		for _, m := range pending {
			if collabRecordExpiry(m).After(time.Now()) {
				kept = append(kept, m)
			}
		}
		if len(kept) == 0 {
			delete(s.invalidations, scope)
			continue
		}
		s.invalidations[scope] = kept
		for _, m := range kept {
			rows = append(rows, m)
			if len(rows) == 64 {
				return rows
			}
		}
	}
	return rows
}
func (s *SafeCollabRecordsStore) flushInvalidations(ctx context.Context) {
	defer collabRecover(&collabSafety.recordPanics)
	for ctx.Err() == nil {
		batch := s.invalidationBatch()
		if len(batch) == 0 {
			return
		}
		var err error
		if writer, ok := s.shared.(CollabSharedInvalidationWriter); ok {
			err = writer.WriteInvalidations(ctx, batch)
		} else if writer, ok := s.shared.(CollabSharedRecordsBatchWriter); ok {
			err = writer.ApplyBatch(ctx, batch)
		} else {
			for _, m := range batch {
				if err = s.shared.Apply(ctx, m); err != nil {
					break
				}
			}
		}
		if err != nil {
			collabSafety.recordErrors.Add(1)
			return
		}
		s.deferredMu.Lock()
		for _, m := range batch {
			pending := s.invalidations[m.Token.Scope]
			kept := pending[:0]
			for _, row := range pending {
				if row.Token != m.Token || !row.Issued.ExpiresAt.Equal(m.Issued.ExpiresAt) {
					kept = append(kept, row)
				}
			}
			if len(kept) == 0 {
				delete(s.invalidations, m.Token.Scope)
			} else {
				s.invalidations[m.Token.Scope] = kept
			}
		}
		s.deferredMu.Unlock()
	}
}
func (s *SafeCollabRecordsStore) runDeferred() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(s.stop, CollabRecordTimeout)
		s.flushInvalidations(ctx)
		cancel()
		s.deferredMu.Lock()
		batch := make(map[collabPublicationKey]CollabRecordMutation)
		for key, m := range s.deferred {
			batch[key] = m
			if len(batch) == 64 {
				break
			}
		}
		s.deferredMu.Unlock()
		if len(batch) == 0 {
			continue
		}
		rows := make([]CollabRecordMutation, 0, len(batch))
		for _, m := range batch {
			rows = append(rows, m)
		}
		// Publication is only an optimization; receipts recover durable admissions.
		// Rejections/replacements remain best-effort after bounded retries.
		for attempt := 0; attempt < 3 && s.stop.Err() == nil; attempt++ {
			if s.applyDeferred(rows) {
				break
			}
		}
		s.deferredMu.Lock()
		for key := range batch {
			delete(s.deferred, key)
		}
		s.deferredMu.Unlock()
	}
}
func (s *SafeCollabRecordsStore) applyDeferred(rows []CollabRecordMutation) (ok bool) {
	defer collabRecover(&collabSafety.recordPanics)
	ctx, cancel := context.WithTimeout(s.stop, CollabRecordTimeout)
	defer cancel()
	if writer, yes := s.shared.(CollabSharedRecordsBatchWriter); yes {
		if writer.ApplyBatch(ctx, rows) != nil {
			collabSafety.recordErrors.Add(1)
			return false
		}
		return true
	}
	for _, m := range rows {
		if s.shared.Apply(ctx, m) != nil {
			collabSafety.recordErrors.Add(1)
			return false
		}
	}
	return true
}
