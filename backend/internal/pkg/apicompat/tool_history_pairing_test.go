//go:build unit

package apicompat

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFoundationToolHistoryPairing(t *testing.T) {
	items := []json.RawMessage{[]byte(`{"type":"function_call","call_id":"same"}`), []byte(`{"type":"function_call_output","call_id":"same","output":"first"}`), []byte(`{"role":"user","content":"next"}`), []byte(`{"type":"function_call","call_id":"same"}`), []byte(`{"type":"function_call_output","call_id":"same","output":"second"}`), []byte(`{"type":"function_call_output","call_id":"orphan"}`), []byte(`{"type":"function_call","call_id":"missing"}`)}
	before, _ := json.Marshal(items)
	for _, continuation := range []bool{false, true} {
		pairs, err := PairToolHistory(items, continuation)
		if err != nil || len(pairs) != 2 || pairs[0].ResultIndex != 1 || pairs[1].CallIndex != 3 || pairs[1].ResultIndex != 4 || pairs[1].Round != 1 {
			t.Fatal(pairs, err)
		}
	}
	after, _ := json.Marshal(items)
	if string(before) != string(after) {
		t.Fatal("mutated input")
	}
	duplicate := []json.RawMessage{items[0], items[0], items[1], items[1]}
	p, err := PairToolHistory(duplicate, false)
	if err != nil || len(p) != 2 || p[0].ResultIndex != 2 || p[1].ResultIndex != 3 || p[1].Slot != 1 || p[1].Round != 0 {
		t.Fatal(p, err)
	}
	legacy := []json.RawMessage{[]byte(`{"type":"function_call","name":"f"}`), []byte(`{"type":"function_call_output","name":"f"}`)}
	p, err = PairToolHistory(legacy, false)
	if err != nil || len(p) != 1 {
		t.Fatal(p, err)
	}
}

func TestFoundationToolHistoryInterleavedAndLegacyFIFO(t *testing.T) {
	interleaved := []json.RawMessage{
		[]byte(`{"type":"function_call","call_id":"A"}`), []byte(`{"type":"function_call","call_id":"B"}`),
		[]byte(`{"type":"function_call_output","call_id":"A","output":"a"}`), []byte(`{"type":"function_call","call_id":"C"}`),
		[]byte(`{"type":"function_call_output","call_id":"B","output":"b"}`), []byte(`{"type":"function_call_output","call_id":"C","output":"c"}`),
	}
	pairs, err := PairToolHistory(interleaved, false)
	want := []ToolHistoryPair{{Round: 0, CallIndex: 0, ResultIndex: 2, Slot: 0, Kind: "function", CallID: "A"}, {Round: 0, CallIndex: 1, ResultIndex: 4, Slot: 1, Kind: "function", CallID: "B"}, {Round: 0, CallIndex: 3, ResultIndex: 5, Slot: 2, Kind: "function", CallID: "C"}}
	if err != nil || !reflect.DeepEqual(pairs, want) {
		t.Fatal(pairs, err)
	}
	legacy := ChatMessage{Role: "assistant", FunctionCall: &ChatFunctionCall{Name: "lookup", Arguments: `{}`}}
	converted, err := ChatCompletionsToResponses(&ChatCompletionsRequest{Model: "probe", Messages: []ChatMessage{legacy, legacy, {Role: "function", Name: "lookup", Content: json.RawMessage(`"one"`)}, {Role: "function", Name: "lookup", Content: json.RawMessage(`"two"`)}}})
	if err != nil {
		t.Fatal(err)
	}
	var items []json.RawMessage
	if err = json.Unmarshal(converted.Input, &items); err != nil {
		t.Fatal(err)
	}
	pairs, err = PairToolHistory(items, false)
	want = []ToolHistoryPair{{Round: 0, CallIndex: 0, ResultIndex: 2, Slot: 0, Kind: "function", CallID: "call_legacy_1"}, {Round: 0, CallIndex: 1, ResultIndex: 3, Slot: 1, Kind: "function", CallID: "call_legacy_2"}}
	if err != nil || !reflect.DeepEqual(pairs, want) {
		t.Fatal(pairs, err)
	}
	for i, pair := range pairs {
		var item ResponsesInputItem
		if err = json.Unmarshal(items[pair.ResultIndex], &item); err != nil {
			t.Fatal(err)
		}
		if item.CallID != want[i].CallID || item.Output != []string{"one", "two"}[i] {
			t.Fatal(item)
		}
	}
}
