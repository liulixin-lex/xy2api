package resilience

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func localEndingFacts() CollabLocalEndingFacts {
	s := Defaults()
	s.Collab.Ladder = true
	s.Collab.LocalEnding.Mode = CollabLocalEndingAll
	return CollabLocalEndingFacts{
		Settings: &s, GroupID: 1, Route: CollabRouteResponsesHttp, RequestKind: CollabRequestKindNormal,
		CompleteHistory: true, EvidenceGrade: CollabEvidenceGradeVerified, LatestRejectionCiphertext: true,
		SavedErrorPending: true, Now: time.Unix(100, 0),
		Delivery: CollabDeliveryKey{Scope: CollabRecordScope{Tenant: "tenant", Group: "group", Tree: "tree"},
			Thread: "thread", Item: "item", Delivery: "delivery", Digest: "content"}, Rejection: "rejection",
	}
}

func TestCollabLocalEndingEligibility(t *testing.T) {
	require.False(t, CollabLocalEndingEligible(CollabLocalEndingFacts{}))
	require.True(t, CollabLocalEndingEligible(localEndingFacts()))
	for name, change := range map[string]func(*CollabLocalEndingFacts){
		"no settings": func(f *CollabLocalEndingFacts) { f.Settings = nil },
		"master off":  func(f *CollabLocalEndingFacts) { f.Settings.Enabled = false },
		"ladder off":  func(f *CollabLocalEndingFacts) { f.Settings.Collab.Ladder = false },
		"ending off":  func(f *CollabLocalEndingFacts) { f.Settings.Collab.LocalEnding.Mode = CollabLocalEndingOff },
		"outside group": func(f *CollabLocalEndingFacts) {
			f.Settings.Collab.LocalEnding = CollabLocalEndingSettings{Mode: CollabLocalEndingGroups, Groups: []int64{2}}
		},
		"incomplete history":       func(f *CollabLocalEndingFacts) { f.CompleteHistory = false },
		"continuation":             func(f *CollabLocalEndingFacts) { f.ContinuationPointer = true },
		"claimed evidence":         func(f *CollabLocalEndingFacts) { f.EvidenceGrade = CollabEvidenceGradeClaimed },
		"unclaimed evidence":       func(f *CollabLocalEndingFacts) { f.EvidenceGrade = CollabEvidenceGradeUnclaimed },
		"missing evidence":         func(f *CollabLocalEndingFacts) { f.EvidenceGrade = "" },
		"last rejection unrelated": func(f *CollabLocalEndingFacts) { f.LatestRejectionCiphertext = false },
		"semantic output":          func(f *CollabLocalEndingFacts) { f.SemanticOutputDelivered = true },
		"terminal":                 func(f *CollabLocalEndingFacts) { f.TerminalClaimed = true },
		"write failure":            func(f *CollabLocalEndingFacts) { f.WriteFailed = true },
		"disconnect":               func(f *CollabLocalEndingFacts) { f.ClientCanceled = true },
		"no saved endpoint":        func(f *CollabLocalEndingFacts) { f.SavedErrorPending = false },
		"tree at cap":              func(f *CollabLocalEndingFacts) { f.TreeLocalEndings = CollabTreeLocalEndingLimit },
		"tree over cap":            func(f *CollabLocalEndingFacts) { f.TreeLocalEndings = CollabTreeLocalEndingLimit + 1 },
		"unreplaceable arguments":  func(f *CollabLocalEndingFacts) { f.UnreplaceableCallArgs = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := localEndingFacts()
			change(&f)
			require.False(t, CollabLocalEndingEligible(f))
		})
	}
	for _, route := range []CollabRoute{CollabRoutePassthrough, CollabRouteHttpToWs, CollabRouteWs,
		CollabRouteCompact, CollabRouteChatBridge, CollabRouteMessagesBridge, CollabRouteOther, ""} {
		t.Run(string(route), func(t *testing.T) {
			f := localEndingFacts()
			f.Route = route
			require.False(t, CollabLocalEndingEligible(f))
		})
	}
	for _, kind := range []CollabRequestKind{CollabRequestKindCompaction, CollabRequestKindReview, CollabRequestKindStructuredOutput, ""} {
		t.Run(string(kind), func(t *testing.T) {
			f := localEndingFacts()
			f.RequestKind = kind
			require.False(t, CollabLocalEndingEligible(f))
		})
	}
	f := localEndingFacts()
	f.Settings.Collab.LocalEnding = CollabLocalEndingSettings{Mode: CollabLocalEndingGroups, Groups: []int64{1}}
	f.TreeLocalEndings = CollabTreeLocalEndingLimit - 1
	require.True(t, CollabLocalEndingEligible(f))
	// These independent switches neither grant nor veto local ending.
	for _, on := range []bool{false, true} {
		f.Settings.Collab.Records, f.Settings.Collab.LosslessFirst = on, on
		f.Settings.Collab.RecordRouting, f.Settings.Collab.TreeAffinity = on, on
		f.Settings.Collab.IncrementalDeclarations, f.Settings.Collab.StrictMarkerVeto = on, on
		require.True(t, CollabLocalEndingEligible(f))
	}
}

func TestCollabLocalEndingRetryHistory(t *testing.T) {
	f := localEndingFacts()
	f.SavedErrorRetryable = true
	require.False(t, CollabLocalEndingEligible(f))
	f.PriorRejections = []CollabTerminalRejectionCount{{Key: f.Delivery, Rejection: f.Rejection, Count: 2, ExpiresAt: f.Now.Add(time.Hour)}}
	require.True(t, CollabLocalEndingEligible(f))
	for name, change := range map[string]func(*CollabLocalEndingFacts){
		"only once":                 func(f *CollabLocalEndingFacts) { f.PriorRejections[0].Count = 1 },
		"expired":                   func(f *CollabLocalEndingFacts) { f.PriorRejections[0].ExpiresAt = f.Now },
		"other rejection":           func(f *CollabLocalEndingFacts) { f.Rejection = "other" },
		"absent rejection":          func(f *CollabLocalEndingFacts) { f.Rejection = "" },
		"absent delivery":           func(f *CollabLocalEndingFacts) { f.Delivery.Delivery = "" },
		"same content new delivery": func(f *CollabLocalEndingFacts) { f.Delivery.Delivery = "new" },
		"same content new item":     func(f *CollabLocalEndingFacts) { f.Delivery.Item = "new" },
		"other tenant":              func(f *CollabLocalEndingFacts) { f.Delivery.Scope.Tenant = "other" },
		"other group":               func(f *CollabLocalEndingFacts) { f.Delivery.Scope.Group = "other" },
		"other tree":                func(f *CollabLocalEndingFacts) { f.Delivery.Scope.Tree = "other" },
		"other thread":              func(f *CollabLocalEndingFacts) { f.Delivery.Thread = "other" },
		"other content":             func(f *CollabLocalEndingFacts) { f.Delivery.Digest = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := f
			copy.PriorRejections = append([]CollabTerminalRejectionCount(nil), f.PriorRejections...)
			change(&copy)
			require.False(t, CollabLocalEndingEligible(copy))
		})
	}
	// Two separate single-rejection records are not two prior terminals.
	f.PriorRejections[0].Count = 1
	f.PriorRejections = append(f.PriorRejections, f.PriorRejections[0])
	require.False(t, CollabLocalEndingEligible(f))
	f.SavedErrorRetryable = false
	require.True(t, CollabLocalEndingEligible(f))
}

func TestCollabLocalEndingText(t *testing.T) {
	const event = "cle_example"
	const prefix = "Gateway notice [cle_example]. "
	wants := map[CollabRole]string{
		CollabRoleRoot:    "Some required input could not be read, so this turn ended here. This reply does not confirm task completion. Providing the missing content in plain text may allow another attempt.",
		CollabRoleUnknown: "Some input could not be read, so this turn ended here. Earlier work may have taken effect. A readable copy of the missing content is needed to continue.",
		CollabRoleChild:   "This turn ended because the task content could not be read. Earlier work may have taken effect. Existing results need to be checked before another attempt.",
	}
	for _, role := range []CollabRole{CollabRoleRoot, CollabRoleChild, CollabRoleUnknown, ""} {
		for _, progress := range []CollabExecution{CollabExecutionNotStarted, CollabExecutionPartial, CollabExecutionUnknown, ""} {
			want, ok := wants[role]
			if !ok {
				want = wants[CollabRoleUnknown]
			}
			if role == CollabRoleChild && progress == CollabExecutionNotStarted {
				want = "The task content could not be read. This turn ended before work began. A readable copy is needed to retry."
			}
			require.Equal(t, prefix+want, CollabLocalEndingText(event, role, progress, false, false))
			for _, flags := range [][2]bool{{true, false}, {false, true}, {true, true}} {
				require.Equal(t, prefix+"The same task could not be delivered. This turn ended here. Repeating this delivery has not resolved the issue. Another execution path or a report of the missing input is needed.",
					CollabLocalEndingText(event, role, progress, flags[0], flags[1]))
			}
		}
	}
}
