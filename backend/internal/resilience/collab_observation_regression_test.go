//go:build unit

package resilience

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollabObservationRejectsPostgresInvalidJSON(t *testing.T) {
	for name, raw := range map[string][]byte{
		"nul":       []byte("{\"v\":2,\"thread\":\"\\u0000\"}"),
		"utf8":      append([]byte("{\"v\":2,\"thread\":\""), append([]byte{0xff}, []byte("\"}")...)...),
		"surrogate": []byte("{\"v\":2,\"thread\":\"\\ud800\"}"),
		"deep":      []byte("{\"v\":2,\"unknown\":" + strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001) + "}"),
	} {
		t.Run(name, func(t *testing.T) { require.Nil(t, NormalizeCollabObservationJSON(raw)) })
	}
}

func TestCollabObservationFormattedReadback(t *testing.T) {
	v := CollabObservation{}
	for i := 0; i < 64; i++ {
		v.Deliveries = append(v.Deliveries, CollabDeliveryObservation{Delivery: observationDigest(), Item: observationDigest(), Digest: observationDigest(), Position: CollabPositionMessage, Form: CollabFormCiphertext, Kind: CollabMessageKindTask, Outcome: CollabDeliveryOutcomePlaceholder})
	}
	raw := EncodeCollabObservation(&v)
	for i := 0; i < CollabObservationMaxSends; i++ {
		v.Sends = append(v.Sends, CollabSendObservation{Seq: CollabSendSequence(i + 1), Account: observationDigest(), Action: CollabActionGenericFailover, Result: CollabSendResultAccepted, RawPlan: CollabPlanOutcomeApplied, RawReason: CollabPlanReasonNone, Plan: CollabPlanOutcomeApplied, PlanReason: CollabPlanReasonNone, Eligibility: CollabEligibilityEligible, RestoreError: CollabRestoreErrorNone})
		raw = EncodeCollabObservation(&v)
		if len(strings.NewReplacer(":", ": ", ",", ", ").Replace(string(raw))) > CollabObservationMaxBytes {
			break
		}
	}
	var formatted bytes.Buffer
	require.NoError(t, json.Indent(&formatted, raw, "", "    "))
	require.Greater(t, formatted.Len(), CollabObservationMaxBytes)
	require.JSONEq(t, string(raw), string(NormalizeCollabObservationJSON(formatted.Bytes())))
	// This fixture has no punctuation inside string values. PostgreSQL emits
	// spaces after separators, in addition to sorting keys.
	spaced := strings.NewReplacer(":", ": ", ",", ", ").Replace(string(raw))
	require.Greater(t, len(spaced), CollabObservationMaxBytes)
	require.JSONEq(t, string(raw), string(NormalizeCollabObservationJSON([]byte(spaced))))
	t.Logf("compact=%d bytes, JSONB spacing=%d bytes", len(raw), len(spaced))
}

func collabLargeIngress(size int, collab bool) []byte {
	prefix := "{\"input\":[{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\""
	suffix := "\"}]}]"
	if collab {
		suffix += ",\"tools\":[{\"type\":\"namespace\",\"name\":\"collaboration\",\"tools\":[{\"type\":\"function\",\"name\":\"send_message\"}]}]"
	}
	suffix += "}"
	return []byte(prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix)
}

func TestCollabIngressLargeBodies(t *testing.T) {
	for _, size := range []int{1 << 20, 10 << 20} {
		for _, collab := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%t", size, collab), func(t *testing.T) {
				body := collabLargeIngress(size, collab)
				got := InspectCollabIngress(body, CollabIngressValidationPassed)
				require.True(t, got.Observed)
				require.Equal(t, collab, got.Declared)
				require.Equal(t, collab, got.Persist(false))
			})
		}
	}
}

func BenchmarkCollabIngress(b *testing.B) {
	for _, size := range []int{1 << 20, 10 << 20} {
		for _, collab := range []bool{false, true} {
			b.Run(fmt.Sprintf("%dMiB/collab=%t", size>>20, collab), func(b *testing.B) {
				body := collabLargeIngress(size, collab)
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					InspectCollabIngress(body, CollabIngressValidationPassed)
				}
			})
		}
	}
}

func TestCollabObservationCountsSurviveEveryCap(t *testing.T) {
	a := NewCollabObservationAccumulator(CollabObservation{}, true)
	three, zero := 3, 0
	send := CollabSendObservation{Action: CollabActionGenericFailover, Result: CollabSendResultAccepted, RawPlan: CollabPlanOutcomeConflict, RawReason: CollabPlanReasonNamespaceUpdate, Plan: CollabPlanOutcomeApplied, PlanReason: CollabPlanReasonNone, Eligibility: CollabEligibilityEligible, RestoreError: CollabRestoreErrorNone, CallCount: &three, Marker: CollabMarkerCounts{UpstreamEmpty: &zero, DeliveredEmpty: &three, Add: &three}, MarkerReasons: []CollabMarkerReason{CollabMarkerReasonOpaqueMessage}}
	for i := 1; i <= 5000; i++ {
		send.Seq = CollabSendSequence(i)
		send.Generation = CollabGeneration(i)
		require.True(t, a.SealSend(send))
		require.False(t, a.SealSend(send))
		a.ObserveInputTransformations(CollabGeneration(i), &CollabInputTransformations{Counts: map[CollabTransformationReason]int{CollabTransformationReasonReasoningRemoved: 1}})
	}
	d := CollabDeliveryObservation{Item: observationDigest(), Digest: observationDigest(), Position: CollabPositionCallArgs, Form: CollabFormCiphertext, Kind: CollabMessageKindTask, Class: CollabItemClassProtected, Outcome: CollabDeliveryOutcomePlaceholder, Evidence: CollabEvidenceGradeVerified, Stage: CollabStageReplaceAll, Scope: CollabScopeWholeItem, Bytes: 128}
	for i := 0; i < 100; i++ {
		d.Delivery = CollabDigest(fmt.Sprintf("%064x", i+1))
		a.ObserveDelivery(d)
		a.ObserveDelivery(d)
	}
	for i := 0; i < 48; i++ {
		event := CollabEventObservation{Event: CollabDigest(fmt.Sprintf("%064x", i+1)), Delivery: observationDigest(), Source: CollabEventSourceSeenParentNotice}
		a.ObserveEvent(event)
		a.ObserveEvent(event)
	}
	for i := 0; i < 20; i++ {
		a.ObserveHint(CollabHintObservation{Kind: CollabHintKindIssuer, Cooldown: CollabCooldownNone, Attempt: CollabHintAttemptSelected, Selectable: true, IsCurrent: true})
	}
	frozen := a.Freeze()
	require.NotNil(t, frozen)
	require.True(t, frozen.Truncated)
	require.Equal(t, 5000, frozen.SendTotal)
	require.Equal(t, 5000, len(frozen.Sends)+frozen.SendOverflow)
	require.Equal(t, 100, frozen.DeliveriesTotal)
	require.Equal(t, 100, len(frozen.Deliveries)+frozen.DeliveriesOverflow)
	require.Less(t, len(frozen.Deliveries), CollabObservationMaxDeliveries, "byte trimming must also be exercised")
	require.Equal(t, 48, frozen.EventsTotal)
	require.Equal(t, 48, len(frozen.Events)+frozen.EventsOverflow)
	require.Equal(t, 20, frozen.HintsTotal)
	require.Equal(t, 20, len(frozen.Hints)+frozen.HintsOverflow)
	counts := frozen.Counts
	require.Equal(t, 5000, counts.Actions[CollabActionGenericFailover])
	require.Equal(t, 5000, counts.Results[CollabSendResultAccepted])
	require.Equal(t, 5000, counts.RawPlans[CollabPlanOutcomeConflict])
	require.Equal(t, 5000, counts.RawReasons[CollabPlanReasonNamespaceUpdate])
	require.Equal(t, 5000, counts.Plans[CollabPlanOutcomeApplied])
	require.Equal(t, 5000, counts.PlanReasons[CollabPlanReasonNone])
	require.Equal(t, 5000, counts.Eligibility[CollabEligibilityEligible])
	require.Equal(t, 5000, counts.RestoreErrors[CollabRestoreErrorNone])
	require.Equal(t, 5000, counts.MarkerReasons[CollabMarkerReasonOpaqueMessage])
	require.Equal(t, 15000, *counts.CallCount)
	require.Equal(t, 0, *counts.Marker.UpstreamEmpty)
	require.Equal(t, 15000, *counts.Marker.DeliveredEmpty)
	require.Equal(t, 15000, *counts.Marker.Add)
	require.Nil(t, counts.Marker.Keep)
	require.Equal(t, 100, counts.DeliveryPositions[CollabPositionCallArgs])
	require.Equal(t, 100, counts.DeliveryForms[CollabFormCiphertext])
	require.Equal(t, 100, counts.DeliveryKinds[CollabMessageKindTask])
	require.Equal(t, 100, counts.DeliveryClasses[CollabItemClassProtected])
	require.Equal(t, 100, counts.DeliveryOutcomes[CollabDeliveryOutcomePlaceholder])
	require.Equal(t, 100, counts.DeliveryEvidence[CollabEvidenceGradeVerified])
	require.Equal(t, 100, counts.DeliveryStages[CollabStageReplaceAll])
	require.Equal(t, 100, counts.DeliveryScopes[CollabScopeWholeItem])
	require.EqualValues(t, 12800, counts.DeliveryBytes)
	require.Equal(t, 48, counts.EventSources[CollabEventSourceSeenParentNotice])
	require.Equal(t, 20, counts.HintKinds[CollabHintKindIssuer])
	require.Equal(t, 20, counts.HintCooldowns[CollabCooldownNone])
	require.Equal(t, 20, counts.HintAttempts[CollabHintAttemptSelected])
	require.Equal(t, 20, counts.HintSelectable)
	require.Equal(t, 20, counts.HintIsCurrent)
	require.Equal(t, 5000, frozen.InputTransformations.Counts[CollabTransformationReasonReasoningRemoved])
	raw := EncodeCollabObservation(frozen)
	require.LessOrEqual(t, len(raw), CollabObservationMaxBytes)
	require.JSONEq(t, string(raw), string(NormalizeCollabObservationJSON(raw)))
	counts.Actions[CollabActionGenericFailover] = 9
	*counts.CallCount = 9
	*counts.Marker.Add = 9
	again := a.Freeze()
	require.Equal(t, 5000, again.Counts.Actions[CollabActionGenericFailover])
	require.Equal(t, 15000, *again.Counts.CallCount)
	require.Equal(t, 15000, *again.Counts.Marker.Add)
}

func TestCollabObservationTypedCountsBeforeTrimming(t *testing.T) {
	v := CollabObservation{}
	n := 1
	for i := 0; i < 100; i++ {
		v.Sends = append(v.Sends, CollabSendObservation{Seq: CollabSendSequence(i + 1), Result: CollabSendResultCanceled, CallCount: &n})
		v.Deliveries = append(v.Deliveries, CollabDeliveryObservation{Delivery: observationDigest(), Outcome: CollabDeliveryOutcomeFailed})
		v.Events = append(v.Events, CollabEventObservation{Source: CollabEventSourceIssued})
		v.Hints = append(v.Hints, CollabHintObservation{Kind: CollabHintKindConsumer})
	}
	raw := EncodeCollabObservation(&v)
	var got CollabObservation
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, 100, got.Counts.Results[CollabSendResultCanceled])
	require.Equal(t, 100, *got.Counts.CallCount)
	require.Equal(t, 100, got.Counts.DeliveryOutcomes[CollabDeliveryOutcomeFailed])
	require.Equal(t, 100, got.Counts.EventSources[CollabEventSourceIssued])
	require.Equal(t, 100, got.Counts.HintKinds[CollabHintKindConsumer])
	require.Equal(t, 100, got.EventsTotal)
	require.Equal(t, 100, len(got.Events)+got.EventsOverflow)
	require.Equal(t, 100, got.HintsTotal)
	require.Equal(t, 100, len(got.Hints)+got.HintsOverflow)
}

func TestCollabObservationSummaryAloneFits(t *testing.T) {
	counts := &CollabObservationCounts{}
	fields := reflect.ValueOf(counts).Elem()
	for i := 0; i < fields.NumField(); i++ {
		f := fields.Field(i)
		if f.Kind() != reflect.Map {
			continue
		}
		f.Set(reflect.MakeMap(f.Type()))
		for _, name := range collabObservationEnumValues[f.Type().Key()] {
			key := reflect.New(f.Type().Key()).Elem()
			key.SetString(name)
			f.SetMapIndex(key, reflect.ValueOf(int(^uint(0)>>1)))
		}
	}
	raw := EncodeCollabObservation(&CollabObservation{Counts: counts})
	require.NotEmpty(t, raw)
	require.LessOrEqual(t, len(raw), CollabObservationMaxBytes)
	var got CollabObservation
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, counts, got.Counts)
}

func TestCollabObservationInvalidCountKeysNeverPersist(t *testing.T) {
	counts := &CollabObservationCounts{EventSources: map[CollabEventSource]int{CollabEventSourceSeenOther: 7, CollabEventSource("untrusted body"): 4}}
	raw := EncodeCollabObservation(&CollabObservation{Counts: counts})
	require.NotContains(t, string(raw), "untrusted body")
	var got CollabObservation
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, map[CollabEventSource]int{CollabEventSourceSeenOther: 7}, got.Counts.EventSources)
	require.Contains(t, string(raw), "\"call_count\":null")
}

func TestCollabObservationUnicodeAndReadBounds(t *testing.T) {
	for _, raw := range []string{
		"{\"v\":2,\"extra\":\"\\\\u0000\"}",
		"{\"v\":2,\"extra\":\"\\ud83d\\ude80\"}",
		"{\"v\":2,\"extra\":\"synthetic é\"}",
	} {
		require.NotNil(t, NormalizeCollabObservationJSON([]byte(raw)))
	}
	for _, raw := range []string{
		"{\"v\":2,\"extra\":\"\\udc00\"}",
		"{\"v\":2,\"extra\":\"\\ud800\\u0061\"}",
		"{\"v\":2,\"extra\":\"\\ud800\\ud800\"}",
		"{\"v\":2,\"extra\":" + strings.Repeat("[", 33) + "0" + strings.Repeat("]", 33) + "}",
	} {
		require.Nil(t, NormalizeCollabObservationJSON([]byte(raw)))
	}
	raw := []byte("{\"v\":2}" + strings.Repeat(" ", 4*CollabObservationMaxBytes))
	require.Nil(t, NormalizeCollabObservationJSON(raw), "formatting allowance remains bounded")
	legacy := NormalizeCollabObservationJSON([]byte("{\"v\":2,\"truncated\":true,\"send_total\":20,\"send_overflow\":20}"))
	var got CollabObservation
	require.NoError(t, json.Unmarshal(legacy, &got))
	require.Nil(t, got.Counts, "missing historical aggregates must not become observed zero")
}
