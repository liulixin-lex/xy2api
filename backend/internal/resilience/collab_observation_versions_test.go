//go:build unit

package resilience

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollabObservationSparseV3ExpandsAsV2(t *testing.T) {
	zero := 0
	v2 := EncodeCollabObservation(&CollabObservation{
		SendTotal: 4, SendOverflow: 2, Truncated: true,
		Sends:                []CollabSendObservation{{Seq: 1, CallCount: &zero, Marker: CollabMarkerCounts{Add: &zero}}, {Seq: 2}},
		Counts:               &CollabObservationCounts{CallCount: &zero, Marker: CollabMarkerCounts{Add: &zero}, DeliveryBytes: 19},
		Replacements:         CollabReplacementCounts{Task: 1},
		InputTransformations: &CollabInputTransformations{Counts: map[CollabTransformationReason]int{CollabTransformationReasonReasoningRemoved: 2}},
	})
	v3 := []byte(`{"v":3,"send_total":4,"send_overflow":2,"truncated":true,"sends":[{"seq":1,"call_count":0,"marker":{"add":0}},{"seq":2}],"counts":{"call_count":0,"marker":{"add":0},"delivery_bytes":19},"replacements":{"task":1},"input_transformations":{"counts":{"reasoning_removed":2}}}`)
	for _, raw := range [][]byte{v2, v3} {
		expanded := NormalizeCollabObservationJSON(raw)
		require.JSONEq(t, string(v2), string(expanded))
		require.Equal(t, expanded, NormalizeCollabObservationJSON(expanded), "expanded form is stable")
		var got CollabObservation
		require.NoError(t, json.Unmarshal(expanded, &got))
		require.Equal(t, 2, got.V, "writers and API must remain v2")
		require.Equal(t, &zero, got.Sends[0].CallCount)
		require.Nil(t, got.Sends[1].CallCount)
		require.Equal(t, &zero, got.Sends[0].Marker.Add)
		require.Nil(t, got.Sends[0].Marker.Veto)
		require.Equal(t, &zero, got.Counts.CallCount)
		require.Equal(t, expanded, EncodeCollabObservation(&got))
	}
}

func TestCollabObservationSparseV3PreservesUnavailableCounts(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		unknown bool
	}{
		{`{"v":3,"truncated":true}`, true},
		{`{"v":3,"truncated":true,"counts":null}`, true},
		{`{"v":3,"truncated":true,"counts":{}}`, false},
		{`{"v":3}`, false},
	} {
		var got CollabObservation
		require.NoError(t, json.Unmarshal(NormalizeCollabObservationJSON([]byte(tc.raw)), &got))
		require.Equal(t, tc.unknown, got.Counts == nil, tc.raw)
		if got.Counts != nil {
			require.Nil(t, got.Counts.CallCount, "omission must not invent an observed zero")
		}
	}
}

func TestCollabObservationSparseV3KeepsValidationAndBounds(t *testing.T) {
	for _, raw := range []string{
		`{"v":4}`, `{"v":3,"extra":"\u0000"}`, `{"v":3,"extra":"\ud800"}`,
		"{\"v\":3,\"extra\":\"" + string([]byte{0xff}) + "\"}",
		`{"v":3,"extra":` + strings.Repeat("[", 33) + `0` + strings.Repeat("]", 33) + `}`,
	} {
		require.Nil(t, NormalizeCollabObservationJSON([]byte(raw)))
	}
	raw := []byte(`{"v":3,"sends":[` + strings.Repeat(`{"seq":1},`, CollabObservationMaxSends) + `{"seq":2}]}`)
	expanded := NormalizeCollabObservationJSON(raw)
	require.LessOrEqual(t, len(expanded), CollabObservationMaxBytes)
	var got CollabObservation
	require.NoError(t, json.Unmarshal(expanded, &got))
	require.Len(t, got.Sends, CollabObservationMaxSends)
	require.Equal(t, 1, got.SendOverflow)
	require.Equal(t, CollabObservationMaxSends+1, got.SendTotal)
	require.True(t, got.Truncated)
}
