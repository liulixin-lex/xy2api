package resilience

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// CollabTreeAffinity is a bounded, process-local store. Losing a hint is safe:
// callers fall back to ordinary selection. It never performs network I/O.
// The embedded no-op methods leave record ordering and borrowing to their owner.
type CollabTreeAffinity struct {
	NoopCollabScheduler
	mu         sync.Mutex
	capacity   int
	ttl        time.Duration
	entries    map[collabAffinityKey]*list.Element
	lru        list.List
	avoidances map[collabAffinityThread]map[int64]*list.Element
	leases     map[CollabRecordScope]map[CollabDeliveryKey]*list.Element
}

type collabAffinityThread struct {
	scope  CollabRecordScope
	thread CollabDigest
}

type collabAffinityKey struct {
	kind     uint8
	scope    CollabRecordScope
	thread   CollabDigest
	account  int64
	delivery CollabDeliveryKey
}

type collabAffinityEntry struct {
	key      collabAffinityKey
	until    time.Time
	hint     CollabTreeHint
	route    CollabThreadRoute
	lease    CollabDeliveryLease
	released bool
	written  time.Time
}

const (
	collabAffinityTree uint8 = iota + 1
	collabAffinityMember
	collabAffinityRoute
	collabAffinityAvoid
	collabAffinityLease
	collabAffinityBinding
)

// ttl must be the gateway's existing sticky-session TTL. All keys are already
// keyed digests; the versioned tree namespace contains no model or credentials.
func NewCollabTreeAffinity(capacity int, ttl time.Duration) *CollabTreeAffinity {
	if capacity <= 0 || capacity > CollabRecordMaxEntries {
		capacity = CollabRecordMaxEntries
	}
	return &CollabTreeAffinity{capacity: capacity, ttl: ttl, entries: make(map[collabAffinityKey]*list.Element),
		avoidances: make(map[collabAffinityThread]map[int64]*list.Element), leases: make(map[CollabRecordScope]map[CollabDeliveryKey]*list.Element)}
}

func collabAffinityScopeValid(scope CollabRecordScope) bool {
	return scope.Tenant != "" && scope.Group != "" && scope.Tree != ""
}

func collabMembershipKey(scope CollabRecordScope, thread CollabDigest) collabAffinityKey {
	// Membership resolves an unknown tree from tenant/group/thread alone.
	scope.Tree = ""
	return collabAffinityKey{kind: collabAffinityMember, scope: scope, thread: thread}
}

func (s *CollabTreeAffinity) expiry(until, now time.Time) time.Time {
	limit := now.Add(s.ttl)
	if until.IsZero() || until.After(limit) {
		return limit
	}
	return until
}

// All cache helpers run under mu. Reads do not extend record lifetimes.
func (s *CollabTreeAffinity) get(key collabAffinityKey, now time.Time) *collabAffinityEntry {
	element := s.entries[key]
	if element == nil {
		return nil
	}
	entry, _ := element.Value.(*collabAffinityEntry)
	if !now.Before(entry.until) {
		s.remove(element)
		return nil
	}
	s.lru.MoveToFront(element)
	return entry
}

func (s *CollabTreeAffinity) put(entry collabAffinityEntry) {
	element := s.entries[entry.key]
	if element != nil {
		s.unindex(entry.key)
		element.Value = &entry
		s.lru.MoveToFront(element)
	} else {
		element = s.lru.PushFront(&entry)
		s.entries[entry.key] = element
	}
	if entry.key.kind == collabAffinityAvoid {
		thread := collabAffinityThread{entry.key.scope, entry.key.thread}
		if s.avoidances[thread] == nil {
			s.avoidances[thread] = make(map[int64]*list.Element)
		}
		s.avoidances[thread][entry.key.account] = element
	} else if entry.key.kind == collabAffinityLease && !entry.released {
		if s.leases[entry.key.scope] == nil {
			s.leases[entry.key.scope] = make(map[CollabDeliveryKey]*list.Element)
		}
		s.leases[entry.key.scope][entry.key.delivery] = element
	}
	if s.lru.Len() > s.capacity {
		s.remove(s.lru.Back())
	}
}

func (s *CollabTreeAffinity) unindex(key collabAffinityKey) {
	switch key.kind {
	case collabAffinityAvoid:
		thread := collabAffinityThread{key.scope, key.thread}
		delete(s.avoidances[thread], key.account)
		if len(s.avoidances[thread]) == 0 {
			delete(s.avoidances, thread)
		}
	case collabAffinityLease:
		delete(s.leases[key.scope], key.delivery)
		if len(s.leases[key.scope]) == 0 {
			delete(s.leases, key.scope)
		}
	}
}

func (s *CollabTreeAffinity) remove(element *list.Element) {
	entry, _ := element.Value.(*collabAffinityEntry)
	s.unindex(entry.key)
	delete(s.entries, entry.key)
	s.lru.Remove(element)
}

// Active tree admissions renew the tree's membership as a whole without an
// O(tree-size) write on every request. Eviction still behaves as a hint miss.
func (s *CollabTreeAffinity) member(scope CollabRecordScope, thread CollabDigest, now time.Time) *collabAffinityEntry {
	key := collabMembershipKey(scope, thread)
	if element := s.entries[key]; element != nil {
		entry, _ := element.Value.(*collabAffinityEntry)
		if root := s.get(collabAffinityKey{kind: collabAffinityTree, scope: entry.hint.Scope}, now); root != nil && root.until.After(entry.until) {
			entry.until, entry.hint.ExpiresAt = root.until, root.until
		}
	}
	return s.get(key, now)
}

func (s *CollabTreeAffinity) ProvisionalTreeHint(_ context.Context, hint CollabTreeHint) bool {
	if s == nil || !collabAffinityScopeValid(hint.Scope) || hint.Thread == "" || hint.Parent != "" || hint.AccountID <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	hint.ExpiresAt = s.expiry(hint.ExpiresAt, now)
	if !now.Before(hint.ExpiresAt) {
		return false
	}
	key := collabAffinityKey{kind: collabAffinityTree, scope: hint.Scope}
	if old := s.get(key, now); old != nil && old.hint.Thread != hint.Thread {
		return false
	}
	hint.Status = CollabTreeHintStatusProvisional
	s.put(collabAffinityEntry{key: key, until: hint.ExpiresAt, hint: hint})
	s.put(collabAffinityEntry{key: collabMembershipKey(hint.Scope, hint.Thread), until: hint.ExpiresAt, hint: hint})
	return true
}

func (s *CollabTreeAffinity) ConfirmTreeHint(_ context.Context, hint CollabTreeHint) bool {
	if s == nil || hint.Parent != "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.get(collabAffinityKey{kind: collabAffinityTree, scope: hint.Scope}, time.Now())
	if entry == nil || entry.hint.Thread != hint.Thread || entry.hint.AccountID != hint.AccountID {
		return false
	}
	entry.hint.Status = CollabTreeHintStatusConfirmed
	return true
}

func (s *CollabTreeAffinity) LookupTreeHint(_ context.Context, scope CollabRecordScope) (CollabTreeHint, bool) {
	if s == nil {
		return CollabTreeHint{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.get(collabAffinityKey{kind: collabAffinityTree, scope: scope}, time.Now()); entry != nil {
		return entry.hint, true
	}
	return CollabTreeHint{}, false
}

func (s *CollabTreeAffinity) RefreshTreeMembership(_ context.Context, hint CollabTreeHint) bool {
	if s == nil || !collabAffinityScopeValid(hint.Scope) || hint.Thread == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	hint.ExpiresAt = s.expiry(hint.ExpiresAt, now)
	key := collabMembershipKey(hint.Scope, hint.Thread)
	if !now.Before(hint.ExpiresAt) {
		return false
	}
	if old := s.member(hint.Scope, hint.Thread, now); old != nil && (old.hint.Scope != hint.Scope || old.hint.Parent != hint.Parent) {
		return false
	}
	if root := s.get(collabAffinityKey{kind: collabAffinityTree, scope: hint.Scope}, now); root != nil {
		root.until, root.hint.ExpiresAt = hint.ExpiresAt, hint.ExpiresAt
	}
	s.put(collabAffinityEntry{key: key, until: hint.ExpiresAt, hint: hint})
	return true
}

func (s *CollabTreeAffinity) LookupThread(_ context.Context, scope CollabRecordScope, thread CollabDigest) (CollabTreeHint, bool) {
	if s == nil {
		return CollabTreeHint{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.member(scope, thread, time.Now()); entry != nil && (scope.Tree == "" || scope.Tree == entry.hint.Scope.Tree) {
		return entry.hint, true
	}
	return CollabTreeHint{}, false
}

func (s *CollabTreeAffinity) BindRecoveredThread(_ context.Context, route CollabThreadRoute) bool {
	if s == nil || !collabAffinityScopeValid(route.Scope) || route.Thread == "" || route.AccountID <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	route.ExpiresAt = s.expiry(route.ExpiresAt, now)
	if !now.Before(route.ExpiresAt) {
		return false
	}
	s.put(collabAffinityEntry{key: collabAffinityKey{kind: collabAffinityRoute, scope: route.Scope, thread: route.Thread}, until: route.ExpiresAt, route: route})
	return true
}

func (s *CollabTreeAffinity) LookupThreadRoute(_ context.Context, scope CollabRecordScope, thread CollabDigest) (CollabThreadRoute, bool) {
	if s == nil {
		return CollabThreadRoute{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.get(collabAffinityKey{kind: collabAffinityRoute, scope: scope, thread: thread}, time.Now()); entry != nil {
		return entry.route, true
	}
	return CollabThreadRoute{}, false
}

func (s *CollabTreeAffinity) AvoidThread(_ context.Context, avoidance CollabThreadAvoidance) bool {
	if s == nil || !collabAffinityScopeValid(avoidance.Scope) || avoidance.Thread == "" || avoidance.AccountID <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	// Rejection timing and expiry are owned by today's avoidance policy, not
	// by the independent tree-hint TTL.
	until := avoidance.ExpiresAt
	if !now.Before(until) {
		return false
	}
	key := collabAffinityKey{kind: collabAffinityAvoid, scope: avoidance.Scope, thread: avoidance.Thread, account: avoidance.AccountID}
	if old := s.get(key, now); old != nil && old.until.After(until) {
		until = old.until
	}
	s.put(collabAffinityEntry{key: key, until: until, written: now})
	return true
}

func (s *CollabTreeAffinity) ThreadAvoided(_ context.Context, avoidance CollabThreadAvoidance) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(collabAffinityKey{kind: collabAffinityAvoid, scope: avoidance.Scope, thread: avoidance.Thread, account: avoidance.AccountID}, time.Now()) != nil
}

// ThreadAvoidances is a snapshot for the ordinary selector's existing exclusion
// path. It is never a tree-wide exclusion and does not consume a retry budget.
func (s *CollabTreeAffinity) ThreadAvoidances(_ context.Context, scope CollabRecordScope, thread CollabDigest) map[int64]struct{} {
	return s.ThreadAvoidancesAfterReset(context.Background(), scope, thread, nil)
}

func (s *CollabTreeAffinity) ThreadAvoidancesAfterReset(_ context.Context, scope CollabRecordScope, thread CollabDigest, resetAt func(int64) time.Time) map[int64]struct{} {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var avoided map[int64]struct{}
	now := time.Now()
	for account, element := range s.avoidances[collabAffinityThread{scope, thread}] {
		entry, _ := element.Value.(*collabAffinityEntry)
		if !now.Before(entry.until) || resetAt != nil && entry.written.Before(resetAt(account)) {
			s.remove(element)
		} else {
			if avoided == nil {
				avoided = make(map[int64]struct{})
			}
			avoided[account] = struct{}{}
		}
	}
	return avoided
}

// A present binding with account zero is a deletion tombstone, not absence.
func (s *CollabTreeAffinity) ThreadBinding(key CollabDigest) (account int64, until time.Time, ok bool) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.get(collabAffinityKey{kind: collabAffinityBinding, thread: key}, time.Now()); e != nil {
		return e.route.AccountID, e.until, true
	}
	return
}

func (s *CollabTreeAffinity) PutThreadBinding(key CollabDigest, account int64, until time.Time) {
	if s == nil || key == "" || account < 0 || !time.Now().Before(until) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.put(collabAffinityEntry{key: collabAffinityKey{kind: collabAffinityBinding, thread: key}, until: until, route: CollabThreadRoute{AccountID: account}})
}

func (s *CollabTreeAffinity) AcquireDeliveryLease(_ context.Context, lease CollabDeliveryLease) bool {
	if s == nil || !collabAffinityScopeValid(lease.Key.Scope) || lease.Key.Thread == "" || lease.Key.Item == "" || lease.Key.Delivery == "" || lease.Key.Digest == "" || lease.AccountID <= 0 || lease.Issued.Token == "" || lease.Issued.Scope != lease.Key.Scope {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	key := collabAffinityKey{kind: collabAffinityLease, scope: lease.Key.Scope, delivery: lease.Key}
	if old := s.get(key, now); old != nil {
		return !old.released && old.lease.Issued == lease.Issued
	}
	lease.ExpiresAt = s.expiry(lease.ExpiresAt, now)
	if !now.Before(lease.ExpiresAt) {
		return false
	}
	s.put(collabAffinityEntry{key: key, until: lease.ExpiresAt, lease: lease})
	return true
}

func (s *CollabTreeAffinity) ReleaseDeliveryLease(_ context.Context, key CollabDeliveryKey, reason CollabLeaseRelease) {
	if s == nil || !collabAffinityScopeValid(key.Scope) || key.Thread == "" || key.Item == "" || key.Delivery == "" || key.Digest == "" || (reason != CollabLeaseReleaseConsumed && reason != CollabLeaseReleaseTerminated) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	cacheKey := collabAffinityKey{kind: collabAffinityLease, scope: key.Scope, delivery: key}
	if entry := s.get(cacheKey, now); entry != nil {
		// Keep a tombstone until expiry so duplicate completion frames cannot
		// recreate a delivery which has already been read or terminated.
		entry.released = true
		s.unindex(cacheKey)
	} else if until := now.Add(s.ttl); now.Before(until) {
		// Acceptance can race ahead of issuance's Written consumer. Retain
		// absence as a tombstone so that late delivery cannot create a lease.
		s.put(collabAffinityEntry{key: cacheKey, until: until, released: true})
	}
}

func (s *CollabTreeAffinity) HasDeliveryLeases(_ context.Context, scope CollabRecordScope) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, element := range s.leases[scope] {
		entry, _ := element.Value.(*collabAffinityEntry)
		if now.Before(entry.until) {
			return true
		}
		s.remove(element)
	}
	return false
}

var _ CollabScheduler = (*CollabTreeAffinity)(nil)
