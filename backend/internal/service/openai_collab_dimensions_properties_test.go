//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"math/rand"
	"testing"
)

func collabPropertyIdentityChannels(t *testing.T) {
	rng := rand.New(rand.NewSource(211))
	for n := 0; n < collabPropertyRuns(); n++ {
		id := fmt.Sprintf("rs_%d", rng.Intn(19))
		other := id
		if n%2 == 0 {
			other += "_distinct"
		}
		raw := []byte(fmt.Sprintf(`{"input":[{"type":"reasoning","id":%q,"encrypted_content":"a"},{"type":"reasoning","id":%q,"encrypted_content":"b"}]}`, id, other))
		p := ParseCollabBody(raw)
		require.Equal(t, id == other, p.Shape.Ambiguous)
		values := []any{123, true, nil, []any{"input[0]"}, map[string]any{"at": "input[0]"}}
		payload, err := json.Marshal(map[string]any{"error": map[string]any{"code": "invalid_encrypted_content", "item_id": id, "message": values[n%len(values)]}})
		require.NoError(t, err)
		e := p.ResolveEvidence((CollabRejection{Payload: payload, Status: 400}).Evidence(p.Shape))
		require.NotNil(t, e.Named)
		require.False(t, e.Named.Located, "nonstring reference channel iteration %d", n)
	}
}

func collabPropertyDisposedEndpoints(t *testing.T) {
	modes := []string{"disposed", "other_selected_account", "invalid_latest_reference", "no_send", "different_account", "stale_body", "ordinary", "missing_item", "immutable", "plain", "continuation"}
	for n := 0; n < collabPropertyRuns(); n++ {
		mode := modes[n%len(modes)]
		raw := []byte(`{"input":[` + collabBatchTask + `]}`)
		if mode == "immutable" {
			raw = []byte(`{"input":[{"type":"function_call","id":"fc_a","arguments":"{}","encrypted_function_args":["opaque"]}]}`)
		}
		if mode == "plain" {
			raw = []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
		}
		if mode == "continuation" {
			raw, _ = sjson.SetBytes(raw, "previous_response_id", "resp_prior")
		}
		p := ParseCollabBody(raw)
		body, _ := p.Derive(raw, resilience.CollabActionReplaceAll, "", false)
		if mode == "missing_item" {
			body = []byte(`{"input":[]}`)
		}
		payload := `{"error":{"code":"invalid_encrypted_content"}}`
		if mode == "ordinary" {
			payload = `{"error":{"code":"server_error"}}`
		}
		if mode == "invalid_latest_reference" {
			payload = `{"error":{"code":"invalid_encrypted_content","item_id":"missing"}}`
		}
		r := collabObserveDerivedRejection(p, body, payload)
		r.AccountID = 1
		progress := &CollabLadderProgress{}
		progress.RememberRejection(r)
		current := int64(1)
		if mode == "other_selected_account" {
			current = 2
			progress.lastRejection.AccountID = 2
			progress.sourceBody = body
		}
		if mode == "no_send" {
			progress.lastRejection = nil
		}
		if mode == "different_account" {
			progress.lastRejection.AccountID = 2
		}
		if mode == "stale_body" {
			progress.lastRejection.Derivation = raw
		}
		evidence := resilience.CollabEvidence{Grade: resilience.CollabEvidenceGradeVerified}
		// Deliberately keep stale verified controller evidence for the ordinary case:
		// only the latest actual send may prove that the endpoint was reached.
		l := CollabLadder{Body: p, First: 1, Current: current, Progress: progress, Evidence: evidence, MaxAttempts: 1, Select: collabBatchSelect,
			Send: func(context.Context, int64, []byte, resilience.CollabAction) (resilience.CollabEvidence, bool) {
				return resilience.CollabEvidence{}, false
			}, Sent: func() bool { return false }}
		require.False(t, l.Run(context.Background()))
		want := CollabLadderFallback
		if mode == "disposed" || mode == "other_selected_account" {
			want = CollabLadderFinalDisposal
		}
		require.Equal(t, want, l.StopReason, "mode %s", mode)
		require.Equal(t, (mode == "disposed" || mode == "other_selected_account"), p.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding, "mode %s", mode)
		if mode == "continuation" {
			require.False(t, p.LadderPolicy(evidence, CollabLadderFinalDisposal).LocalEnding)
			require.False(t, p.LadderPolicy(evidence, CollabLadderDeliveryUnavailable).LocalEnding)
		}
	}
}

func collabPropertyRepairIdentityDimensions(t *testing.T) {
	rng := rand.New(rand.NewSource(223))
	for n := 0; n < collabPropertyRuns(); n++ {
		length := 12 + rng.Intn(9)
		values := make([]int, length)
		for i := range values {
			values[i] = i
		}
		first, second := 2, 10+rng.Intn(length-10)
		raw, _ := json.Marshal(map[string]any{"input": []any{json.RawMessage(collabBatchTask)}, "tools": values})
		p := ParseCollabBody(raw)
		a, _ := sjson.DeleteBytes(raw, fmt.Sprintf("tools.%d", first))
		b, _ := sjson.DeleteBytes(raw, fmt.Sprintf("tools.%d", second))
		repairs := p.independentRepairs(a)
		repairs.merge(p.independentRepairs(b))
		want := []int{}
		for i := range values {
			if i != first && i != second {
				want = append(want, i)
			}
		}
		expected, _ := json.Marshal(want)
		require.JSONEq(t, string(expected), gjson.GetBytes(repairs.apply(p, raw), "tools").Raw)
		// Anonymous nested-array deletion, with a unique retained sibling.
		raw = []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"remove"},{"type":"input_text","text":"keep"}]},` + collabBatchTask + `]}`)
		p = ParseCollabBody(raw)
		fixed, _ := sjson.DeleteBytes(raw, "input.0.content.0")
		require.JSONEq(t, string(fixed), string(p.independentRepairs(fixed).apply(p, raw)))
		// The existing setter is a string-to-object edit, followed by an optional
		// confirmed deletion. Expected values are assembled as ordinary JSON.
		query := fmt.Sprintf("query_%d", rng.Intn(100))
		arguments, _ := json.Marshal(map[string]any{"query": query})
		input := map[string]any{"type": "tool_search_call", "call_id": "call_search", "arguments": string(arguments), "status": "completed"}
		raw, _ = json.Marshal(map[string]any{"input": []any{input, json.RawMessage(collabBatchTask)}})
		p = ParseCollabBody(raw)
		fixed, _, changed, err := convertOpenAIResponsesToolSearchCallArguments(raw, 0)
		require.NoError(t, err)
		require.True(t, changed)
		input["arguments"] = map[string]any{"query": query}
		if n%2 == 0 {
			fixed, _, changed, err = removeOpenAIResponsesRejectedStatusAtIndex(fixed, 0)
			require.NoError(t, err)
			require.True(t, changed)
			delete(input, "status")
		}
		wantBody, _ := json.Marshal(map[string]any{"input": []any{input, json.RawMessage(collabBatchTask)}})
		require.JSONEq(t, string(wantBody), string(p.independentRepairs(fixed).apply(p, raw)))
		task, _ := sjson.Delete(collabBatchTask, "id")
		raw = []byte(`{"input":[` + task + `,` + task + `]}`)
		p = ParseCollabBody(raw)
		replaced := n % 2
		fixed, changed = p.Derive(raw, resilience.CollabActionNamedRepair, fmt.Sprintf("@input:%d", replaced), false)
		require.True(t, changed)
		parts := gjson.GetBytes(fixed, "input").Array()
		moved := []byte(`{"input":[` + parts[1].Raw + `,` + parts[0].Raw + `]}`)
		positions, ok := p.inputPositions(moved)
		require.True(t, ok)
		require.Equal(t, map[int]int{0: 1, 1: 0}, positions)

	}
}
