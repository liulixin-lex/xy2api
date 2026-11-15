package apicompat

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeResponsesPreservesSmallLimit(t *testing.T) {
	for _, tc := range []struct{ input, want int }{{1, 16}, {15, 16}, {16, 16}, {127, 127}, {128, 128}} {
		t.Run(fmt.Sprint(tc.input), func(t *testing.T) {
			limit := tc.input
			request := ResponsesRequest{Model: "gpt-4o", MaxOutputTokens: &limit}
			data, err := json.Marshal(request)
			require.NoError(t, err)
			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &wire))
			t.Logf("max_output_tokens input=%d wire=%s", tc.input, wire["max_output_tokens"])
			require.JSONEq(t, fmt.Sprint(tc.want), string(wire["max_output_tokens"]))
			require.Equal(t, tc.input, *request.MaxOutputTokens)
		})
	}
}

func TestPrepareNativeResponsesStopsOmitted(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		count   int
		invalid bool
	}{
		{`"END"`, 1, false}, {`["END","STOP"]`, 1, false}, {`null`, 0, false}, {`[]`, 0, false},
		{`""`, 0, true}, {`["END",2]`, 0, true}, {`[null]`, 0, true}, {`42`, 0, true}, {`{}`, 0, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			req := ResponsesRequest{Model: "gpt-4o", Input: json.RawMessage(`[]`), bridgeStop: json.RawMessage(tc.raw)}
			var notices []ConversionNotice
			out, err := PrepareNativeResponsesRequest(req, ConversionOptions{Record: func(n ConversionNotice) { notices = append(notices, n) }})
			require.Equal(t, tc.raw, string(req.bridgeStop))
			if tc.invalid {
				requireUnsupportedConversion(t, err, "stop")
				require.Empty(t, notices)
				return
			}
			require.NoError(t, err)
			require.Len(t, notices, tc.count)
			if tc.count > 0 {
				require.Equal(t, ConversionNotice{Field: "stop", Action: "omit"}, notices[0])
				middle, err := ResponsesToAnthropicRequest(&req)
				require.NoError(t, err)
				require.NotEmpty(t, middle.StopSeqs)
				_, err = json.Marshal(req)
				requireUnsupportedConversion(t, err, "stop")
			}
			wire, err := json.Marshal(out)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(wire, &fields))
			require.NotContains(t, fields, "stop")
		})
	}
	for _, limit := range []int{1, 15, 16, 127, 128} {
		req := ResponsesRequest{MaxOutputTokens: &limit}
		var notices []ConversionNotice
		opts := ConversionOptions{Record: func(n ConversionNotice) { notices = append(notices, n) }}
		out, err := PrepareNativeResponsesRequest(req, opts)
		require.NoError(t, err)
		require.Equal(t, max(limit, 16), *out.MaxOutputTokens)
		if limit < 16 {
			require.Equal(t, []ConversionNotice{{Field: "max_output_tokens", Action: "map"}}, notices)
		} else {
			require.Empty(t, notices)
		}
		before := len(notices)
		_, err = PrepareNativeResponsesRequest(out, opts)
		require.NoError(t, err)
		require.Len(t, notices, before)
	}
}
