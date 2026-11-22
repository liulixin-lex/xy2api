package resilience

import (
	"slices"
	"time"
)

// Items are in original input order. Only protected ciphertext participates;
// multiple sources use the most recent unfinished task.
type CollabRoutingItem struct {
	Key                       CollabDeliveryKey
	Protected, UnfinishedTask bool
	Records                   CollabRecordLookup
	Rejected                  map[CollabDigest]bool
}

// OrderCollabFailoverCandidates must be called separately inside each existing
// admission/quality round. It returns the original slice when no rank changes.
func OrderCollabFailoverCandidates(settings *Settings, candidates []int64, accounts map[int64]CollabDigest, items []CollabRoutingItem, now time.Time) []int64 {
	if !settings.CollabRecordRoutingOn() || len(candidates) < 2 {
		return candidates
	}
	var chosen *CollabRoutingItem
	protected := 0
	for i := range items {
		if items[i].Protected {
			protected++
			if chosen == nil || items[i].UnfinishedTask {
				chosen = &items[i]
			}
		}
	}
	if chosen == nil || (protected > 1 && !chosen.UnfinishedTask) {
		return candidates
	}
	rank := func(id int64) int {
		account := accounts[id]
		if account == "" || chosen.Rejected[account] {
			return 2
		}
		for _, r := range chosen.Records.Consumed {
			if r.Account == account && r.Key == chosen.Key && r.ExpiresAt.After(now) {
				return 0
			}
		}
		for _, r := range chosen.Records.Issued {
			if r.Account == account && r.Scope == chosen.Key.Scope && r.Digest == chosen.Key.Digest && r.Status == CollabIssuedStatusConfirmed && r.ExpiresAt.After(now) {
				return 1
			}
		}
		return 2
	}
	ordered := true
	for i := 1; i < len(candidates); i++ {
		if rank(candidates[i-1]) > rank(candidates[i]) {
			ordered = false
			break
		}
	}
	if ordered {
		return candidates
	}
	out := slices.Clone(candidates)
	slices.SortStableFunc(out, func(a, b int64) int { return rank(a) - rank(b) })
	return out
}

// Parsed notice is untrusted until matched to a live gateway-issued termination.
type CollabSeenNotice struct {
	Event, Sender CollabDigest
	FinalAnswer   bool
	Current       bool
}

func CollabSeenEvent(notice CollabSeenNotice, scope CollabRecordScope, thread CollabDigest, records []CollabTerminatedRecord, now time.Time) CollabEventObservation {
	result := CollabEventObservation{Event: notice.Event, Source: CollabEventSourceSeenOther}
	if notice.Event == "" {
		return result
	}
	for _, r := range records {
		if r.Event == notice.Event && r.Key.Scope == scope && r.ExpiresAt.After(now) {
			result.Delivery = r.Key.Delivery
			if notice.FinalAnswer && thread != "" && r.Parent == thread && notice.Sender != "" && r.Key.Thread == notice.Sender {
				result.Source = CollabEventSourceSeenParentNotice
				return result
			}
		}
	}
	return result
}

type CollabBorrowInput struct {
	Settings               *Settings
	Scope                  CollabRecordScope
	Thread                 CollabDigest
	OriginalAccountID      int64
	Notices                []CollabSeenNotice
	Terminated             []CollabTerminatedRecord
	ParentUnreadCiphertext bool
	// Every compaction/call-argument ciphertext carried by the parent.
	Carried []CollabRoutingItem
	// Computed by the real planner using mapped model, protocol, flattening,
	// account and global switches. Only already-admitted immediate targets.
	Convertible                     map[int64]bool
	Immediate                       map[int64]bool
	Accounts                        map[int64]CollabDigest
	FirstCollaborationCallDelivered bool
}
type CollabBorrowDecision struct {
	AccountID int64
	Policy    CollabBindingPolicy
}

// DecideCollabRedispatchBorrow never changes candidates, waits, or binds.
func DecideCollabRedispatchBorrow(in CollabBorrowInput, candidates []int64, now time.Time) CollabBorrowDecision {
	if !in.Settings.CollabRedispatchBorrowOn() || in.ParentUnreadCiphertext || in.FirstCollaborationCallDelivered || in.OriginalAccountID <= 0 || in.Convertible[in.OriginalAccountID] {
		return CollabBorrowDecision{}
	}
	seen := false
	for _, notice := range in.Notices {
		if notice.Current && CollabSeenEvent(notice, in.Scope, in.Thread, in.Terminated, now).Source == CollabEventSourceSeenParentNotice {
			seen = true
			break
		}
	}
	if !seen {
		return CollabBorrowDecision{}
	}
	for _, id := range candidates {
		if id == in.OriginalAccountID || !in.Convertible[id] || !in.Immediate[id] {
			continue
		}
		account := in.Accounts[id]
		if account == "" {
			continue
		}
		readable := true
		for _, item := range in.Carried {
			consumed := false
			for _, r := range item.Records.Consumed {
				if r.Key == item.Key && r.Key.Scope == in.Scope && r.Account == account && r.ExpiresAt.After(now) {
					consumed = true
					break
				}
			}
			if !consumed || item.Rejected[account] {
				readable = false
				break
			}
		}
		if readable {
			return CollabBorrowDecision{AccountID: id, Policy: CollabBindingPolicy{Borrow: true, PreserveAdmissionBinding: true, PreserveSuccessBinding: true, UntilFirstCollaborationCall: true}}
		}
	}
	return CollabBorrowDecision{}
}

// A rejected/failed borrow always returns the immutable original and account;
// the caller re-runs normal admission and the existing forwarding path.
func CollabRedispatchFallback(originalAccountID int64, original []byte) (int64, []byte) {
	return originalAccountID, original
}
