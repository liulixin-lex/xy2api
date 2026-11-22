package apicompat

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConversionIdentityDiagnostic(t *testing.T) {
	left := ResponsesOutput{ID: "private_item_a", CallID: "private_call_a", Type: "function_call"}
	for _, tc := range []struct {
		name  string
		right ResponsesOutput
	}{
		{"item_id_call_id", ResponsesOutput{ID: left.ID, CallID: "private_call_b", Type: left.Type}},
		{"item_id_type", ResponsesOutput{ID: left.ID, Type: "message"}},
		{"call_id_item_id", ResponsesOutput{ID: "private_item_b", CallID: left.CallID, Type: left.Type}},
		{"call_id_type", ResponsesOutput{CallID: left.CallID, Type: "message"}},
		{"output_index_item_id", ResponsesOutput{ID: "private_item_b", Type: left.Type}},
		{"output_index_call_id", ResponsesOutput{CallID: "private_call_b", Type: left.Type}},
		{"output_index_type", ResponsesOutput{Type: "message"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc := NewBufferedResponseAccumulator()
			acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndexPresent: true, Item: &left})
			acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndexPresent: true, Item: &tc.right})
			var conversion *UpstreamConversionError
			require.ErrorAs(t, acc.Err(), &conversion)
			require.Equal(t, "conflicting output identity", conversion.Reason)
			d := conversion.Diagnostic()
			require.Equal(t, tc.name, d.IdentityConflict)
			require.Equal(t, "responses", d.SourceProtocol)
			require.Equal(t, "output", d.Field)
			require.Equal(t, "conflicting_output_identity", d.Reason)
			require.NotContains(t, fmt.Sprintf("%+v", d), "private_")
			latched := acc.Err()
			acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndexPresent: true, Item: &left})
			require.Same(t, latched, acc.Err())
		})
	}
}

func TestConversionFourRejectionsRemainDistinct(t *testing.T) {
	for _, reason := range []string{"ambiguous output identity", "conflicting output identity", "conflicting output index", "ambiguous unindexed output"} {
		t.Run(reason, func(t *testing.T) {
			acc := NewBufferedResponseAccumulator()
			left := ResponsesOutput{ID: "item_a", CallID: "call_a", Type: "function_call"}
			right := ResponsesOutput{ID: "item_b", CallID: "call_b", Type: "function_call"}
			if reason == "ambiguous unindexed output" {
				acc.slot(left, 0, false, false).item = left
				acc.slot(right, 0, false, false).item = right
				require.Nil(t, acc.slot(ResponsesOutput{Type: "function_call"}, 0, false, false))
			} else {
				acc.slot(left, 0, true, false).item = left
				switch reason {
				case "ambiguous output identity":
					acc.slot(right, 1, true, false).item = right
					require.Nil(t, acc.slot(ResponsesOutput{ID: left.ID, CallID: right.CallID}, 0, true, false))
				case "conflicting output identity":
					require.Nil(t, acc.slot(right, 0, true, false))
				case "conflicting output index":
					require.Nil(t, acc.slot(left, 1, true, false))
				}
			}
			var conversion *UpstreamConversionError
			require.ErrorAs(t, acc.Err(), &conversion)
			require.Equal(t, reason, conversion.Reason)
			require.NotEqual(t, "invalid_response", conversion.Diagnostic().Reason)
			require.NotEmpty(t, conversion.Diagnostic().IdentityConflict)
			require.NotEqual(t, "unspecified", conversion.Diagnostic().IdentityConflict)
		})
	}
}

func TestConversionDiagnosticDoesNotExposeGeneratedFields(t *testing.T) {
	err := &UpstreamConversionError{SourceProtocol: "private_protocol", Field: "private_field", Reason: "private_reason", SourceReason: "private_source_reason", IdentityConflict: "private_identity"}
	d := err.Diagnostic()
	require.Equal(t, "unknown", d.SourceProtocol)
	require.Equal(t, "other", d.Field)
	require.Equal(t, "invalid_response", d.Reason)
	require.Empty(t, d.IdentityConflict)
	require.Empty(t, d.SourceReason)
	require.NotContains(t, fmt.Sprintf("%+v", d), "private_")
	for _, field := range []string{"output[123].arguments", "output[456].status"} {
		err.Field = field
		d = err.Diagnostic()
		require.Contains(t, []string{"output_arguments", "output_status"}, d.Field)
	}
}
