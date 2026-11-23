//go:build unit

package resilience

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
)

func TestCollabObservationTrackingIsBounded(t *testing.T) {
	a := NewCollabObservationAccumulator(CollabObservation{}, true)
	counts := &CollabInputTransformations{Counts: map[CollabTransformationReason]int{CollabTransformationReasonReasoningRemoved: 1}}
	for i := 1; i <= 20000; i++ {
		digest := CollabDigest(fmt.Sprintf("%064x", i))
		a.SealSend(CollabSendObservation{Seq: CollabSendSequence(i), Result: CollabSendResultAccepted})
		a.ObserveInputTransformations(CollabGeneration(i), counts)
		a.ObserveDelivery(CollabDeliveryObservation{Delivery: digest, Kind: CollabMessageKindTask, Outcome: CollabDeliveryOutcomePlaceholder})
		a.ObserveEvent(CollabEventObservation{Event: digest, Source: CollabEventSourceIssued})
	}
	fields := reflect.ValueOf(a).Elem()
	for _, name := range []string{"sends", "deliveries", "events", "transformations"} {
		t.Run(name, func(t *testing.T) {
			field := fields.FieldByName(name)
			if field.Kind() == reflect.Map {
				require.LessOrEqual(t, field.Len(), 256)
			} else {
				require.LessOrEqual(t, field.Type().Size(), uintptr(4096))
			}
		})
	}
	// Replays still inside tracking bounds remain duplicates. Old sequence
	// numbers and identities we could not retain are explicitly unverifiable.
	require.False(t, a.SealSend(CollabSendObservation{Seq: 20000, Result: CollabSendResultAccepted}))
	require.False(t, a.SealSend(CollabSendObservation{Seq: 1, Result: CollabSendResultAccepted}))
	a.ObserveInputTransformations(20000, counts)
	a.ObserveInputTransformations(1, counts)
	a.ObserveDelivery(CollabDeliveryObservation{Delivery: CollabDigest(fmt.Sprintf("%064x", 1)), Kind: CollabMessageKindTask, Outcome: CollabDeliveryOutcomePlaceholder})
	a.ObserveEvent(CollabEventObservation{Event: CollabDigest(fmt.Sprintf("%064x", 1)), Source: CollabEventSourceIssued})
	snapshot := a.Freeze()
	require.NotNil(t, snapshot)
	require.Equal(t, 20000, snapshot.SendTotal)
	require.Equal(t, 20000, snapshot.Counts.Results[CollabSendResultAccepted])
	require.Equal(t, 20000, snapshot.InputTransformations.Counts[CollabTransformationReasonReasoningRemoved])
	require.True(t, snapshot.Truncated)
	require.Equal(t, CollabUntrackedCounts{Sends: 1, Deliveries: 20000 - 256, Events: 20000 - 256, Transformations: 1}, snapshot.Untracked)
	require.Equal(t, 256, snapshot.DeliveriesTotal)
	require.Equal(t, 256, snapshot.EventsTotal)
	require.Equal(t, 256, snapshot.Counts.DeliveryOutcomes[CollabDeliveryOutcomePlaceholder])
	require.Equal(t, 256, snapshot.Counts.EventSources[CollabEventSourceIssued])
	raw := EncodeCollabObservation(snapshot)
	require.LessOrEqual(t, len(raw), CollabObservationMaxBytes)
	var restored CollabObservation
	require.NoError(t, json.Unmarshal(NormalizeCollabObservationJSON(raw), &restored))
	require.Equal(t, snapshot.Untracked, restored.Untracked)
	require.Equal(t, snapshot.Counts, restored.Counts)
}

func collabPrefilterFixture(size int, kind string) []byte {
	role, padding := "user", "x"
	suffix := "}"
	switch kind {
	case "developer":
		role = "developer"
	case "escaped":
		role = "developer"
		padding = "synthetic\\ntext\\t"
	case "collaboration":
		suffix = ",\"tools\":[{\"type\":\"namespace\",\"name\":\"collaboration\"}]}"
	case "escaped_identifiers":
		suffix = ",\"tools\":[{\"type\":\"namespace\",\"name\":\"collabo\\u0072ation\"}]}"
	}
	prefix := "{\"input\":[{\"type\":\"message\",\"role\":\"" + role + "\",\"content\":\""
	suffix = "\"}]" + suffix
	count := (size - len(prefix) - len(suffix)) / len(padding)
	pad := strings.Repeat(padding, count)
	pad += strings.Repeat("x", size-len(prefix)-len(suffix)-len(pad))
	return []byte(prefix + pad + suffix)
}

func TestCollabIngressIrrelevantEscapedBodyDoesNotAllocate(t *testing.T) {
	for _, kind := range []string{"plain", "developer", "escaped"} {
		t.Run(kind, func(t *testing.T) {
			body := collabPrefilterFixture(10<<20, kind)
			require.True(t, json.Valid(body))
			var got CollabIngressObservation
			allocs := collabMinAllocsPerRun(3, func() { got = InspectCollabIngress(body, CollabIngressValidationPassed) })
			require.True(t, got.Observed)
			require.False(t, got.Persist(false))
			require.Zero(t, allocs, "irrelevant bytes must bypass parsing and unescaping")
		})
	}
}

func TestCollabIngressPrefilterPreservesPossibleMatches(t *testing.T) {
	fixtures := []struct {
		body  string
		check func(CollabIngressObservation) bool
	}{
		{"{\"tools\":[{\"type\":\"namespace\",\"name\":\"collabo\\u0072ation\"}]}", func(o CollabIngressObservation) bool { return o.Declared }},
		{"{\"\\u0074ools\":[{\"type\":\"namespace\",\"name\":\"\\u0063\\u006f\\u006c\\u006c\\u0061\\u0062\\u006f\\u0072\\u0061\\u0074\\u0069\\u006f\\u006e\"}]}", func(o CollabIngressObservation) bool { return o.Declared }},
		{"{\"input\":[{\"type\":\"agent_message\",\"content\":[{\"type\":\"encrypted_content\"}]}]}", func(o CollabIngressObservation) bool { return o.EncryptedAgent }},
		{"{\"input\":[{\"type\":\"agent_\\u006dessage\",\"content\":[{\"type\":\"encrypted_content\"}]}]}", func(o CollabIngressObservation) bool { return o.EncryptedAgent }},
		{"{\"input\":[{\"type\":\"function_call_output\",\"output\":[{\"type\":\"encrypted_content\"}]}]}", func(o CollabIngressObservation) bool { return o.EncryptedTool }},
		{"{\"tools\":[{\"type\":\"namespace\",\"name\":\"custom\",\"tools\":[{\"name\":\"spawn_agent\"}]}]}", func(o CollabIngressObservation) bool { return o.SuspectedCustomNamespace }},
		{"{\"tools\":[{\"type\":\"namespace\",\"name\":\"custom\",\"tools\":[{\"name\":\"send_message\"}]}]}", func(o CollabIngressObservation) bool { return o.SuspectedCustomNamespace }},
		{"{\"tools\":[{\"type\":\"namespace\",\"name\":\"custom\",\"tools\":[{\"name\":\"followup_task\"}]}]}", func(o CollabIngressObservation) bool { return o.SuspectedCustomNamespace }},
		{"{\"input\":[{\"role\":\"developer\",\"content\":\"namespace alias for collaboration\"}]}", func(o CollabIngressObservation) bool { return o.UnrecognizedCatalog }},
	}
	for i, fixture := range fixtures {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			got := InspectCollabIngress([]byte(fixture.body), CollabIngressValidationPassed)
			require.True(t, got.Observed)
			require.True(t, fixture.check(got))
		})
	}
}

func BenchmarkCollabIngressPrefilter(b *testing.B) {
	for _, kind := range []string{"plain", "developer", "escaped", "collaboration", "escaped_identifiers"} {
		b.Run(kind, func(b *testing.B) {
			body := collabPrefilterFixture(10<<20, kind)
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				InspectCollabIngress(body, CollabIngressValidationPassed)
			}
		})
	}
}

func TestCollabSequenceWindowBoundsAndReordering(t *testing.T) {
	var tracker collabSequenceWindow
	for _, step := range []struct {
		sequence       uint64
		fresh, tracked bool
	}{
		{0, false, false}, {1, true, true}, {3, true, true}, {2, true, true}, {3, false, true},
		{256, true, true}, {1, false, true}, {257, true, true}, {1, false, false}, {2, false, true},
		{^uint64(0), true, true}, {^uint64(0), false, true}, {^uint64(0) - 255, true, true}, {^uint64(0) - 256, false, false},
	} {
		fresh, tracked := tracker.remember(step.sequence)
		require.Equal(t, step.fresh, fresh, "sequence %d", step.sequence)
		require.Equal(t, step.tracked, tracked, "sequence %d", step.sequence)
	}
}

func TestCollabObservationTrackingDetachesIdentifiers(t *testing.T) {
	backing := strings.Repeat("a", 1<<20)
	digest := CollabDigest(backing[:64])
	kindBacking := strings.Repeat("task", 1<<18)
	kind := CollabMessageKind(kindBacking[:4])
	a := NewCollabObservationAccumulator(CollabObservation{}, true)
	a.ObserveDelivery(CollabDeliveryObservation{Delivery: digest, Kind: kind})
	a.ObserveEvent(CollabEventObservation{Event: digest})
	for delivery := range a.deliveries {
		require.Equal(t, digest, delivery.Delivery)
		require.False(t, unsafe.StringData(backing) == unsafe.StringData(string(delivery.Delivery)))
		require.False(t, unsafe.StringData(kindBacking) == unsafe.StringData(string(delivery.Kind)))
	}
	for event := range a.events {
		require.False(t, unsafe.StringData(backing) == unsafe.StringData(string(event.Event)))
	}
}
