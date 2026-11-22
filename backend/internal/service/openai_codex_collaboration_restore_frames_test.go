//go:build unit

package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexCollabFrameFixture struct {
	name, event string
	body        []byte
}

// Payload sizes describe the large opaque field; protocol envelopes add their
// normal overhead. All fixtures are synthetic and run without delivery hooks.
func codexCollabFrameFixtures(size int) []codexCollabFrameFixture {
	large := strings.Repeat("a", size)
	tool := `{"type":"function","name":"collaboration-xy.spawn_agent","description":"` + large + `"}`
	namespace := `{"type":"namespace","name":"collaboration-xy","description":"` + large + `","tools":[]}`
	call := `{"type":"function_call","id":"call_item","call_id":"call_one","name":"spawn_agent","namespace":"collaboration-xy","encrypted_function_args":["message"],"arguments":"{\"message\":\"gAAAA_` + large + `\"}"}`
	plain := `{"type":"function_call","id":"call_item","call_id":"call_one","name":"spawn_agent","namespace":"collaboration-xy","arguments":"{\"message\":\"please review ` + large + `\"}"}`
	failure := `{"code":"invalid_state","message":"collaboration-xy.spawn_agent ` + large + `"}`
	return []codexCollabFrameFixture{
		{"tools", "response.completed", []byte(`{"type":"response.completed","response":{"tools":[` + tool + `]}}`)},
		{"namespace_tools", "response.completed", []byte(`{"type":"response.completed","response":{"tools":[` + namespace + `]}}`)},
		{"tool_choice", "response.completed", []byte(`{"type":"response.completed","response":{"tool_choice":` + tool + `}}`)},
		{"allowed_choice", "response.completed", []byte(`{"type":"response.completed","response":{"tool_choice":{"type":"allowed_tools","tools":[` + tool + `]}}}`)},
		{"item_added", "response.output_item.added", []byte(`{"type":"response.output_item.added","output_index":0,"item":` + call + `}`)},
		{"item_done", "response.output_item.done", []byte(`{"type":"response.output_item.done","output_index":0,"item":` + call + `}`)},
		{"custom_item", "response.output_item.done", []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"custom_tool_call","id":"call_item","name":"spawn_agent","namespace":"collaboration-xy","input":"` + large + `"}}`)},
		{"message_item", "response.output_item.done", []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"message_item","content":[{"type":"output_text","text":"` + large + `"}]}}`)},
		{"image_item", "response.output_item.done", []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"image_generation_call","id":"image_item","result":"` + large + `"}}`)},
		{"namespace_item", "response.output_item.done", []byte(`{"type":"response.output_item.done","output_index":0,"item":` + namespace + `}`)},
		{"arguments_delta", "response.function_call_arguments.delta", []byte(`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"call_item","delta":"` + large + `"}`)},
		{"arguments_done", "response.function_call_arguments.done", []byte(`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"call_item","arguments":"` + large + `"}`)},
		{"custom_input", "response.custom_tool_call_input.delta", []byte(`{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"call_item","delta":"` + large + `"}`)},
		{"completed", "response.completed", []byte(`{"type":"response.completed","response":{"output":[` + call + `]}}`)},
		{"done", "response.done", []byte(`{"type":"response.done","response":{"output":[` + call + `]}}`)},
		{"json", "response.json", []byte(`{"output":[` + call + `]}`)},
		{"json_plain", "response.json", []byte(`{"output":[` + plain + `]}`)},
		{"failed_json", "response.json", []byte(`{"status":"failed","error":` + failure + `}`)},
		{"failed", "response.failed", []byte(`{"type":"response.failed","response":{"error":` + failure + `}}`)},
		{"error", "error", []byte(`{"type":"error","error":` + failure + `}`)},
		{"error_body", "error.body", []byte(`{"error":` + failure + `}`)},
	}
}

func TestCodexCollabRestorerEchoValidationOrder(t *testing.T) {
	for _, field := range []string{"tools", "tool_choice"} {
		for _, identity := range []string{"collaboration-xy.spawn_agent", "lookup"} {
			for _, size := range []int{32 << 10, 1 << 20} {
				tool := `{"type":"function","name":"` + identity + `","description":"` + strings.Repeat("a", size) + `"}`
				value := tool
				if field == "tools" {
					value = "[" + tool + "]"
				}
				body := []byte(`{"` + field + `":` + value + `}`)
				out, err := newCodexCollabRestorer(codexCollabTestPlan(t)).ResponseJSON(body)
				require.NoError(t, err)
				if identity == "lookup" {
					require.Equal(t, body, out)
				} else {
					require.NotContains(t, string(out), "collaboration-xy")
				}
			}
		}
		for _, tool := range []string{
			`{"type":"function","name":"collaboration-xy.spawn_agent","description":"first","description":"second"}`,
			`{"type":"function","name":"lookup","name":"collaboration-xy.spawn_agent"}`,
			`{"type":"function","name":"lookup","namespace":"other","namespace":"collaboration-xy"}`,
		} {
			value := tool
			if field == "tools" {
				value = "[" + tool + "]"
			}
			_, err := newCodexCollabRestorer(codexCollabTestPlan(t)).ResponseJSON([]byte(`{"` + field + `":` + value + `}`))
			require.Error(t, err)
		}
		tool := `{"type":"function","name":"lookup","description":"first","description":"second"}`
		if field == "tools" {
			tool = "[" + tool + "]"
		}
		body := []byte(`{"` + field + `":` + tool + `}`)
		out, err := newCodexCollabRestorer(codexCollabTestPlan(t)).ResponseJSON(body)
		require.NoError(t, err)
		require.Equal(t, body, out)
	}
}

func TestCodexCollabRestorerCompletionWithoutSnapshots(t *testing.T) {
	r := newCodexCollabRestorer(codexCollabTestPlan(t))
	r.BeginResponse(1, 1)
	for _, event := range []string{"response.completed", "response.done", "response.json"} {
		item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review"}`)
		body := []byte(`{"output":[` + string(item) + `]}`)
		var out []byte
		var err error
		if event == "response.json" {
			out, err = r.ResponseJSON(body)
		} else {
			out, err = r.SSEData(event, body)
		}
		require.NoError(t, err)
		require.Equal(t, "[]", gjson.GetBytes(out, "output.0.encrypted_function_args").Raw)
		require.Zero(t, r.snapshotBytes)
	}
}

func BenchmarkCodexCollabRestorerFrameMatrix(b *testing.B) {
	_, plan, _ := planCodexCollaborationPlaintext(codexCollabTestRequest())
	for _, size := range []int{32 << 10, 1 << 20} {
		for _, frame := range codexCollabFrameFixtures(size) {
			b.Run(fmt.Sprintf("%d/%s", size, frame.name), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(frame.body)))
				for b.Loop() {
					r := newCodexCollabRestorer(plan)
					var err error
					switch frame.event {
					case "response.json":
						_, err = r.ResponseJSON(frame.body)
					case "error.body":
						r.ErrorBody(frame.body)
					default:
						_, err = r.SSEData(frame.event, frame.body)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
