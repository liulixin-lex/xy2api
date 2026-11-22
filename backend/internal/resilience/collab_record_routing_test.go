package resilience

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollabOrderingPreservesCandidatesAndRound(t *testing.T) {
	settings := Defaults()
	settings.Collab.Ladder = true
	settings.Collab.RecordRouting = true
	now := time.Now()
	key := collabTestKey()
	expiry := now.Add(time.Hour)
	item := CollabRoutingItem{Key: key, Protected: true, UnfinishedTask: true, Records: CollabRecordLookup{
		Issued:   []CollabIssuedRecord{{Scope: key.Scope, Digest: key.Digest, Account: "issuer", Status: CollabIssuedStatusConfirmed, ExpiresAt: expiry}},
		Consumed: []CollabConsumedRecord{{Key: key, Account: "reader", ExpiresAt: expiry}},
	}}
	candidates := []int64{9, 1, 2, 8}
	accounts := map[int64]CollabDigest{1: "issuer", 2: "reader"}
	ordered := OrderCollabFailoverCandidates(&settings, candidates, accounts, []CollabRoutingItem{item}, now)
	require.Equal(t, []int64{2, 1, 9, 8}, ordered)
	require.Equal(t, []int64{9, 1, 2, 8}, candidates)
	require.ElementsMatch(t, candidates, ordered)
	// Omitted hard/quality candidates cannot be reintroduced.
	round := []int64{9, 8}
	require.Same(t, &round[0], &OrderCollabFailoverCandidates(&settings, round, accounts, []CollabRoutingItem{item}, now)[0])
	item.Rejected = map[CollabDigest]bool{"reader": true, "issuer": true}
	require.Same(t, &candidates[0], &OrderCollabFailoverCandidates(&settings, candidates, accounts, []CollabRoutingItem{item}, now)[0])
	item.Rejected = nil
	require.Equal(t, candidates, OrderCollabFailoverCandidates(&settings, candidates, accounts, []CollabRoutingItem{item}, expiry))
	settings.Collab.RecordRouting = false
	require.Equal(t, candidates, OrderCollabFailoverCandidates(&settings, candidates, accounts, []CollabRoutingItem{item}, now))
	settings.Collab.RecordRouting = true
	settings.Collab.Records = false // dispositions remain independent; routing evidence does not
	require.Equal(t, candidates, OrderCollabFailoverCandidates(&settings, candidates, accounts, []CollabRoutingItem{item}, now))
	settings.Collab.Records = true
	newer := item
	newer.Key.Item = "new"
	newer.Records = CollabRecordLookup{}
	require.Equal(t, candidates, OrderCollabFailoverCandidates(&settings, candidates, accounts, []CollabRoutingItem{item, newer}, now))
	item.Protected = false
	require.Equal(t, candidates, OrderCollabFailoverCandidates(&settings, candidates, accounts, []CollabRoutingItem{item}, now))
}
func TestCollabBorrowGatesAndLosslessFallback(t *testing.T) {
	now := time.Now()
	key := collabTestKey()
	settings := Defaults()
	settings.Collab.Ladder = true
	settings.Collab.RecordRouting = true
	settings.Collab.LosslessFirst = true
	input := CollabBorrowInput{Settings: &settings, Scope: key.Scope, Thread: "parent", OriginalAccountID: 1,
		Notices:     []CollabSeenNotice{{Event: "event", Sender: key.Thread, FinalAnswer: true, Current: true}},
		Terminated:  []CollabTerminatedRecord{{Key: key, Event: "event", Parent: "parent", ExpiresAt: now.Add(time.Hour)}},
		Convertible: map[int64]bool{2: true, 3: true}, Immediate: map[int64]bool{2: true, 3: true}, Accounts: map[int64]CollabDigest{2: "account", 3: "other"},
	}
	candidates := []int64{1, 2}
	decision := DecideCollabRedispatchBorrow(input, candidates, now)
	require.Equal(t, int64(2), decision.AccountID)
	require.True(t, decision.Policy.Borrow)
	require.True(t, decision.Policy.PreserveAdmissionBinding)
	require.True(t, decision.Policy.PreserveSuccessBinding)
	require.True(t, decision.Policy.UntilFirstCollaborationCall)
	require.Equal(t, []int64{1, 2}, candidates)
	for _, change := range []func(*CollabBorrowInput){
		func(i *CollabBorrowInput) { i.ParentUnreadCiphertext = true },
		func(i *CollabBorrowInput) { i.Carried = []CollabRoutingItem{{Key: key}} },
		func(i *CollabBorrowInput) { i.Notices = nil },
		func(i *CollabBorrowInput) {
			i.Notices = []CollabSeenNotice{{Event: "event", Sender: key.Thread, FinalAnswer: true}}
		},
		func(i *CollabBorrowInput) { i.Terminated = nil },
		func(i *CollabBorrowInput) { i.Thread = "other" },
		func(i *CollabBorrowInput) { i.Immediate = nil },
		func(i *CollabBorrowInput) { i.Convertible = nil },
		func(i *CollabBorrowInput) { i.Convertible = map[int64]bool{1: true, 2: true} },
		func(i *CollabBorrowInput) { i.FirstCollaborationCallDelivered = true },
	} {
		modified := input
		change(&modified)
		require.Empty(t, DecideCollabRedispatchBorrow(modified, candidates, now))
	}
	for _, family := range []string{"compaction", "call_args"} {
		t.Run(family, func(t *testing.T) {
			modified := input
			modified.Carried = []CollabRoutingItem{{Key: key, Records: CollabRecordLookup{Consumed: []CollabConsumedRecord{{Key: key, Account: "account", ExpiresAt: now.Add(time.Hour)}}}}}
			require.Equal(t, int64(2), DecideCollabRedispatchBorrow(modified, candidates, now).AccountID)
			require.Empty(t, DecideCollabRedispatchBorrow(modified, []int64{1, 3}, now))
		})
	}
	settings.Collab.LosslessFirst = false
	require.Empty(t, DecideCollabRedispatchBorrow(input, candidates, now))
	original := []byte("immutable original ciphertext")
	account, body := CollabRedispatchFallback(1, original)
	require.Equal(t, int64(1), account)
	require.Equal(t, original, body)
	require.Same(t, &original[0], &body[0])
}
