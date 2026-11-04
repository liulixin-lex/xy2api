//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Synthetic fixture follows Codex 0.166's captured input[0].additional_tools
// declaration shape, without retaining the captured prompt or local metadata.
const codexCollabTestNamespace = `{"type":"namespace","name":"collaboration","description":"Agent tools","tools":[
{"type":"function","name":"spawn_agent","strict":false,"parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true},"task_name":{"type":"string"}},"required":["message","task_name"],"additionalProperties":false}},
{"type":"function","name":"send_message","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true},"target":{"type":"string"}}}},
{"type":"function","name":"followup_task","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true},"target":{"type":"string"}}}},
{"type":"function","name":"wait_agent","parameters":{"type":"object","properties":{"timeout_ms":{"type":"number"}}}},
{"type":"function","name":"list_agents","parameters":{"type":"object","properties":{}}}
]}`

func codexCollabTestRequest() []byte {
	return []byte(`{"model":"verified-model","store":false,"input":[{"type":"additional_tools","id":"at_fixture","role":"developer","tools":[` + codexCollabTestNamespace + `]}]}`)
}

func codexCollabTestPlan(t *testing.T) *codexCollabPlan {
	t.Helper()
	_, plan, outcome := applyCodexCollaborationPlaintext(codexCollabTestRequest())
	require.Equal(t, codexCollabApplied, outcome)
	require.NotNil(t, plan)
	return plan
}

func codexCollabTestSet(t *testing.T, body []byte, path string, value any) []byte {
	t.Helper()
	next, err := sjson.SetBytes(body, path, value)
	require.NoError(t, err)
	return next
}

func codexCollabTestItem(t *testing.T, name, namespace, arguments string) []byte {
	t.Helper()
	item := map[string]any{"type": "function_call", "id": "item_1", "call_id": "call_1", "name": name, "arguments": arguments}
	if namespace != "" {
		item["namespace"] = namespace
	}
	data, err := json.Marshal(item)
	require.NoError(t, err)
	return data
}

func codexCollabTestSSE(t *testing.T, r *codexCollabRestorer, event string, item []byte) []byte {
	t.Helper()
	frame := []byte(`{"type":"` + event + `","output_index":0,"item":` + string(item) + `}`)
	out, err := r.SSEData(event, frame)
	require.NoError(t, err)
	return []byte(gjson.GetBytes(out, "item").Raw)
}

func TestCodexCollabApplyDeclarationsAndOpaqueHistory(t *testing.T) {
	for _, location := range []string{"additional_tools", "top_level", "both"} {
		t.Run(location, func(t *testing.T) {
			request := codexCollabTestRequest()
			if location != "additional_tools" {
				request = codexCollabTestSet(t, request, "tools", []json.RawMessage{json.RawMessage(codexCollabTestNamespace)})
			}
			if location == "top_level" {
				var err error
				request, err = sjson.DeleteBytes(request, "input.0")
				require.NoError(t, err)
			}
			history := []byte(`[
{"type":"function_call","namespace":"collaboration","name":"spawn_agent","call_id":"collaboration-xy.keep","id":"collaboration.keep","arguments":"{ \"message\": \"gAAAA_cipher\", \"name\": \"collaboration-xy.spawn_agent\" }","encrypted_function_args":["message"]},
{"type":"function_call","namespace":"collaboration","name":"send_message","call_id":"call_2","arguments":"{\"message\":\"hi\"}"},
{"type":"function_call","namespace":"collaboration","name":"followup_task","call_id":"call_3","arguments":"{\"message\":\"hi\"}"},
{"type":"agent_message","content":[ {"type":"encrypted_content","encrypted_content":"gAAAA_original"} ]},
{"type":"function_call_output","call_id":"call_1","output":[ { "type":"encrypted_content", "encrypted_content":"old", "name":"collaboration-xy.spawn_agent" } ]},
{"role":"developer","content":"to=functions.collaboration.spawn_agent remains unchanged"}
]`)
			start := len(gjson.GetBytes(request, "input").Array())
			for _, item := range gjson.ParseBytes(history).Array() {
				request = codexCollabTestSet(t, request, "input.-1", json.RawMessage(item.Raw))
			}
			request = codexCollabTestSet(t, request, "tool_choice", "auto")
			original := bytes.Clone(request)
			out, plan, outcome := applyCodexCollaborationPlaintext(request)
			require.Equal(t, codexCollabApplied, outcome)
			require.Equal(t, original, request, "never mutate the caller's backing array")
			require.Len(t, plan.messageTools, 3)
			if location != "top_level" {
				require.Equal(t, "collaboration-xy", gjson.GetBytes(out, "input.0.tools.0.name").String())
				for i := 0; i < 3; i++ {
					require.False(t, gjson.GetBytes(out, fmt.Sprintf("input.0.tools.0.tools.%d.parameters.properties.message.encrypted", i)).Exists())
				}
			}
			if location != "additional_tools" {
				require.Equal(t, "collaboration-xy", gjson.GetBytes(out, "tools.0.name").String())
			}
			require.Equal(t, "collaboration-xy", gjson.GetBytes(out, fmt.Sprintf("input.%d.namespace", start)).String())
			require.Equal(t, "collaboration-xy", gjson.GetBytes(out, fmt.Sprintf("input.%d.namespace", start+1)).String())
			require.Equal(t, "collaboration-xy", gjson.GetBytes(out, fmt.Sprintf("input.%d.namespace", start+2)).String())
			for i := 0; i < 3; i++ {
				for _, field := range []string{"arguments", "id", "call_id", "encrypted_function_args"} {
					path := fmt.Sprintf("input.%d.%s", start+i, field)
					require.Equal(t, gjson.GetBytes(request, path).Raw, gjson.GetBytes(out, path).Raw, path)
				}
			}
			for i := 3; i < 6; i++ {
				path := fmt.Sprintf("input.%d", start+i)
				require.Equal(t, gjson.GetBytes(request, path).Raw, gjson.GetBytes(out, path).Raw, path)
			}
			require.Equal(t, "auto", gjson.GetBytes(out, "tool_choice").String())
		})
	}
}

func TestCodexCollabApplyAtomicRejections(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(*testing.T, []byte) []byte
		outcome codexCollabOutcome
	}{
		{"invalid JSON", func(t *testing.T, b []byte) []byte { return []byte(`{"input":`) }, codexCollabInvalid},
		{"duplicate root fields", func(t *testing.T, b []byte) []byte { return []byte(`{"store":false,"store":true,"input":[]}`) }, codexCollabInvalid},
		{"not object", func(t *testing.T, b []byte) []byte { return []byte(`[]`) }, codexCollabInvalid},
		{"absent", func(t *testing.T, b []byte) []byte {
			return []byte(`{"tools":[{"type":"function","name":"spawn_agent"}]}`)
		}, codexCollabAbsent},
		{"custom namespace", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "input.0.tools.0.name", "my-agents")
		}, codexCollabAbsent},
		{"resume", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "previous_response_id", "resp_previous")
		}, codexCollabUnsupported},
		{"stored", func(t *testing.T, b []byte) []byte { return codexCollabTestSet(t, b, "store", true) }, codexCollabUnsupported},
		{"private namespace", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "tools", []any{map[string]any{"type": "namespace", "name": "collaboration-xy", "tools": []any{}}})
		}, codexCollabConflict},
		{"private declaration dotted", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "tools", []any{map[string]any{"type": "function", "name": "collaboration-xy.spawn_agent"}})
		}, codexCollabConflict},
		{"private declaration flat", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "tools", []any{map[string]any{"type": "custom", "name": "collaboration-xy__other"}})
		}, codexCollabConflict},
		{"private history", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "input.-1", map[string]any{"type": "function_call", "namespace": "collaboration-xy", "name": "spawn_agent"})
		}, codexCollabConflict},
		{"private tool choice", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "tool_choice", json.RawMessage(`{"type":"allowed_tools","tools":[{"type":"function","function":{"name":"collaboration-xy__spawn_agent"}}]}`))
		}, codexCollabUnsupported},
		{"private instructions", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "instructions", "to=functions.collaboration-xy.spawn_agent")
		}, codexCollabConflict},
		{"private developer example", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "input.-1", json.RawMessage(`{"type":"message","role":"developer","content":"to=functions.collaboration-xy.spawn_agent"}`))
		}, codexCollabConflict},
		{"private system example", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "input.-1", json.RawMessage(`{"type":"message","role":"system","content":[{"type":"input_text","text":"to=functions.collaboration-xy.spawn_agent"}]}`))
		}, codexCollabConflict},
		{"ambiguous developer content", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "input.-1", json.RawMessage(`{"type":"message","role":"developer","content":"functions.collaboration.spawn_agent","content":"other"}`))
		}, codexCollabConflict},
		{"ambiguous system text", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "input.-1", json.RawMessage(`{"type":"message","role":"system","content":[{"type":"input_text","text":"functions.collaboration.spawn_agent","text":"other"}]}`))
		}, codexCollabConflict},
		{"different schema", func(t *testing.T, b []byte) []byte {
			altered := codexCollabTestSet(t, []byte(codexCollabTestNamespace), "tools.0.parameters.properties.message.type", "number")
			return codexCollabTestSet(t, b, "tools", []json.RawMessage{altered})
		}, codexCollabConflict},
		{"message missing", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "input.0.tools.0.tools.2.parameters.properties.message", nil)
		}, codexCollabConflict},
		{"message union", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "input.0.tools.0.tools.1.parameters.properties.message.type", []string{"string", "null"})
		}, codexCollabConflict},
		{"marker abnormal", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "input.0.tools.0.tools.2.parameters.properties.message.encrypted", "true")
		}, codexCollabConflict},
		{"message schema ambiguity", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "input.0.tools.0.tools.2.parameters.properties.message", json.RawMessage(`{"type":"string","encrypted":true,"encrypted":false}`))
		}, codexCollabConflict},
		{"function name absent", func(t *testing.T, b []byte) []byte {
			return codexCollabTestSet(t, b, "input.0.tools.0.tools.2.name", nil)
		}, codexCollabConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := codexCollabTestSet(t, codexCollabTestRequest(), "instructions", "to=functions.collaboration.spawn_agent")
			request = codexCollabTestSet(t, request, "input.-1", json.RawMessage(`{"type":"message","role":"developer","content":"to=functions.collaboration.send_message"}`))
			request = tt.edit(t, request)
			original := bytes.Clone(request)
			out, plan, outcome := applyCodexCollaborationPlaintext(request)
			require.Equal(t, tt.outcome, outcome)
			require.Nil(t, plan)
			require.Equal(t, original, out)
			require.Equal(t, original, request)
		})
	}
}

func TestCodexCollabApplyDeveloperText(t *testing.T) {
	const original = "to=functions.collaboration.spawn_agent"
	const alias = "to=functions.collaboration-xy.spawn_agent"
	tests := []struct {
		name, item, path, text, want string
		rewrites                     int
	}{
		{"instructions", "", "instructions", original, alias, 1},
		{"developer string", `{"type":"message","role":"developer","content":""}`, "input.1.content", original, alias, 1},
		{"system string", `{"type":"message","role":"system","content":""}`, "input.1.content", original, alias, 1},
		{"developer input text", `{"type":"message","role":"developer","content":[{"type":"input_text","text":""}]}`, "input.1.content.0.text", original, alias, 1},
		{"system input text", `{"type":"message","role":"system","content":[{"type":"input_text","text":""}]}`, "input.1.content.0.text", original, alias, 1},
		{"multiple examples", "", "instructions", original + "\nfunctions.collaboration.send_message", alias + "\nfunctions.collaboration-xy.send_message", 2},
		{"exact literal only", "", "instructions", "Functions.collaboration.spawn_agent functions.Collaboration.spawn_agent collaboration.spawn_agent functions.collaboration__spawn_agent", "Functions.collaboration.spawn_agent functions.Collaboration.spawn_agent collaboration.spawn_agent functions.collaboration__spawn_agent", 0},
		{"user string", `{"type":"message","role":"user","content":""}`, "input.1.content", original, original, 0},
		{"assistant input text", `{"type":"message","role":"assistant","content":[{"type":"input_text","text":""}]}`, "input.1.content.0.text", original, original, 0},
		{"message type required", `{"role":"developer","content":""}`, "input.1.content", original, original, 0},
		{"output text excluded", `{"type":"message","role":"developer","content":[{"type":"output_text","text":""}]}`, "input.1.content.0.text", original, original, 0},
		{"tool output excluded", `{"type":"function_call_output","output":""}`, "input.1.output", original, original, 0},
		{"arguments excluded", `{"type":"function_call","namespace":"collaboration","name":"spawn_agent","arguments":""}`, "input.1.arguments", original, original, 0},
		{"historical encrypted text excluded", `{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":""}]}`, "input.1.content.0.encrypted_content", original, original, 0},
		{"user private text excluded", `{"type":"message","role":"user","content":""}`, "input.1.content", alias, alias, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := codexCollabTestRequest()
			if tt.item != "" {
				request = codexCollabTestSet(t, request, "input.-1", json.RawMessage(tt.item))
			}
			request = codexCollabTestSet(t, request, tt.path, tt.text)
			originalRequest := bytes.Clone(request)
			out, plan, outcome := applyCodexCollaborationPlaintext(request)
			require.Equal(t, codexCollabApplied, outcome)
			require.Equal(t, originalRequest, request)
			require.Equal(t, tt.want, gjson.GetBytes(out, tt.path).String())
			if tt.rewrites == 0 {
				require.Equal(t, gjson.GetBytes(request, tt.path).Raw, gjson.GetBytes(out, tt.path).Raw)
			}
			r := newCodexCollabRestorer(plan)
			require.Equal(t, tt.rewrites, r.Stats().DevTextRewrites)
			// Incoming natural language is opaque, and response processing does
			// not count the outbound prompt rewrites a second time.
			response := []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"functions.collaboration-xy.spawn_agent"}]}]}`)
			restored, err := r.ResponseJSON(response)
			require.NoError(t, err)
			require.Equal(t, response, restored)
			require.Equal(t, tt.rewrites, r.Stats().DevTextRewrites)
		})
	}
}

func TestCodexCollabApplyDeveloperTextCombined(t *testing.T) {
	request := codexCollabTestRequest()
	for _, role := range []string{"developer", "system"} {
		request = codexCollabTestSet(t, request, "input.-1", json.RawMessage(`{"type":"message","role":"`+role+`","content":[{"type":"input_text","text":"functions.collaboration.spawn_agent"},{"type":"input_image","image_url":"functions.collaboration.send_message"}]}`))
	}
	request = codexCollabTestSet(t, request, "instructions", "functions.collaboration.followup_task")
	out, plan, outcome := applyCodexCollaborationPlaintext(request)
	require.Equal(t, codexCollabApplied, outcome)
	require.Equal(t, 3, newCodexCollabRestorer(plan).Stats().DevTextRewrites)
	for _, path := range []string{"input.1.content.0.text", "input.2.content.0.text", "instructions"} {
		require.Contains(t, gjson.GetBytes(out, path).String(), "functions.collaboration-xy.")
	}
	for _, path := range []string{"input.1.content.1", "input.2.content.1"} {
		require.Equal(t, gjson.GetBytes(request, path).Raw, gjson.GetBytes(out, path).Raw)
	}
	require.Zero(t, newCodexCollabRestorer(nil).Stats().DevTextRewrites)
}

func TestCodexCollabRequestProtocolRejections(t *testing.T) {
	tests := []struct {
		name, body string
		outcome    codexCollabOutcome
	}{
		{"duplicate additional tools", `{"input":[{"type":"additional_tools","tools":[` + codexCollabTestNamespace + `],"tools":[{"type":"namespace","name":"collaboration-xy","tools":[]}]}]}`, codexCollabConflict},
		{"duplicate type", `{"input":[{"type":"additional_tools","type":"message","tools":[` + codexCollabTestNamespace + `]}]}`, codexCollabConflict},
		{"duplicate tool identity", `{"tools":[` + codexCollabTestNamespace + `,{"type":"namespace","name":"other","name":"collaboration-xy","tools":[]}]}`, codexCollabConflict},
		{"duplicate choice", `{"tools":[` + codexCollabTestNamespace + `],"tool_choice":{"name":"a","name":"b"}}`, codexCollabConflict},
		{"object choice", `{"tools":[` + codexCollabTestNamespace + `],"tool_choice":{"type":"allowed_tools","tools":[]}}`, codexCollabUnsupported},
		{"unsupported choice string", `{"tools":[` + codexCollabTestNamespace + `],"tool_choice":"collaboration"}`, codexCollabUnsupported},
		{"root functions", `{"tools":[` + codexCollabTestNamespace + `],"functions":[]}`, codexCollabUnsupported},
		{"nested declaration", `{"tools":[` + codexCollabTestNamespace + `,{"type":"function","function":{"namespace":"collaboration-xy","name":"spawn_agent"}}]}`, codexCollabUnsupported},
		{"nested history", `{"tools":[` + codexCollabTestNamespace + `],"input":[{"type":"function_call","function":{"namespace":"collaboration-xy","name":"spawn_agent"}}]}`, codexCollabUnsupported},
		{"dotted history", `{"tools":[` + codexCollabTestNamespace + `],"input":[{"type":"function_call","name":"collaboration.spawn_agent"}]}`, codexCollabUnsupported},
		{"flat history", `{"tools":[` + codexCollabTestNamespace + `],"input":[{"type":"function_call","name":"collaboration__spawn_agent"}]}`, codexCollabUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			out, plan, outcome := applyCodexCollaborationPlaintext(body)
			require.Equal(t, tt.outcome, outcome)
			require.Nil(t, plan)
			require.Equal(t, body, out)
		})
	}
	for _, choice := range []string{"auto", "none", "required"} {
		body := codexCollabTestSet(t, codexCollabTestRequest(), "tool_choice", choice)
		out, _, outcome := applyCodexCollaborationPlaintext(body)
		require.Equal(t, codexCollabApplied, outcome)
		require.Equal(t, choice, gjson.GetBytes(out, "tool_choice").String())
	}
}

func TestCodexCollabPromptAndIdentityEchoes(t *testing.T) {
	request := codexCollabTestSet(t, codexCollabTestRequest(), "instructions", "Call functions.collaboration.send_message")
	_, plan, outcome := applyCodexCollaborationPlaintext(request)
	require.Equal(t, codexCollabApplied, outcome)
	for _, tt := range []struct{ name, event, body, path string }{
		{"json", "", `{"instructions":"Call functions.collaboration-xy.send_message","output":[]}`, "instructions"},
		{"created", "response.created", `{"type":"response.created","response":{"instructions":"Call functions.collaboration-xy.send_message","output":[]}}`, "response.instructions"},
		{"in progress", "response.in_progress", `{"type":"response.in_progress","response":{"instructions":"Call functions.collaboration-xy.send_message","output":[]}}`, "response.instructions"},
		{"completed", "response.completed", `{"type":"response.completed","response":{"instructions":"Call functions.collaboration-xy.send_message","output":[]}}`, "response.instructions"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newCodexCollabRestorer(plan)
			var out []byte
			var err error
			if tt.event == "" {
				out, err = r.ResponseJSON([]byte(tt.body))
			} else {
				out, err = r.SSEData(tt.event, []byte(tt.body))
			}
			require.NoError(t, err)
			require.Equal(t, "Call functions.collaboration.send_message", gjson.GetBytes(out, tt.path).String())
			observed, err := newCodexCollabRestorer(nil).ResponseJSON([]byte(tt.body))
			require.NoError(t, err)
			require.Equal(t, []byte(tt.body), observed)
		})
	}
	for _, body := range []string{
		`{"output":[],"tools":[{"type":"namespace","name":"collaboration-xy","name":"collaboration-xy","tools":[]}]}`,
		`{"output":[],"tool_choice":{"type":"function","namespace":"collaboration-xy","namespace":"collaboration-xy","name":"spawn_agent"}}`,
		`{"output":[{"type":"namespace","name":"collaboration-xy","name":"collaboration-xy"}]}`,
		`{"output":[{"type":"message","type":"namespace","name":"collaboration-xy"}]}`,
	} {
		r := newCodexCollabRestorer(plan)
		out, err := r.ResponseJSON([]byte(body))
		require.Error(t, err)
		require.Nil(t, out)
		out, err = r.SSEData("response.completed", []byte(body))
		require.Error(t, err)
		require.Nil(t, out)
	}
	unchanged := []byte(`{"instructions":"functions.collaboration-xy.spawn_agent","output":[]}`)
	out, err := newCodexCollabRestorer(codexCollabTestPlan(t)).ResponseJSON(unchanged)
	require.NoError(t, err)
	require.Equal(t, unchanged, out)
}

func TestCodexCollabMessageOnlyStats(t *testing.T) {
	for _, tt := range []struct {
		name, message string
		opaque, text  int
	}{
		{"opaque", "gAAAAABpZm9vYmFyYmF6cXV4cXV1eHF1dXhxdXV4cXV1eA==", 1, 0},
		{"text", "please review the diff", 0, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			items := []string{}
			for i, name := range []string{"spawn_agent", "wait_agent", "list_agents"} {
				item := codexCollabTestItem(t, name, "collaboration", `{"message":"`+tt.message+`"}`)
				item = codexCollabTestSet(t, item, "id", fmt.Sprintf("item_%d", i))
				item = codexCollabTestSet(t, item, "call_id", fmt.Sprintf("call_%d", i))
				items = append(items, string(item))
			}
			body := []byte(`{"output":[` + strings.Join(items, ",") + `]}`)
			r := newCodexCollabRestorer(nil)
			out, err := r.ResponseJSON(body)
			require.NoError(t, err)
			require.Equal(t, body, out)
			_, err = r.ResponseJSON(body)
			require.NoError(t, err)
			require.Equal(t, 1, r.Stats().Calls)
			require.Equal(t, 1, r.Stats().EFAAbsent)
			require.Equal(t, tt.opaque, r.Stats().MsgOpaque)
			require.Equal(t, tt.text, r.Stats().MsgText)
			applied := newCodexCollabRestorer(codexCollabTestPlan(t))
			_, err = applied.ResponseJSON([]byte(strings.ReplaceAll(string(body), `"namespace":"collaboration"`, `"namespace":"collaboration-xy"`)))
			require.NoError(t, err)
			require.Equal(t, 1, applied.Stats().Calls)
			require.Equal(t, 1, applied.Stats().EFAAbsent)
			require.Zero(t, applied.Stats().MsgText+applied.Stats().MsgOpaque)
		})
	}
}

func TestCodexCollabObserveMessageStreamEvidence(t *testing.T) {
	r := newCodexCollabRestorer(nil)
	for _, args := range []string{`{`, `{}`, `{"message":42}`, `{"message":"working text"}`, `{"message":"gAAAAcipher"}`, `{"message":"working text"}`} {
		item := codexCollabTestItem(t, "send_message", "collaboration", args)
		out, err := r.SSEData("response.output_item.done", []byte(`{"output_index":0,"item":`+string(item)+`}`))
		require.NoError(t, err)
		require.NotEmpty(t, out)
	}
	require.Equal(t, 1, r.Stats().Calls)
	require.Equal(t, 1, r.Stats().MsgOpaque)
	require.Zero(t, r.Stats().MsgText)
}

func TestCodexCollabRestorerCanonicalEchoes(t *testing.T) {
	identities := []struct{ name, namespace string }{
		{"spawn_agent", "collaboration-xy"}, // Precheck 2a's actual upstream shape.
		{"collaboration-xy.spawn_agent", ""},
		{"collaboration-xy__spawn_agent", ""},
	}
	for _, identity := range identities {
		for _, transport := range []string{"SSE", "JSON", "SSE aggregate"} {
			t.Run(identity.name+identity.namespace+"/"+transport, func(t *testing.T) {
				r := newCodexCollabRestorer(codexCollabTestPlan(t))
				item := codexCollabTestItem(t, identity.name, identity.namespace, `{ "message": "你好，task-7", "task_name": "child" }`)
				// Raw identity/content values must survive unrelated edits exactly.
				item = codexCollabTestSet(t, item, "call_id", "call/collaboration-xy__spawn_agent")
				var restored []byte
				if transport == "SSE" {
					restored = codexCollabTestSSE(t, r, "response.output_item.done", item)
				} else {
					response := []byte(`{"output":[` + string(item) + `]}`)
					if transport == "SSE aggregate" {
						response = []byte(`{"type":"response.completed","response":` + string(response) + `}`)
					}
					var out []byte
					var err error
					if transport == "JSON" {
						out, err = r.ResponseJSON(response)
					} else {
						out, err = r.SSEData("response.completed", response)
					}
					require.NoError(t, err)
					path := "output.0"
					if transport == "SSE aggregate" {
						path = "response.output.0"
					}
					restored = []byte(gjson.GetBytes(out, path).Raw)
				}
				require.Equal(t, "function_call", gjson.GetBytes(restored, "type").String())
				require.Equal(t, "collaboration", gjson.GetBytes(restored, "namespace").String())
				require.Equal(t, "spawn_agent", gjson.GetBytes(restored, "name").String())
				require.Equal(t, "[]", gjson.GetBytes(restored, "encrypted_function_args").Raw)
				for _, field := range []string{"call_id", "id", "arguments"} {
					require.Equal(t, gjson.GetBytes(item, field).Raw, gjson.GetBytes(restored, field).Raw)
				}
				require.Equal(t, codexCollabStats{Outcome: codexCollabApplied, Applied: true, Calls: 1, EFAAbsent: 1, MarkerAdded: 1}, r.Stats())
			})
		}
	}
}

func TestCodexCollabErrorBody(t *testing.T) {
	const body = `{"error":{"type":"collaboration-xy","code":"collaboration-xy","message":"Unknown collaboration-xy.send_message, collaboration-xy__spawn_agent and collaboration-xy; xcollaboration-xy and collaboration-xyz stay","param":"collaboration-xy.send_message"},"other":"collaboration-xy"}`
	const expected = `{"error":{"type":"collaboration-xy","code":"collaboration-xy","message":"Unknown collaboration.send_message, collaboration__spawn_agent and collaboration; xcollaboration-xy and collaboration-xyz stay","param":"collaboration.send_message"},"other":"collaboration-xy"}`
	r := newCodexCollabRestorer(codexCollabTestPlan(t))
	require.JSONEq(t, expected, string(r.ErrorBody([]byte(body))))
	require.Equal(t, []byte(body), newCodexCollabRestorer(nil).ErrorBody([]byte(body)))
	for _, input := range []string{`{"error":`, `{"error":{"message":"collaboration-xy","message":"collaboration-xy"}}`} {
		require.Equal(t, []byte(input), r.ErrorBody([]byte(input)))
	}
	for _, tt := range []struct{ event, body, path string }{
		{"error", `{"type":"error","error":{"message":"collaboration-xy.send_message","param":"collaboration-xy__send_message","code":"collaboration-xy"}}`, "error"},
		{"response.failed", `{"type":"response.failed","response":{"error":{"message":"collaboration-xy.send_message","param":"collaboration-xy__send_message","code":"collaboration-xy"}}}`, "response.error"},
	} {
		t.Run(tt.event, func(t *testing.T) {
			out, err := r.SSEData(tt.event, []byte(tt.body))
			require.NoError(t, err)
			require.Equal(t, "collaboration.send_message", gjson.GetBytes(out, tt.path+".message").String())
			require.Equal(t, "collaboration__send_message", gjson.GetBytes(out, tt.path+".param").String())
			require.Equal(t, "collaboration-xy", gjson.GetBytes(out, tt.path+".code").String())
			observed, err := newCodexCollabRestorer(nil).SSEData(tt.event, []byte(tt.body))
			require.NoError(t, err)
			require.Equal(t, []byte(tt.body), observed)
		})
	}
}

func TestCodexCollabRestorerMarkerVetoes(t *testing.T) {
	tests := []struct {
		name, args, efa    string
		added, empty, veto int
	}{
		{name: "plain Unicode", args: `{"message":"你好🌏"}`, added: 1},
		{name: "short no spaces", args: `{"message":"task-7"}`, added: 1},
		{name: "fernet", args: `{"message":"gAAAA_secret"}`, veto: 1},
		{name: "base64url", args: `{"message":"` + strings.Repeat("A", 40) + `"}`, veto: 1},
		{name: "base64", args: `{"message":"` + strings.Repeat("ab+4/", 8) + `"}`, veto: 1},
		{name: "nonempty EFA", args: `{"message":"hello"}`, efa: `["message"]`, veto: 1},
		{name: "upstream empty", args: `{"message":"gAAAA_secret"}`, efa: `[]`, empty: 1},
		{name: "null EFA", args: `{"message":"hello"}`, efa: `null`, veto: 1},
		{name: "scalar EFA", args: `{"message":"hello"}`, efa: `false`, veto: 1},
		{name: "object EFA", args: `{"message":"hello"}`, efa: `{}`, veto: 1},
		{name: "invalid arguments", args: `{"message":`, veto: 1},
		{name: "message nonstring", args: `{"message":123}`, veto: 1},
		{name: "message absent", args: `{"target":"child"}`, veto: 1},
		{name: "other encrypted data", args: `{"message":"hi", "data":{"encrypted_content":"secret"}}`, veto: 1},
		{name: "other encrypted token", args: `{"message":"hi", "data":"gAAAA_secret"}`, veto: 1},
		{name: "multiple JSON values", args: `{"message":"hi"} {}`, veto: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCodexCollabRestorer(codexCollabTestPlan(t))
			item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", tt.args)
			if tt.efa != "" {
				item = codexCollabTestSet(t, item, "encrypted_function_args", json.RawMessage(tt.efa))
			}
			out := codexCollabTestSSE(t, r, "response.output_item.done", item)
			require.Equal(t, "collaboration", gjson.GetBytes(out, "namespace").String())
			if tt.added > 0 {
				require.Equal(t, "[]", gjson.GetBytes(out, "encrypted_function_args").Raw)
			} else {
				require.Equal(t, gjson.GetBytes(item, "encrypted_function_args").Raw, gjson.GetBytes(out, "encrypted_function_args").Raw)
			}
			require.Equal(t, tt.added, r.Stats().MarkerAdded)
			require.Equal(t, tt.veto, r.Stats().Vetoed)
			require.Equal(t, tt.empty, r.Stats().EFAEmpty)
			require.Equal(t, gjson.GetBytes(item, "arguments").Raw, gjson.GetBytes(out, "arguments").Raw)
		})
	}
}

func TestCodexCollabRestorerStreamConsistency(t *testing.T) {
	tests := []struct {
		name, early, final        string
		added, veto, contradicted int
	}{
		{name: "absent throughout", added: 1},
		{name: "early nonempty later absent", early: `["message"]`, veto: 1, contradicted: 1},
		{name: "early nonempty later empty", early: `["message"]`, final: `[]`, veto: 1, contradicted: 1},
		{name: "nonempty throughout", early: `["message"]`, final: `["message"]`, veto: 1},
		{name: "upstream empty", final: `[]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCodexCollabRestorer(codexCollabTestPlan(t))
			done := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"hello world"}`)
			early := codexCollabTestSet(t, done, "arguments", "")
			var err error
			early, err = sjson.DeleteBytes(early, "call_id")
			require.NoError(t, err)
			if tt.early != "" {
				early = codexCollabTestSet(t, early, "encrypted_function_args", json.RawMessage(tt.early))
			}
			added := codexCollabTestSSE(t, r, "response.output_item.added", early)
			require.Equal(t, gjson.GetBytes(early, "encrypted_function_args").Raw, gjson.GetBytes(added, "encrypted_function_args").Raw)
			if tt.final != "" {
				done = codexCollabTestSet(t, done, "encrypted_function_args", json.RawMessage(tt.final))
			}
			restored := codexCollabTestSSE(t, r, "response.output_item.done", done)
			terminal, err := r.SSEData("response.completed", []byte(`{"type":"response.completed","response":{"output":[`+string(done)+`]}}`))
			require.NoError(t, err)
			require.JSONEq(t, string(restored), gjson.GetBytes(terminal, "response.output.0").Raw)
			require.Equal(t, 1, r.Stats().Calls)
			require.Equal(t, tt.added, r.Stats().MarkerAdded)
			require.Equal(t, tt.veto, r.Stats().Vetoed)
			require.Equal(t, tt.contradicted, r.Stats().Contradicted)
			if tt.final != "" {
				require.Equal(t, tt.final, gjson.GetBytes(restored, "encrypted_function_args").Raw)
			}
			failure := []byte(`{"type":"response.failed","response":{"error":{"code":"invalid_state"}}}`)
			out, err := r.SSEData("response.failed", failure)
			require.NoError(t, err)
			require.Equal(t, failure, out)
		})
	}
}

func TestCodexCollabRestorerUncoveredAndNonMessageTools(t *testing.T) {
	tests := []struct {
		name, typ        string
		calls, uncovered int
	}{
		{"wait_agent", "function_call", 0, 0},
		{"list_agents", "function_call", 0, 0},
		{"spawn_agent", "custom_tool_call", 0, 1},
		{"undeclared", "function_call", 0, 1},
		{"undeclared", "custom_tool_call", 0, 1},
	}
	for _, tt := range tests {
		for _, shape := range []string{"namespace", "dot", "flat"} {
			t.Run(tt.name+tt.typ+shape, func(t *testing.T) {
				r := newCodexCollabRestorer(codexCollabTestPlan(t))
				name, namespace := tt.name, "collaboration-xy"
				if shape == "dot" {
					name = "collaboration-xy." + tt.name
					namespace = ""
				}
				if shape == "flat" {
					name = "collaboration-xy__" + tt.name
					namespace = ""
				}
				item := codexCollabTestItem(t, name, namespace, `{"message":"hello"}`)
				item = codexCollabTestSet(t, item, "type", tt.typ)
				out := codexCollabTestSSE(t, r, "response.output_item.done", item)
				require.Equal(t, tt.name, gjson.GetBytes(out, "name").String())
				require.Equal(t, "collaboration", gjson.GetBytes(out, "namespace").String())
				require.Equal(t, tt.typ, gjson.GetBytes(out, "type").String())
				require.False(t, gjson.GetBytes(out, "encrypted_function_args").Exists())
				require.Equal(t, tt.calls, r.Stats().Calls)
				require.Equal(t, tt.uncovered, r.Stats().Uncovered)
			})
		}
	}
}

func TestCodexCollabRestorerPlanEligibilityAndIsolation(t *testing.T) {
	for _, mode := range []string{"encrypted false", "marker absent", "extra encrypted schema"} {
		t.Run(mode, func(t *testing.T) {
			request := codexCollabTestRequest()
			path := "input.0.tools.0.tools.0.parameters.properties.message.encrypted"
			if mode == "encrypted false" {
				request = codexCollabTestSet(t, request, path, false)
			}
			if mode == "marker absent" {
				var err error
				request, err = sjson.DeleteBytes(request, path)
				require.NoError(t, err)
			}
			if mode == "extra encrypted schema" {
				request = codexCollabTestSet(t, request, "input.0.tools.0.tools.0.parameters.properties.task_name.encrypted", true)
			}
			_, plan, outcome := applyCodexCollaborationPlaintext(request)
			require.Equal(t, codexCollabApplied, outcome)
			r := newCodexCollabRestorer(plan)
			item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"hello","task_name":"child"}`)
			out := codexCollabTestSSE(t, r, "response.output_item.done", item)
			require.False(t, gjson.GetBytes(out, "encrypted_function_args").Exists())
			require.Zero(t, r.Stats().MarkerAdded)
		})
	}
	plan := codexCollabTestPlan(t)
	first, second := newCodexCollabRestorer(plan), newCodexCollabRestorer(plan)
	item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"hello"}`)
	cipher := codexCollabTestSet(t, item, "encrypted_function_args", []string{"message"})
	codexCollabTestSSE(t, first, "response.output_item.added", cipher)
	firstOut := codexCollabTestSSE(t, first, "response.output_item.done", item)
	secondOut := codexCollabTestSSE(t, second, "response.output_item.done", item)
	require.False(t, gjson.GetBytes(firstOut, "encrypted_function_args").Exists())
	require.Equal(t, "[]", gjson.GetBytes(secondOut, "encrypted_function_args").Raw)
}

func TestCodexCollabRestorerOpaqueContainersAndArgumentEvents(t *testing.T) {
	r := newCodexCollabRestorer(codexCollabTestPlan(t))
	item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{ "message": "hello world", "user_json":{"name":"collaboration-xy.spawn_agent"} }`)
	opaque := `{"type":"function_call_output","call_id":"collaboration-xy.name","output":[ {"type":"function_call","namespace":"collaboration-xy","name":"spawn_agent","arguments":"fake"} ]}`
	body := []byte(`{"output":[` + opaque + `,` + string(item) + `],"input":[{"type":"function_call","namespace":"collaboration-xy","name":"spawn_agent"}]}`)
	out, err := r.ResponseJSON(body)
	require.NoError(t, err)
	require.Equal(t, opaque, gjson.GetBytes(out, "output.0").Raw)
	require.Equal(t, gjson.GetBytes(body, "input").Raw, gjson.GetBytes(out, "input").Raw)
	require.Equal(t, gjson.GetBytes(body, "output.1.arguments").Raw, gjson.GetBytes(out, "output.1.arguments").Raw)
	require.Equal(t, "collaboration", gjson.GetBytes(out, "output.1.namespace").String())
	// The argument delta is opaque even when it contains a private tool name.
	delta := codexCollabTestSet(t, []byte(`{"type":"response.function_call_arguments.delta","item_id":"other","output_index":2,"name":"collaboration-xy__send_message"}`), "delta", `{"message":"collaboration-xy.name"`)
	restored, err := r.SSEData("response.function_call_arguments.delta", delta)
	require.NoError(t, err)
	require.Equal(t, "send_message", gjson.GetBytes(restored, "name").String())
	require.Equal(t, "collaboration", gjson.GetBytes(restored, "namespace").String())
	for _, field := range []string{"delta", "item_id", "type"} {
		require.Equal(t, gjson.GetBytes(delta, field).Raw, gjson.GetBytes(restored, field).Raw)
	}
	require.False(t, gjson.GetBytes(restored, "encrypted_function_args").Exists())
}

func TestCodexCollabRestorerObserveOnly(t *testing.T) {
	r := newCodexCollabRestorer(nil)
	items := [][]byte{}
	for i, efa := range []string{"", `[]`, `["message"]`} {
		item := codexCollabTestItem(t, "spawn_agent", "collaboration", `{"message":"hello"}`)
		item = codexCollabTestSet(t, item, "id", fmt.Sprintf("item_%d", i))
		item = codexCollabTestSet(t, item, "call_id", fmt.Sprintf("call_%d", i))
		if efa != "" {
			item = codexCollabTestSet(t, item, "encrypted_function_args", json.RawMessage(efa))
		}
		items = append(items, item)
	}
	body := []byte(`{ "output": [` + string(bytes.Join(items, []byte(","))) + `] }`)
	out, err := r.ResponseJSON(body)
	require.NoError(t, err)
	require.Equal(t, body, out)
	require.Equal(t, codexCollabStats{Outcome: codexCollabAbsent, Calls: 3, EFAAbsent: 1, EFAEmpty: 1, EFANonEmpty: 1, MsgText: 3}, r.Stats())
	for _, invalid := range []string{`not-json`, `{"item":`, "[DONE]"} {
		out, err = r.SSEData("", []byte(invalid))
		require.NoError(t, err)
		require.Equal(t, invalid, string(out))
	}
}

func TestCodexCollabRestorerUnsafeIdentityErrors(t *testing.T) {
	for _, body := range []string{
		`{"output":`,
		`{"output":{}}`,
		`{"output":[{"type":"function_call","namespace":"collaboration-xy","name":3}]}`,
		`{"output":[{"type":"function_call","namespace":"other","name":"collaboration-xy.spawn_agent"}]}`,
		`{"output":[{"type":"function_call","namespace":"collaboration-xy","name":"spawn_agent","name":"send_message"}]}`,
		`{"output":[],"tool_choice":{"type":"function","namespace":"other","name":"collaboration-xy.spawn_agent"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			r := newCodexCollabRestorer(codexCollabTestPlan(t))
			out, err := r.ResponseJSON([]byte(body))
			require.Error(t, err)
			require.Empty(t, out)
		})
	}
}

func TestCodexCollabRestorerToolEchoes(t *testing.T) {
	r := newCodexCollabRestorer(codexCollabTestPlan(t))
	body := []byte(`{"output":[],"tools":[{"type":"namespace","name":"collaboration-xy","tools":[{"type":"function","name":"spawn_agent"}]}],"tool_choice":{"type":"allowed_tools","tools":[{"type":"function","name":"collaboration-xy__spawn_agent"},{"type":"function","namespace":"collaboration-xy","name":"unknown"}]}}`)
	out, err := r.ResponseJSON(body)
	require.NoError(t, err)
	require.Equal(t, "collaboration", gjson.GetBytes(out, "tools.0.name").String())
	require.Equal(t, "spawn_agent", gjson.GetBytes(out, "tools.0.tools.0.name").String())
	require.Equal(t, "collaboration", gjson.GetBytes(out, "tool_choice.tools.0.namespace").String())
	require.Equal(t, "spawn_agent", gjson.GetBytes(out, "tool_choice.tools.0.name").String())
	require.Equal(t, "collaboration", gjson.GetBytes(out, "tool_choice.tools.1.namespace").String())
	require.Equal(t, "unknown", gjson.GetBytes(out, "tool_choice.tools.1.name").String())
}

func TestCodexCollabAttemptContext(t *testing.T) {
	c := &gin.Context{}
	_, ok := openAICodexCollabAttempt(c)
	require.False(t, ok)
	stats := codexCollabStats{Outcome: codexCollabApplied, Applied: true, Calls: 3}
	setOpenAICodexCollabAttempt(c, stats)
	got, ok := openAICodexCollabAttempt(c)
	require.True(t, ok)
	require.Equal(t, stats, got)
	next := codexCollabStats{Outcome: codexCollabUnsupported}
	setOpenAICodexCollabAttempt(c, next)
	got, ok = openAICodexCollabAttempt(c)
	require.True(t, ok)
	require.Equal(t, next, got)
	setOpenAICodexCollabAttempt(nil, stats)
	_, ok = openAICodexCollabAttempt(nil)
	require.False(t, ok)
}

func TestCodexCollabRestorerLateCiphertextAndArgumentChanges(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, []byte) []byte
	}{
		{"late nonempty marker", func(t *testing.T, item []byte) []byte {
			return codexCollabTestSet(t, item, "encrypted_function_args", []string{"message"})
		}},
		{"late encrypted arguments", func(t *testing.T, item []byte) []byte {
			return codexCollabTestSet(t, item, "arguments", `{"message":"gAAAA_late_cipher"}`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newCodexCollabRestorer(codexCollabTestPlan(t))
			item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"hello world"}`)
			done := codexCollabTestSSE(t, r, "response.output_item.done", item)
			require.Equal(t, "[]", gjson.GetBytes(done, "encrypted_function_args").Raw)
			changed := tt.change(t, item)
			terminal, err := r.SSEData("response.completed", []byte(`{"type":"response.completed","response":{"output":[`+string(changed)+`]}}`))
			require.NoError(t, err)
			require.Equal(t, gjson.GetBytes(changed, "encrypted_function_args").Raw, gjson.GetBytes(terminal, "response.output.0.encrypted_function_args").Raw)
			require.Equal(t, 1, r.Stats().Contradicted)
			require.Equal(t, 1, r.Stats().Vetoed)
		})
	}
}

func TestCodexCollabRestorerArgumentEvidenceAndAnonymousEvents(t *testing.T) {
	for _, args := range []string{
		`{"message":"gAAAA_cipher","message":"safe-looking"}`,
		`{"message":" safe","data":{"encrypted":"yes","encrypted":false}}`,
		`{"message":"  gAAAA_cipher"}`,
	} {
		t.Run(args, func(t *testing.T) {
			r := newCodexCollabRestorer(codexCollabTestPlan(t))
			item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", args)
			out := codexCollabTestSSE(t, r, "response.output_item.done", item)
			require.False(t, gjson.GetBytes(out, "encrypted_function_args").Exists())
			require.Equal(t, 1, r.Stats().Vetoed)
		})
	}
	t.Run("ordinary target UUID is not ciphertext", func(t *testing.T) {
		r := newCodexCollabRestorer(codexCollabTestPlan(t))
		item := codexCollabTestItem(t, "send_message", "collaboration-xy", `{"message":"hello world","target":"1d7efb4a-02a0-4bcb-83f0-70b713da1c22"}`)
		out := codexCollabTestSSE(t, r, "response.output_item.done", item)
		require.Equal(t, "[]", gjson.GetBytes(out, "encrypted_function_args").Raw)
	})
	t.Run("marker on argument event is remembered", func(t *testing.T) {
		r := newCodexCollabRestorer(codexCollabTestPlan(t))
		delta := []byte(`{"type":"response.function_call_arguments.delta","item_id":"item_1","encrypted_function_args":["message"],"delta":"unchanged"}`)
		out, err := r.SSEData("response.function_call_arguments.delta", delta)
		require.NoError(t, err)
		require.Equal(t, delta, out)
		item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"hello world"}`)
		done := codexCollabTestSSE(t, r, "response.output_item.done", item)
		require.False(t, gjson.GetBytes(done, "encrypted_function_args").Exists())
		require.Equal(t, 1, r.Stats().Contradicted)
	})
	t.Run("anonymous stream item is never pooled or marked", func(t *testing.T) {
		r := newCodexCollabRestorer(codexCollabTestPlan(t))
		frame := []byte(`{"type":"response.output_item.done","item":{"type":"function_call","name":"spawn_agent","namespace":"collaboration-xy","arguments":"{\"message\":\"hello world\"}"}}`)
		out, err := r.SSEData("response.output_item.done", frame)
		require.NoError(t, err)
		require.Equal(t, "collaboration", gjson.GetBytes(out, "item.namespace").String())
		require.False(t, gjson.GetBytes(out, "item.encrypted_function_args").Exists())
		require.Equal(t, 1, r.Stats().Vetoed)
	})
}

func TestCodexCollabThreeMessageTools(t *testing.T) {
	for _, name := range []string{"spawn_agent", "send_message", "followup_task"} {
		t.Run(name, func(t *testing.T) {
			r := newCodexCollabRestorer(codexCollabTestPlan(t))
			item := codexCollabTestItem(t, name, "collaboration-xy", `{"message":"hello world"}`)
			out := codexCollabTestSSE(t, r, "response.output_item.done", item)
			require.Equal(t, name, gjson.GetBytes(out, "name").String())
			require.Equal(t, "collaboration", gjson.GetBytes(out, "namespace").String())
			require.Equal(t, "[]", gjson.GetBytes(out, "encrypted_function_args").Raw)
		})
	}
}
