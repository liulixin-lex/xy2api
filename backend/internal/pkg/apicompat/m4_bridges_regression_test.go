//go:build unit

package apicompat

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// These fixed snapshots were captured at 1a0513330 from the unchanged R3
// fixtures: 21 paired cases, 5 unpaired cases and the existing encoding error.
// Source JSON and both legacy projections are compared byte for byte.
func TestM4ToolHistoryBaseline(t *testing.T) {
	var fixtures []struct {
		Name           string
		Messages       []ChatMessage
		Input          json.RawMessage
		SourceError    string
		Chat           json.RawMessage
		ChatError      string
		Anthropic      json.RawMessage
		AnthropicError string
	}
	raw, err := os.ReadFile("testdata/m4-history-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 27 {
		t.Fatalf("expected 27 fixed cases, got %d", len(fixtures))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			request, err := ChatCompletionsToResponses(&ChatCompletionsRequest{Model: "probe", Messages: fixture.Messages})
			m4CheckError(t, err, fixture.SourceError)
			if err != nil {
				return
			}
			m4CheckJSONBytes(t, request.Input, fixture.Input)
			chat, err := ResponsesToChatCompletionsRequest(request)
			m4CheckError(t, err, fixture.ChatError)
			if err == nil {
				actual, e := json.Marshal(chat)
				if e != nil {
					t.Fatal(e)
				}
				m4CheckJSONBytes(t, actual, fixture.Chat)
			}
			anthropic, err := ResponsesToAnthropicRequest(request)
			m4CheckError(t, err, fixture.AnthropicError)
			if err == nil {
				actual, e := json.Marshal(anthropic)
				if e != nil {
					t.Fatal(e)
				}
				m4CheckJSONBytes(t, actual, fixture.Anthropic)
			}
		})
	}
}

func m4CheckError(t *testing.T, err error, expected string) {
	t.Helper()
	actual := ""
	if err != nil {
		actual = err.Error()
	}
	if actual != expected {
		t.Fatalf("error=%q want=%q", actual, expected)
	}
}

func m4CheckJSONBytes(t *testing.T, actual, expected []byte) {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, expected); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, compact.Bytes()) {
		t.Fatalf("actual=%s\nexpected=%s", actual, compact.Bytes())
	}
}

func TestM4TypedChatOutputDecoder(t *testing.T) {
	cases := []struct {
		raw  string
		want []ChatOutputPart
		bad  bool
	}{
		{`"answer"`, []ChatOutputPart{{"text", "answer"}}, false},
		{`null`, nil, false},
		{`[{"type":"thinking","thinking":"THINK"},{"type":"text","text":"ANSWER"},{"type":"thinking","thinking":{"text":"NEXT"}}]`, []ChatOutputPart{{"thinking", "THINK"}, {"text", "ANSWER"}, {"thinking", "NEXT"}}, false},
		{`[{"type":"unknown","text":"x"}]`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := DecodeChatOutputContent(json.RawMessage(tc.raw))
			if (err != nil) != tc.bad || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, %v; want %#v, bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
	raw := []byte(`{"id":"chunk","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":"THINK"},{"type":"text","text":"ANSWER"}]}}]}`)
	original := string(raw)
	chunk, arrays, err := DecodeChatBridgeChunk(raw)
	if err != nil || chunk.ID != "chunk" || len(arrays) != 1 || string(raw) != original {
		t.Fatalf("bridge chunk: %#v %#v %v", chunk, arrays, err)
	}
	var legacy ChatCompletionsChunk
	if json.Unmarshal(raw, &legacy) == nil {
		t.Fatal("default ChatDelta unexpectedly accepted arrays")
	}
	for _, raw := range []string{`{"choices":[{"index":0,"finish_reason":"stop"}]}`, `{"choices":[{"index":0,"delta":null}]}`, `{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`} {
		if _, _, err := DecodeChatBridgeChunk([]byte(raw)); err != nil {
			t.Fatalf("ordinary chunk rejected: %s: %v", raw, err)
		}
	}
}

func TestM4ToolAliasAllocation(t *testing.T) {
	occupied := make(map[string]string)
	for _, name := range []string{"lookup", strings.Repeat("a", 64), strings.Repeat("a", 65), "工具"} {
		alias := bridgeToolAlias(name, name, 64, occupied)
		if !validBridgeToolName(alias, 64) || bridgeToolAlias(name, name, 64, occupied) != alias {
			t.Fatalf("unstable or illegal alias %q", alias)
		}
		if validBridgeToolName(name, 64) && alias != name {
			t.Fatalf("valid name changed: %q", alias)
		}
	}
	first := bridgeToolAlias("function/ns/x", "ns__x", 64, occupied)
	second := bridgeToolAlias("custom/ns/x", "ns__x", 64, occupied)
	if first == second {
		t.Fatal("kinds collided")
	}
	occupied[second] = "occupied by another original name"
	if bridgeToolAlias("custom/ns/x", "ns__x", 64, occupied) == second {
		t.Fatal("collision was not salted")
	}
}

func TestM4ToolDeclarationMergeCore(t *testing.T) {
	batches := [][]json.RawMessage{
		{json.RawMessage(`{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}},"strict":false}`), json.RawMessage(`{"type":"namespace","name":"n","tools":[{"type":"function","name":"a"}]}`)},
		{json.RawMessage(`{"type":"function","name":"lookup","parameters":{"properties":{},"type":"object"},"strict":false}`), json.RawMessage(`{"type":"namespace","name":"n","tools":[{"type":"function","name":"b"},{"type":"custom","name":"a"}]}`)},
		{json.RawMessage(`{"type":"function","name":"lookup","parameters":{"type":"string"},"strict":true}`), json.RawMessage(`{"type":"custom","name":"lookup","format":{"type":"text"}}`), json.RawMessage(`{"type":"function","name":"same"}`), json.RawMessage(`{"type":"function","name":"same"}`)},
	}
	var notices []string
	merged, err := mergeBridgeToolBatches(batches, func(field string) { notices = append(notices, field) })
	if err != nil || len(merged) != 5 {
		t.Fatalf("merged=%s err=%v", merged, err)
	}
	var tools []ResponsesTool
	raw, _ := json.Marshal(merged)
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools[1].Tools) != 3 || !bridgeSchemaEqual(tools[0].Parameters, json.RawMessage(`{"type":"object","properties":{}}`)) {
		t.Fatalf("lost first definition/namespace children: %s", raw)
	}
	if !reflect.DeepEqual(notices, []string{"input[1].tools[0].parameters", "input[1].tools[0].strict"}) {
		t.Fatalf("notices=%v", notices)
	}
}

func TestM4ChatToolSlotResolver(t *testing.T) {
	var slots chatToolSlots
	first, err := ResolveChatToolSlot(0, 0, nil, "a", &slots)
	if err != nil || first != 0 {
		t.Fatalf("first=%d %v", first, err)
	}
	second, err := ResolveChatToolSlot(0, 1, nil, "b", &slots)
	if err != nil || second != 1 {
		t.Fatalf("second=%d %v", second, err)
	}
	index := 2
	bound, err := ResolveChatToolSlot(0, 0, &index, "b", &slots)
	if err != nil || bound != second {
		t.Fatalf("late index=%d %v", bound, err)
	}
	if _, err := ResolveChatToolSlot(0, 0, nil, "", &slots); err == nil {
		t.Fatal("ambiguous position accepted")
	}
	if _, err := ResolveChatToolSlot(1, 0, nil, "", &slots); err == nil {
		t.Fatal("nonzero choice accepted")
	}
	index = -1
	if _, err := ResolveChatToolSlot(0, 0, &index, "", &slots); err == nil {
		t.Fatal("negative index accepted")
	}
}

func TestM4ToolDeclarationMerge(t *testing.T) {
	req := &ResponsesRequest{Model: "probe", Input: json.RawMessage(`[{"role":"user","content":"hi"},{"type":"additional_tools","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}},{"type":"function","name":"other"}]}]`), Tools: []ResponsesTool{{Type: "function", Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	var notices []ConversionNotice
	out, err := ResponsesToChatCompletionsRequestWithOptions(req, &ResponsesToChatOptions{Bridge: &BridgeOptions{Enabled: true}, Record: func(n ConversionNotice) { notices = append(notices, n) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Tools) != 2 || string(out.Tools[0].Function.Parameters) != `{"type":"object"}` {
		t.Fatalf("tools=%+v", out.Tools)
	}
	if !reflect.DeepEqual(notices, []ConversionNotice{{Field: "input[1].tools[0].parameters", Action: "omit"}}) {
		t.Fatalf("notices=%v", notices)
	}
	if _, err := ResponsesToChatCompletionsRequest(req); err == nil {
		t.Fatal("legacy duplicate rejection changed")
	}
}
