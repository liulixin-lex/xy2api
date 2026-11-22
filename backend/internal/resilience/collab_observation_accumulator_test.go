//go:build unit

package resilience

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func observationDigest() CollabDigest { return CollabDigest(strings.Repeat("a", 64)) }

func TestCollabObservationSealsEveryPhysicalSendOnce(t *testing.T) {
	a := NewCollabObservationAccumulator(CollabObservation{Thread: observationDigest()}, true)
	count := 0
	for i := 1; i <= 20; i++ {
		send := CollabSendObservation{Seq: CollabSendSequence(i), Generation: CollabGeneration(i), Account: observationDigest(), Action: CollabActionGenericFailover, Result: CollabSendResultCipherRejected, RawReason: CollabPlanReasonNamespaceUpdate, PlanReason: CollabPlanReasonNone, Eligibility: CollabEligibilityAccountOff, RestoreError: CollabRestoreErrorInvalidItem, CallCount: &count, Marker: CollabMarkerCounts{Add: &count}}
		if i == 20 {
			send.Result = CollabSendResultCanceled
		}
		require.True(t, a.SealSend(send))
		require.False(t, a.SealSend(send))
	}
	require.True(t, a.SealSend(CollabSendObservation{Seq: 21, Result: CollabSendResultNotSent}))
	count = 91
	frozen := a.Freeze()
	require.Equal(t, 20, frozen.SendTotal)
	require.Equal(t, 4, frozen.SendOverflow)
	require.Len(t, frozen.Sends, 16)
	s := frozen.Sends[0]
	require.Equal(t, CollabPlanReasonNamespaceUpdate, s.RawReason)
	require.Equal(t, CollabPlanReasonNone, s.PlanReason)
	require.Equal(t, CollabEligibilityAccountOff, s.Eligibility)
	require.Equal(t, CollabRestoreErrorInvalidItem, s.RestoreError)
	require.Equal(t, 0, *s.CallCount)
	raw := EncodeCollabObservation(frozen)
	require.Contains(t, string(raw), "\"call_count\":0")
	require.Contains(t, string(raw), "\"upstream_empty\":null")
	require.Contains(t, string(raw), "\"add\":0")
	*frozen.Sends[0].CallCount = 9
	require.Equal(t, 0, *a.Freeze().Sends[0].CallCount)
	require.False(t, a.SealSend(CollabSendObservation{Seq: 22}))
}

func TestCollabObservationBoundedDeepCopyAndByteCap(t *testing.T) {
	d := CollabDeliveryObservation{Delivery: observationDigest(), Item: observationDigest(), Digest: observationDigest(), Position: CollabPositionCallArgs, Form: CollabFormCiphertext, Kind: CollabMessageKindToolOutput, Class: CollabItemClassUnreadMessage, Outcome: CollabDeliveryOutcomePlaceholderReplay, Evidence: CollabEvidenceGradeVerified, Stage: CollabStageReplaceAll, Scope: CollabScopeWholeItem, Bytes: 400}
	v := CollabObservation{Replacements: CollabReplacementCounts{Task: 73}, LocalCount: 2, InputTransformations: &CollabInputTransformations{Counts: map[CollabTransformationReason]int{CollabTransformationReasonReasoningRemoved: 4}}}
	zero := 0
	for i := 0; i < 100; i++ {
		v.Deliveries = append(v.Deliveries, d)
		v.Sends = append(v.Sends, CollabSendObservation{Seq: CollabSendSequence(i + 1), CallCount: &zero, Marker: CollabMarkerCounts{Add: &zero}, MarkerReasons: []CollabMarkerReason{CollabMarkerReasonChangedArguments}})
		v.Events = append(v.Events, CollabEventObservation{Event: observationDigest(), Delivery: observationDigest(), Source: CollabEventSourceSeenParentNotice})
		v.Hints = append(v.Hints, CollabHintObservation{Kind: CollabHintKindIssuer})
	}
	raw := EncodeCollabObservation(&v)
	require.NotEmpty(t, raw)
	require.LessOrEqual(t, len(raw), CollabObservationMaxBytes)
	var got CollabObservation
	require.NoError(t, json.Unmarshal(raw, &got))
	require.True(t, got.Truncated)
	require.Equal(t, 100, got.SendTotal)
	require.Equal(t, 100, len(got.Sends)+got.SendOverflow)
	require.Equal(t, 100, len(got.Deliveries)+got.DeliveriesOverflow)
	require.Less(t, len(got.Deliveries), 64, "byte cap trims in addition to item caps")
	require.Equal(t, 73, got.Replacements.Task)
	require.Equal(t, 2, got.LocalCount)
	copied := CloneCollabObservation(&v)
	v.InputTransformations.Counts[CollabTransformationReasonReasoningRemoved] = 99
	v.Sends[0].MarkerReasons[0] = CollabMarkerReasonAnonymous
	zero = 9
	require.Equal(t, 4, copied.InputTransformations.Counts[CollabTransformationReasonReasoningRemoved])
	require.Equal(t, 0, *copied.Sends[0].Marker.Add)
	require.Equal(t, CollabMarkerReasonChangedArguments, copied.Sends[0].MarkerReasons[0])
	require.Nil(t, NormalizeCollabObservationJSON([]byte(strings.Repeat("x", CollabObservationMaxBytes+1))))
	require.Nil(t, NormalizeCollabObservationJSON([]byte("invalid")))
}

func TestCollabObservationUntrustedTextDoesNotPersist(t *testing.T) {
	raw := EncodeCollabObservation(&CollabObservation{Thread: "raw-identity", ClientVersion: strings.Repeat("x", 1<<20), InitialReason: CollabReason("upstream body"), Sends: []CollabSendObservation{{RawReason: CollabPlanReason("raw body")}}})
	require.NotContains(t, string(raw), "raw-")
	require.NotContains(t, string(raw), "raw body")
	require.NotContains(t, string(raw), "upstream body")
	require.Less(t, len(raw), 4096)
}

func TestCollabObservationTransformationsDeduplicateAndPreserveAbsence(t *testing.T) {
	a := NewCollabObservationAccumulator(CollabObservation{}, true)
	a.ObserveInputTransformations(1, nil)
	require.Nil(t, CloneCollabObservation(&a.value).InputTransformations)
	counts := &CollabInputTransformations{Counts: map[CollabTransformationReason]int{CollabTransformationReasonCompactionRemoved: 2, CollabTransformationReason("raw reason"): 9}}
	a.ObserveInputTransformations(1, counts)
	a.ObserveInputTransformations(1, counts)
	a.ObserveInputTransformations(2, counts)
	counts.Counts[CollabTransformationReasonCompactionRemoved] = 55
	got := a.Freeze()
	require.Equal(t, map[CollabTransformationReason]int{CollabTransformationReasonCompactionRemoved: 4}, got.InputTransformations.Counts)
	a = NewCollabObservationAccumulator(CollabObservation{}, true)
	a.ObserveInputTransformations(1, &CollabInputTransformations{})
	require.NotNil(t, a.Freeze().InputTransformations)
}

type unsafeCollabObserver struct{ NoopCollabObserver }

func (unsafeCollabObserver) Freeze() *CollabObservation {
	panic("must not call extension during billing")
}

func TestCollabObservationCannotInterfereWithBillingClaims(t *testing.T) {
	for _, kind := range []string{"normal", "panic_observer", "busy", "absent"} {
		t.Run(kind, func(t *testing.T) {
			st := NewRequestState()
			st.EnableGatewayRequest(time.Now())
			a := NewCollabObservationAccumulator(CollabObservation{Thread: observationDigest()}, true)
			a.SealSend(CollabSendObservation{Seq: 1, Result: CollabSendResultAccepted})
			observer := CollabObserver(a)
			switch kind {
			case "panic_observer":
				observer = unsafeCollabObserver{}
			case "absent":
				observer = NoopCollabObserver{}
			case "busy":
				a.mu.Lock()
				defer a.mu.Unlock()
			}
			require.True(t, st.SetCollabState(&CollabRequestState{Observer: observer}))
			st.NoteGatewaySend(1)
			st.CompleteGatewayForward(true, false)
			meta, write := st.ClaimGatewayRequest(true)
			require.False(t, write)
			require.Equal(t, RequestSuccess, meta.Result)
			if kind == "normal" {
				require.NotEmpty(t, meta.CollabObservation)
				before := append([]byte(nil), meta.CollabObservation...)
				ctx := WithGatewayRequestMetadata(context.Background(), meta)
				meta.CollabObservation[0] = 'x'
				require.Equal(t, before, []byte(GatewayRequestMetadataFromContext(ctx).CollabObservation))
				again, _ := st.ClaimGatewayRequest(false)
				require.Equal(t, before, []byte(again.CollabObservation))
			} else {
				require.Empty(t, meta.CollabObservation)
			}
			_, write = st.ClaimGatewayRequest(false)
			require.False(t, write)
		})
	}
	st := NewRequestState()
	st.EnableGatewayRequest(time.Now())
	st.SetCollabState(&CollabRequestState{Observer: NewCollabObservationAccumulator(CollabObservation{StopReason: CollabReasonCanceled}, true)})
	st.CompleteGatewayForward(false, true)
	meta, write := st.ClaimGatewayRequest(false)
	require.True(t, write)
	require.Equal(t, RequestCanceled, meta.Result)
	require.Contains(t, string(meta.CollabObservation), "\"send_total\":0")
	require.Nil(t, NewCollabObservationAccumulator(CollabObservation{}, false))
}

func TestCollabClientObservationUsesLeadingSemanticVersion(t *testing.T) {
	for _, tc := range []struct {
		ua      string
		client  CollabClient
		state   CollabVersionState
		version string
	}{
		{"", CollabClientUnknown, CollabVersionStateMissing, ""},
		{"codex_cli_rs/1.2.3-alpha.2 (ignored)", CollabClientCodexCli, CollabVersionStateParsed, "1.2.3-alpha.2"},
		{"codex_app/broken", CollabClientCodexApp, CollabVersionStateInvalid, ""},
		{"codex_vscode/1.2", CollabClientCodexVscode, CollabVersionStateInvalid, ""},
		{"prefix codex_cli_rs/1.2.3", CollabClientOther, CollabVersionStateInvalid, ""},
	} {
		t.Run(tc.ua, func(t *testing.T) {
			c, s, v := ParseCollabClient(tc.ua)
			require.Equal(t, tc.client, c)
			require.Equal(t, tc.state, s)
			require.Equal(t, tc.version, v)
		})
	}
	require.True(t, CollabClientVersionAtLeast("1.2.3-alpha.10", "1.2.3-alpha.2"))
	require.False(t, CollabClientVersionAtLeast("1.2.3-alpha.2", "1.2.3"))
}

func TestCollabDeliveryReconciliationPrecedence(t *testing.T) {
	now := time.Now()
	delivery := observationDigest()
	record := func(outcome CollabDeliveryOutcome, source CollabEventSource, at time.Time) CollabDeliveryRecord {
		return CollabDeliveryRecord{At: at, Observation: &CollabObservation{Deliveries: []CollabDeliveryObservation{{Delivery: delivery, Form: CollabFormPlaintext, Outcome: outcome}}, Events: []CollabEventObservation{{Delivery: delivery, Event: observationDigest(), Source: source}}}}
	}
	pending := record(CollabDeliveryOutcomeNoticePending, CollabEventSourceIssued, now)
	other := record(CollabDeliveryOutcomeFailed, CollabEventSourceSeenOther, now)
	parent := record(CollabDeliveryOutcomeFailed, CollabEventSourceSeenParentNotice, now)
	require.Equal(t, "notice_pending", ReconcileCollabDelivery(delivery, []CollabDeliveryRecord{pending, other}, now).Outcome)
	require.Equal(t, "notice_returned_parent", ReconcileCollabDelivery(delivery, []CollabDeliveryRecord{parent, pending}, now).Outcome)
	require.Equal(t, "silent_loss", ReconcileCollabDelivery(delivery, []CollabDeliveryRecord{pending}, now.Add(24*time.Hour)).Outcome)
	accepted := record(CollabDeliveryOutcomeAcceptedOriginal, "", now.Add(-time.Hour))
	placeholder := record(CollabDeliveryOutcomePlaceholder, "", now)
	for _, records := range [][]CollabDeliveryRecord{{pending, placeholder, accepted}, {accepted, pending, placeholder}} {
		got := ReconcileCollabDelivery(delivery, records, now)
		require.Equal(t, "delivered", got.Outcome)
		require.Equal(t, CollabFormPlaintext, got.Form)
		require.True(t, got.HistoricalLoss)
	}
	require.Equal(t, "notice_written_user", ReconcileCollabDelivery(delivery, []CollabDeliveryRecord{placeholder, record(CollabDeliveryOutcomeNoticeWrittenUser, "", now)}, now).Outcome)
	require.Equal(t, "interrupted", ReconcileCollabDelivery(delivery, []CollabDeliveryRecord{other}, now).Outcome)
}

func TestCollabIngressInventoryAndSampling(t *testing.T) {
	body := []byte("{\"tools\":[{\"type\":\"namespace\",\"name\":\"collaboration\"}],\"input\":[{\"type\":\"additional_tools\",\"tools\":[{\"type\":\"namespace\",\"name\":\"collaboration\"}]},{\"type\":\"agent_message\",\"content\":[{\"type\":\"encrypted_content\",\"encrypted_content\":\"synthetic\"}]},{\"type\":\"function_call_output\",\"encrypted_content\":\"synthetic\"}]}")
	before := append([]byte(nil), body...)
	got := InspectCollabIngress(body, CollabIngressValidationPassed)
	require.True(t, got.Observed)
	require.True(t, got.Declared)
	require.Equal(t, "both", got.Carrier)
	require.True(t, got.EncryptedAgent)
	require.True(t, got.EncryptedTool)
	require.True(t, got.Persist(false))
	require.Equal(t, before, body)
	empty := InspectCollabIngress([]byte("{}"), CollabIngressValidationPassed)
	require.False(t, empty.Persist(false))
	require.True(t, empty.Persist(true))
	require.False(t, InspectCollabIngress([]byte("{invalid"), CollabIngressValidationFailed).Observed)
	require.False(t, InspectCollabIngress([]byte(strings.Repeat("x", 1<<20+1)), CollabIngressValidationFailed).Observed)
	count := 0
	for i := uint64(0); i < 32; i++ {
		if ShouldLogCollabObservation(true, 0, empty, i) {
			count++
		}
		require.True(t, ShouldLogCollabObservation(false, 0, empty, i))
		require.True(t, ShouldLogCollabObservation(true, 1, empty, i))
		require.True(t, ShouldLogCollabObservation(true, 0, got, i))
	}
	require.Equal(t, 2, count)
}

func TestCollabObservationConcurrentSealing(t *testing.T) {
	a := NewCollabObservationAccumulator(CollabObservation{}, true)
	var wg sync.WaitGroup
	for i := 1; i <= 64; i++ {
		wg.Add(1)
		go func(seq CollabSendSequence) {
			defer wg.Done()
			for n := 0; n < 3; n++ {
				a.SealSend(CollabSendObservation{Seq: seq, Result: CollabSendResultAccepted})
				a.ObserveDelivery(CollabDeliveryObservation{Delivery: observationDigest(), Outcome: CollabDeliveryOutcomeAcceptedOriginal})
			}
		}(CollabSendSequence(i))
	}
	wg.Wait()
	frozen := a.Freeze()
	require.Equal(t, 64, frozen.SendTotal)
	require.Equal(t, 48, frozen.SendOverflow)
	require.Len(t, frozen.Deliveries, 1)
}

func TestCollabObservationImplementationPanicIsContained(t *testing.T) {
	// A zero-value accumulator has no aggregate counts. A missing constructor cannot
	// turn a successful send into a forwarding or billing panic.
	a := &CollabObservationAccumulator{}
	require.NotPanics(t, func() { require.False(t, a.SealSend(CollabSendObservation{Seq: 1})) })
	require.NotNil(t, a.Freeze())
}

func TestCollabIngressUnsupportedRoutesStillObserved(t *testing.T) {
	body := []byte("{\"type\":\"response.create\",\"response\":{\"input\":[{\"type\":\"function_call_output\",\"output\":[{\"type\":\"encrypted_content\"}]}]}}")
	require.True(t, InspectCollabIngress(body, CollabIngressValidationPassed).EncryptedTool)
	body = []byte("{\"tools\":[{\"type\":\"function\",\"function\":{\"name\":\"collaboration.send_message\"}}]}")
	require.True(t, InspectCollabIngress(body, CollabIngressValidationPassed).Declared)
}
