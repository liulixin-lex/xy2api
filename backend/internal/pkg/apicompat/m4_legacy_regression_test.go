//go:build unit

package apicompat

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

func m4LegacyProjectionFixtures(t *testing.T) map[string]any {
	t.Helper()
	results := make(map[string]any)
	for name, body := range map[string]string{
		"namespace_custom": `{"tools":[{"type":"namespace","name":"mcp","tools":[{"type":"function","name":"f","parameters":{"type":"object"}},{"type":"custom","name":"c"}]}],"tool_choice":{"type":"function","namespace":"mcp","name":"f"},"input":[{"type":"function_call","call_id":"c","namespace":"mcp","name":"f","arguments":"{}"}]}`,
		"unicode":          `{"tools":[{"type":"function","name":"工具","parameters":{"type":"object"}}]}`,
		"collision":        `{"tools":[{"type":"function","name":"mcp__f"},{"type":"namespace","name":"mcp","tools":[{"type":"function","name":"f"}]}]}`,
		"custom":           `{"tools":[{"type":"custom","name":"exec"}],"input":[{"type":"custom_tool_call","call_id":"c","name":"exec","input":"echo ok"}]}`,
	} {
		var request map[string]any
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		mapping, changed, err := AdaptResponsesClientTools(request)
		errorText := ""
		if err != nil {
			errorText = err.Error()
		}
		var namespaces map[string]map[string]string
		if mapping.NamespaceTools != nil {
			namespaces = make(map[string]map[string]string)
			for alias, identity := range mapping.NamespaceTools {
				namespaces[alias] = map[string]string{"Namespace": identity.Namespace, "Name": identity.Name}
			}
		}
		results["adapter_"+name] = map[string]any{"request": request, "changed": changed, "error": errorText, "custom": mapping.CustomTools, "namespace": namespaces, "search": mapping.ToolSearch}
	}
	var chat ChatCompletionsResponse
	if err := json.Unmarshal([]byte(`{"id":"fixed","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"THINK","content":"ANSWER","tool_calls":[{"id":"call_a","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`), &chat); err != nil {
		t.Fatal(err)
	}
	results["chat_buffered_responses"] = ChatCompletionsResponseToResponses(&chat, "m", nil, nil, false, nil)
	results["chat_buffered_anthropic"] = ChatCompletionsResponseToAnthropic(&chat, "m")
	var anthropic AnthropicResponse
	if err := json.Unmarshal([]byte(`{"id":"fixed","model":"claude-sonnet-5-5","content":[{"type":"thinking","thinking":"THINK","signature":"signature"},{"type":"text","text":"ANSWER"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`), &anthropic); err != nil {
		t.Fatal(err)
	}
	results["anthropic_buffered"] = AnthropicToResponsesResponse(&anthropic)
	chatResponses := NewChatCompletionsToResponsesStreamState("m")
	chatAnthropic := NewChatCompletionsToAnthropicStreamState("m")
	var responsesEvents []ResponsesStreamEvent
	var anthropicEvents []AnthropicStreamEvent
	for _, raw := range []string{
		`{"id":"fixed","choices":[{"index":0,"delta":{"reasoning_content":"THINK"}}]}`,
		`{"id":"fixed","choices":[{"index":0,"delta":{"content":"ANSWER"}}]}`,
		`{"id":"fixed","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`,
		`{"id":"fixed","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	} {
		var chunk ChatCompletionsChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatal(err)
		}
		responsesEvents = append(responsesEvents, ChatCompletionsChunkToResponsesEvents(&chunk, chatResponses)...)
		anthropicEvents = append(anthropicEvents, ChatCompletionsChunkToAnthropicEvents(&chunk, chatAnthropic)...)
	}
	responsesEvents = append(responsesEvents, FinalizeChatCompletionsResponsesStream(chatResponses)...)
	anthropicEvents = append(anthropicEvents, FinalizeChatCompletionsAnthropicStream(chatAnthropic)...)
	results["chat_stream_responses"] = responsesEvents
	results["chat_stream_anthropic"] = anthropicEvents
	state := NewAnthropicEventToResponsesState()
	var events []ResponsesStreamEvent
	for _, raw := range []string{
		`{"type":"message_start","message":{"id":"fixed","model":"claude-sonnet-5-5","usage":{"input_tokens":5}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"FIRST"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"SECOND"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signature"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`{"type":"message_stop"}`,
	} {
		var event AnthropicStreamEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, AnthropicEventToResponsesEvents(&event, state)...)
	}
	events = append(events, FinalizeAnthropicResponsesStream(state)...)
	results["anthropic_stream"] = events
	return results
}

func m4NormalizeGeneratedFields(raw []byte) []byte {
	ids := make(map[string]string)
	idPattern := regexp.MustCompile(`(?:item|resp)_[0-9a-f]{24}`)
	raw = idPattern.ReplaceAllFunc(raw, func(value []byte) []byte {
		key := string(value)
		if _, ok := ids[key]; !ok {
			prefix, _, _ := strings.Cut(key, "_")
			ids[key] = fmt.Sprintf("%s_%024d", prefix, len(ids))
		}
		return []byte(ids[key])
	})
	return regexp.MustCompile(`"created_at":\d+`).ReplaceAll(raw, []byte(`"created_at":0`))
}

func TestM4LegacyProjectionsUnchanged(t *testing.T) {
	actual, err := json.Marshal(m4LegacyProjectionFixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("testdata/m4-legacy-projections.json")
	if err != nil {
		t.Fatal(err)
	}
	m4CheckJSONBytes(t, m4NormalizeGeneratedFields(actual), expected)
}
