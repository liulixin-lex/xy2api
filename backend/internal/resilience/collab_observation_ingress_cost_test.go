//go:build unit

package resilience

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func collabRealisticBody(size int, kind string) []byte {
	base := "plain"
	if kind == "escaped_developer_history" || kind == "escaped_developer_controls" {
		base = "escaped"
	}
	body := collabPrefilterFixture(size, base)
	if kind == "escaped_developer_controls" {
		body = bytes.Replace(body, []byte("synthetic"), []byte("\\u001b\\u003c"), 1)
	}
	if kind == "plain" {
		return body
	}
	if kind == "user_escape" {
		return bytes.Replace(body, []byte("\"content\":\""), []byte("\"content\":\"\\u001b\\u003c"), 1)
	}
	// Put ordinary tool and reasoning history before large text, as in Codex.
	return bytes.Replace(body, []byte("\"input\":["), []byte("\"input\":[{\"type\":\"function_call_output\",\"output\":\"synthetic result\"},{\"type\":\"reasoning\",\"encrypted_content\":\"synthetic cipher\"},"), 1)
}

func TestCollabIngressRealisticBodiesDoNotAllocate(t *testing.T) {
	for _, kind := range []string{"plain", "user_history", "user_escape", "escaped_developer_history", "escaped_developer_controls"} {
		t.Run(kind, func(t *testing.T) {
			body := collabRealisticBody(10<<20, kind)
			require.True(t, json.Valid(body))
			var got CollabIngressObservation
			allocations := collabMinAllocsPerRun(3, func() { got = InspectCollabIngress(body, CollabIngressValidationPassed) })
			require.True(t, got.Observed)
			require.False(t, got.Persist(false))
			require.Zero(t, allocations)
		})
	}
}

func TestCollabIngressMalformedDoesNotBecomeEvidence(t *testing.T) {
	for i, raw := range []string{
		"{\"tools\":[{\"type\":\"namespace\",\"name\":\"collaboration\"}],}",
		"{\"input\":[{\"type\":\"agent_message\",\"content\":[{\"type\":\"encrypted_content\"}]}],}",
		"{\"input\":[{\"type\":\"function_call_output\",\"output\":[{\"type\":\"encrypted_content\"}]}],}",
		"{\"tools\": [}",
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			require.False(t, json.Valid([]byte(raw)))
			for _, validation := range []CollabIngressValidation{CollabIngressValidationUnknown, CollabIngressValidationFailed} {
				require.Equal(t, CollabIngressObservation{}, InspectCollabIngress([]byte(raw), validation))
			}
		})
	}
}

func TestCollabObservationZeroNumbersAreUntracked(t *testing.T) {
	a := NewCollabObservationAccumulator(CollabObservation{}, true)
	require.False(t, a.SealSend(CollabSendObservation{Seq: 0, Result: CollabSendResultAccepted}))
	a.ObserveInputTransformations(0, &CollabInputTransformations{Counts: map[CollabTransformationReason]int{CollabTransformationReasonReasoningRemoved: 1}})
	require.False(t, a.SealSend(CollabSendObservation{Seq: 0, Result: CollabSendResultNotSent}))
	a.ObserveInputTransformations(0, &CollabInputTransformations{})
	a.ObserveInputTransformations(0, nil)
	got := a.Freeze()
	require.Equal(t, CollabUntrackedCounts{Sends: 2, Transformations: 3}, got.Untracked)
	require.Zero(t, got.SendTotal)
	require.Nil(t, got.InputTransformations)
}

func BenchmarkCollabIngressRealistic(b *testing.B) {
	for _, kind := range []string{"plain", "user_history", "user_escape", "escaped_developer_history", "escaped_developer_controls"} {
		b.Run(kind, func(b *testing.B) {
			body := collabRealisticBody(10<<20, kind)
			require.True(b, json.Valid(body))
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				InspectCollabIngress(body, CollabIngressValidationPassed)
			}
		})
	}
}

// Vary irrelevant text, identifier escaping, field order and all observed shapes.
// The reference parses every field and unescapes every value, without a filter.
func TestCollabIngressGeneratedDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	fixtures := []string{
		"{}",
		"{\"tools\":[{\"type\":\"namespace\",\"name\":\"collaboration\"}]}",
		"{\"tools\":[{\"type\":\"namespace\",\"name\":\"custom\",\"tools\":[{\"name\":\"spawn_agent\"},{\"name\":\"send_message\"},{\"name\":\"followup_task\"}]}]}",
		"{\"tools\":[{\"name\":\"collaboration.send_message\"},{\"function\":{\"name\":\"collaboration.followup_task\"}}]}",
		"{\"tools\":[{\"name\":null,\"function\":{\"name\":\"collaboration.send_message\"}}]}",
		"{\"input\":[{\"type\":\"additional_tools\",\"tools\":[{\"name\":\"spawn_agent\",\"namespace\":\"collaboration\"}]}]}",
		"{\"input\":[{\"type\":\"agent_message\",\"content\":[{\"type\":\"encrypted_content\"}]}]}",
		"{\"input\":[{\"type\":\"function_call_output\",\"output\":[{\"encrypted_content\":\"cipher\"}]}]}",
		"{\"input\":[{\"type\":\"tool_output\",\"encrypted_content\":\"cipher\"}]}",
		"{\"input\":[{\"role\":\"developer\",\"content\":\"namespace alias for collaboration\"}]}",
		"{\"input\":[{\"role\":\"developer\",\"content\":[{\"text\":\"namespace collaboration\"},{\"text\":\"collaboration alias namespace\"}]}]}",
		"{\"type\":\"response.create\",\"response\":{\"tools\":[{\"name\":\"collaboration.send_message\"}]}}",
	}
	for i := 0; i < 500; i++ {
		raw := []byte(fixtures[i%len(fixtures)])
		body := collabEscapeJSONLetters(raw, rng, 4)
		require.True(t, json.Valid(body))
		require.Equal(t, collabIngressUnfilteredReference(body), InspectCollabIngress(body, CollabIngressValidationPassed), "fixture %d", i)
	}
}

func TestCollabIngressUnknownValidationHasNoEvidence(t *testing.T) {
	body := []byte("{\"tools\":[{\"type\":\"namespace\",\"name\":\"collaboration\"}]}")
	for _, validation := range []CollabIngressValidation{CollabIngressValidationUnknown, CollabIngressValidationFailed, 255} {
		require.Equal(t, CollabIngressObservation{}, InspectCollabIngress(body, validation))
	}
	require.True(t, InspectCollabIngress(body, CollabIngressValidationPassed).Declared)
}

func TestCollabIngressCatalogRawMatchingDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	parts := []string{"namespace", "collaboration", "namespace collaboration", "collabo", "ration", "name", "space", " alias ", "\n", "\t", "\"", "\\", "\x1b", "<", "é", "🚀"}
	for i := 0; i < 1000; i++ {
		var text strings.Builder
		for j := 0; j < 10; j++ {
			text.WriteString(parts[rng.Intn(len(parts))])
		}
		body, err := json.Marshal(map[string]any{"input": []any{map[string]any{"role": "developer", "content": []any{map[string]any{"text": text.String()}}}}})
		require.NoError(t, err)
		raw := collabEscapeJSONLetters(body, rng, 8)
		require.True(t, json.Valid(raw))
		require.Equal(t, collabIngressUnfilteredReference(raw), InspectCollabIngress(raw, CollabIngressValidationPassed), "fixture %d", i)
	}
}

func TestCollabIngressMalformedVariants(t *testing.T) {
	bases := []string{
		"{\"tools\":[{\"type\":\"namespace\",\"name\":\"collaboration\"}]}",
		"{\"input\":[{\"type\":\"agent_message\",\"content\":[{\"type\":\"encrypted_content\"}]}]}",
		"{\"input\":[{\"type\":\"function_call_output\",\"output\":[{\"type\":\"encrypted_content\"}]}]}",
	}
	for _, base := range bases {
		for i := 0; i < len(base); i++ {
			// Inserting an unquoted invalid token generates failures at every location,
			// including valid-looking evidence before the syntax error.
			raw := []byte(base[:i] + "\x00" + base[i:])
			require.False(t, json.Valid(raw))
			require.Equal(t, CollabIngressObservation{}, InspectCollabIngress(raw, CollabIngressValidationFailed))
			require.Equal(t, CollabIngressObservation{}, InspectCollabIngress(raw, CollabIngressValidationUnknown))
		}
	}
}

func TestCollabIngressLongEscapedMetadataIsBounded(t *testing.T) {
	padding := strings.Repeat("synthetic\\ntext\\t", 1<<16)
	for _, field := range []string{"type", "role", "name", "namespace"} {
		body := []byte("{\"tools\":[{\"" + field + "\":\"" + padding + "\"}],\"input\":[{\"" + field + "\":\"" + padding + "\"}]}")
		require.True(t, json.Valid(body))
		require.Zero(t, collabMinAllocsPerRun(3, func() { InspectCollabIngress(body, CollabIngressValidationPassed) }))
	}
}

func TestCollabIngressCatalogLargePrefixDoesNotCopy(t *testing.T) {
	raw := []byte("{\"input\":[{\"role\":\"developer\",\"content\":\"" + strings.Repeat("c\\n", 1<<18) + "namespace alias collaboration\"}]}")
	require.True(t, json.Valid(raw))
	var got CollabIngressObservation
	require.Zero(t, collabMinAllocsPerRun(3, func() { got = InspectCollabIngress(raw, CollabIngressValidationPassed) }))
	require.True(t, got.UnrecognizedCatalog)
}

func collabEscapeJSONLetters(raw []byte, rng *rand.Rand, rate int) []byte {
	var out strings.Builder
	inString := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '"' {
			inString = !inString
			out.WriteByte(c)
			continue
		}
		if !inString {
			out.WriteByte(c)
			continue
		}
		if c == '\\' {
			end := i + 2
			if raw[i+1] == 'u' {
				end = i + 6
			}
			out.Write(raw[i:end])
			i = end - 1
		} else if c >= 'a' && c <= 'z' && rng.Intn(rate) == 0 {
			fmt.Fprintf(&out, "\\u%04x", c)
		} else {
			out.WriteByte(c)
		}
	}
	return []byte(out.String())
}
