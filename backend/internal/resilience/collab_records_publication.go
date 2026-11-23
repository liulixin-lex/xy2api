package resilience

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

type collabPublicationKey struct {
	scope CollabRecordScope
	field string
}
type collabPublication struct {
	done  chan struct{}
	count int
	ok    bool
}

func collabAdmissionPublicationKey(rows []CollabRecordMutation) collabPublicationKey {
	// Expiry is not part of an idempotent notice identity.
	copyRows := append([]CollabRecordMutation(nil), rows...)
	for i := range copyRows {
		copyRows[i].Issued.ExpiresAt = time.Time{}
		copyRows[i].Terminated.ExpiresAt = time.Time{}
	}
	payload, _ := json.Marshal(copyRows)
	digest := sha256.Sum256(payload)
	return collabPublicationKey{scope: rows[0].Scope, field: "a:" + hex.EncodeToString(digest[:])}
}

// There is no acknowledged disposition queue to lose on shutdown. At most 128
// shared calls (64 per tenant) remain in flight, even if a backend ignores cancel.
// Identical concurrent calls share one result. A timeout is never success.
func (s *SafeCollabRecordsStore) required(ctx context.Context, key collabPublicationKey, call func(context.Context) (int, bool)) (int, bool) {
	bounded, cancel := context.WithTimeout(ctx, CollabRecordTimeout)
	defer cancel()
	if bounded.Err() != nil || s.stop.Err() != nil {
		return CollabTreeLocalEndingLimit, false
	}
	s.publicationMu.Lock()
	pending := s.publications[key]
	if pending == nil {
		tenant := 0
		for k := range s.publications {
			if k.scope.Tenant == key.scope.Tenant {
				tenant++
			}
		}
		if len(s.publications) >= 128 || tenant >= 64 {
			s.publicationMu.Unlock()
			return CollabTreeLocalEndingLimit, false
		}
		pending = &collabPublication{done: make(chan struct{}), count: CollabTreeLocalEndingLimit}
		s.publications[key] = pending
		go func() {
			stop := context.AfterFunc(s.stop, cancel)
			defer stop()
			func() { defer collabRecover(&collabSafety.recordPanics); pending.count, pending.ok = call(bounded) }()
			if bounded.Err() != nil {
				pending.ok = false
			}
			s.publicationMu.Lock()
			delete(s.publications, key)
			close(pending.done)
			s.publicationMu.Unlock()
		}()
	}
	s.publicationMu.Unlock()
	select {
	case <-bounded.Done():
		return CollabTreeLocalEndingLimit, false
	case <-s.stop.Done():
		return CollabTreeLocalEndingLimit, false
	case <-pending.done:
		if bounded.Err() != nil || !pending.ok {
			return CollabTreeLocalEndingLimit, false
		}
		return pending.count, true
	}
}
