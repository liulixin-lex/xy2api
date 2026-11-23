package resilience

import (
	"context"
	"encoding/json"
	"time"
)

// PrepareCollabLocalEnding bounds and normalizes the whole admission before any
// lock or network call. All rows have one scope, event and reservation lifetime.
func PrepareCollabLocalEnding(records []CollabTerminatedRecord, now time.Time) ([]CollabRecordMutation, bool) {
	if len(records) == 0 || len(records) > CollabRecordMaxTreeEntries {
		return nil, false
	}
	first := records[0]
	if !validCollabDelivery(first.Key) || first.Event == "" || !first.ExpiresAt.After(now) {
		return nil, false
	}
	expiry := first.ExpiresAt
	if expiry.After(now.Add(CollabRecordTTL)) {
		expiry = now.Add(CollabRecordTTL)
	}
	counter, _, ok := NormalizeCollabRecordMutation(CollabRecordMutation{Kind: CollabRecordMutationLocalEnding, Scope: first.Key.Scope, Event: first.Event, Issued: CollabIssuedRecord{ExpiresAt: expiry}}, now)
	if !ok {
		return nil, false
	}
	rows := []CollabRecordMutation{counter}
	seen := make(map[string]bool)
	for _, r := range records {
		if !validCollabDelivery(r.Key) || r.Key.Scope != first.Key.Scope || r.Event != first.Event {
			return nil, false
		}
		r.ExpiresAt = expiry
		m, _, ok := NormalizeCollabRecordMutation(CollabRecordMutation{Kind: CollabRecordMutationTerminated, Terminated: r, Reserved: true}, now)
		if !ok {
			return nil, false
		}
		_, field, _ := CollabRecordStorageIdentity(m)
		if seen[field] {
			return nil, false
		}
		seen[field] = true
		rows = append(rows, m)
	}
	return rows, true
}

func (s *MemoryCollabRecordsStore) ReserveLocalEnding(ctx context.Context, records []CollabTerminatedRecord) (int, bool) {
	now := time.Now()
	rows, ok := PrepareCollabLocalEnding(records, now)
	if !ok || ctx.Err() != nil {
		return CollabTreeLocalEndingLimit, false
	}
	tree := s.mutationTree(rows[0].Scope, true, now)
	if tree == nil {
		return CollabTreeLocalEndingLimit, false
	}
	tree.mu.Lock()
	defer func() { tree.mu.Unlock(); tree.active.Add(-1) }()
	for field, entry := range tree.entries {
		if !collabRecordExpiry(entry.mutation).After(now) {
			tree.remove(field)
		}
	}
	counts := [3]int{}
	used := [3]int{}
	for p := range counts {
		counts[p], used[p] = tree.partitionSize(p)
	}
	prepared := make(map[string]collabMemoryEntry, len(rows))
	for _, m := range rows {
		_, field, _ := CollabRecordStorageIdentity(m)
		if old, exists := tree.entries[field]; exists {
			if m.Kind == CollabRecordMutationTerminated {
				m.Terminated.ExpiresAt = old.mutation.Terminated.ExpiresAt
				m.Reserved = old.mutation.Reserved
				a, _ := json.Marshal(m)
				b, _ := json.Marshal(old.mutation)
				if string(a) != string(b) {
					return CollabTreeLocalEndingLimit, false
				}
			}
			continue
		}
		payload, _ := json.Marshal(m)
		charge := collabRecordCharge(field, payload)
		part := collabRecordPartition(m.Kind)
		counts[part]++
		used[part] += charge
		if counts[part] > CollabRecordMaxTreeEntries || used[part] > collabPartitionBytes(part) || counts[2] > CollabTreeLocalEndingLimit {
			return CollabTreeLocalEndingLimit, false
		}
		prepared[field] = collabMemoryEntry{mutation: m, charge: charge, written: now.UnixNano()}
	}
	for field, entry := range prepared {
		tree.entries[field] = entry
		tree.bytes += entry.charge
		for _, index := range collabMutationIndexes(entry.mutation) {
			if tree.index[index] == nil {
				tree.index[index] = make(map[string]struct{})
			}
			tree.index[index][field] = struct{}{}
		}
		expiry := collabRecordExpiry(entry.mutation).UnixNano()
		if expiry > tree.expires.Load() {
			tree.expires.Store(expiry)
		}
		if expiry > tree.protectedUntil.Load() {
			tree.protectedUntil.Store(expiry)
		}
	}
	tree.lastWritten.Store(now.UnixNano())
	return counts[2], true
}
