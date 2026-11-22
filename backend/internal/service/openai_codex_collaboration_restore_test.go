//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexCollabRestorerPhysicalResponseIsolation(t *testing.T) {
	for _, tool := range []string{"spawn_agent", "send_message"} {
		t.Run(tool, func(t *testing.T) {
			r := newCodexCollabRestorer(codexCollabTestPlan(t))
			r.BeginResponse(1, 1)
			first := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"gAAAA_cipher"}`)
			first = codexCollabTestSet(t, first, "encrypted_function_args", []string{"message"})
			codexCollabTestSSE(t, r, "response.output_item.added", first)
			_, err := r.SSEData("response.failed", []byte(`{"type":"response.failed","response":{"error":{"code":"invalid_state"}}}`))
			require.NoError(t, err)
			stats, sealed := r.SealStats()
			require.True(t, sealed)
			require.Equal(t, 1, stats.Calls)
			_, sealed = r.SealStats()
			require.False(t, sealed)
			r.BeginResponse(2, 2)
			second := codexCollabTestItem(t, tool, "collaboration-xy", `{"message":"please review the change"}`)
			second = codexCollabTestSet(t, second, "id", "item_next")
			second = codexCollabTestSet(t, second, "call_id", "call_next")
			out := codexCollabTestSSE(t, r, "response.output_item.done", second)
			require.Equal(t, "[]", gjson.GetBytes(out, "encrypted_function_args").Raw)
			require.Equal(t, 2, r.Stats().Calls)
			require.Equal(t, 1, r.Stats().EFANonEmpty)
			require.Equal(t, 1, r.Stats().MarkerAdded)
		})
	}
}

func TestCodexCollabRestorerGenerationSharesAllExits(t *testing.T) {
	r := newCodexCollabRestorer(codexCollabTestPlan(t))
	r.BeginResponse(1, 1)
	item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review the change"}`)
	early := codexCollabTestSet(t, item, "encrypted_function_args", []string{"message"})
	codexCollabTestSSE(t, r, "response.output_item.added", early)
	r.BeginResponse(1, 1)
	r.BeginResponse(0, 0)
	for _, route := range []string{"done", "completed", "json"} {
		var out []byte
		var err error
		path := "output.0"
		switch route {
		case "done":
			out = codexCollabTestSSE(t, r, "response.output_item.done", item)
			path = ""
		case "completed":
			out, err = r.SSEData("response.completed", []byte(`{"response":{"output":[`+string(item)+`]}}`))
			path = "response.output.0"
		default:
			out, err = r.ResponseJSON([]byte(`{"output":[` + string(item) + `]}`))
		}
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(out, codexCollabPath(path, "encrypted_function_args")).Exists())
	}
	require.Equal(t, 1, r.Stats().Calls)
	r.Completed(context.Background(), resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1})
	_, sealed := r.SealStats()
	require.False(t, sealed)
	r.BeginResponse(2, 2)
	r.Completed(context.Background(), resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1})
	codexCollabTestSSE(t, r, "response.output_item.done", item)
	require.Equal(t, 2, r.Stats().Calls)
	require.Equal(t, 1, r.Stats().MarkerAdded)
}

func codexCollabRestoreDone(t *testing.T, r *codexCollabRestorer, item []byte, index int) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{"type": "response.output_item.done", "output_index": index, "item": json.RawMessage(item)})
	require.NoError(t, err)
	out, err := r.SSEData("response.output_item.done", frame)
	require.NoError(t, err)
	return out
}

func codexCollabConfirmDone(r *codexCollabRestorer, frame []byte) {
	hooks := &resilience.CollabDeliveryHooks{}
	hooks.Register(r)
	hooks.Written(context.Background(), resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1, Sequence: 1,
		EventType: "response.output_item.done", Executable: true, Successful: true, BytesWritten: len(frame)}, frame)
}

func TestCodexCollabRestorerDeliveredSnapshots(t *testing.T) {
	for _, change := range []string{"tool", "type", "arguments", "marker", "unrelated"} {
		for _, route := range []string{"response.completed", "response.done", "json"} {
			t.Run(change+"/"+route, func(t *testing.T) {
				r := newCodexCollabRestorer(codexCollabTestPlan(t))
				r.BeginResponse(1, 1)
				item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review the change"}`)
				done := codexCollabRestoreDone(t, r, item, 0)
				codexCollabConfirmDone(r, done)
				codexCollabConfirmDone(r, done)
				snapshot := gjson.GetBytes(done, "item").Raw
				changed := append([]byte(nil), item...)
				switch change {
				case "tool":
					changed = codexCollabTestSet(t, changed, "name", "send_message")
				case "type":
					changed = codexCollabTestSet(t, changed, "type", "custom_tool_call")
				case "arguments":
					changed = codexCollabTestSet(t, changed, "arguments", `{"message":"gAAAA_cipher"}`)
				case "marker":
					changed = codexCollabTestSet(t, changed, "encrypted_function_args", []string{"message"})
				case "unrelated":
					changed = codexCollabTestSet(t, changed, "namespace", "other")
				}
				var out []byte
				var err error
				path := "response.output.0"
				if route == "json" {
					out, err = r.ResponseJSON([]byte(`{"output":[` + string(changed) + `]}`))
					path = "output.0"
				} else {
					out, err = r.SSEData(route, []byte(`{"response":{"output":[`+string(changed)+`]}}`))
				}
				require.NoError(t, err)
				require.Equal(t, snapshot, gjson.GetBytes(out, path).Raw)
				require.Equal(t, 1, r.Stats().Contradicted)
				require.Equal(t, 1, r.Stats().Calls)
				require.Zero(t, r.Stats().Vetoed)
			})
		}
	}
}

func TestCodexCollabRestorerDeliveryConfirmationGuards(t *testing.T) {
	for _, mode := range []string{"received", "about_to_write", "write_error", "partial", "unsuccessful", "old_generation", "other_send", "other_payload", "unrestored"} {
		t.Run(mode, func(t *testing.T) {
			r := newCodexCollabRestorer(codexCollabTestPlan(t))
			r.BeginResponse(1, 1)
			item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review the change"}`)
			done := codexCollabRestoreDone(t, r, item, 0)
			hooks := &resilience.CollabDeliveryHooks{}
			hooks.Register(r)
			payload := done
			event := resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1, EventType: "response.output_item.done", Executable: true, Successful: true, BytesWritten: len(done)}
			switch mode {
			case "received":
				hooks.Received(context.Background(), event, payload)
			case "about_to_write":
				hooks.AboutToWrite(context.Background(), event, payload)
			case "write_error":
				event.WriteError = errors.New("synthetic write failure")
			case "partial":
				event.BytesWritten--
			case "unsuccessful":
				event.Successful = false
			case "old_generation":
				event.Generation = 0
			case "other_send":
				event.SendSeq = 2
			case "other_payload":
				payload = codexCollabTestSet(t, done, "item.arguments", `{"message":"different"}`)
			case "unrestored":
				payload = []byte(`{"type":"response.output_item.done","output_index":0,"item":` + string(item) + `}`)
			}
			if mode != "received" && mode != "about_to_write" {
				hooks.Written(context.Background(), event, payload)
			}
			changed := codexCollabTestSet(t, item, "name", "send_message")
			_, err := r.SSEData("response.completed", []byte(`{"response":{"output":[`+string(changed)+`]}}`))
			require.ErrorContains(t, err, "identity changed")
		})
	}
}

func TestCodexCollabRestorerSnapshotDoesNotRelaxExecutableOrConflictingItems(t *testing.T) {
	for _, mode := range []string{"duplicate_done", "conflicting_ids", "unknown_id", "only_index", "duplicate_identity"} {
		t.Run(mode, func(t *testing.T) {
			r := newCodexCollabRestorer(codexCollabTestPlan(t))
			r.BeginResponse(1, 1)
			item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review the change"}`)
			done := codexCollabRestoreDone(t, r, item, 0)
			codexCollabConfirmDone(r, done)
			changed := codexCollabTestSet(t, item, "name", "send_message")
			switch mode {
			case "conflicting_ids":
				other := codexCollabTestSet(t, item, "id", "other_item")
				other = codexCollabTestSet(t, other, "call_id", "other_call")
				codexCollabRestoreDone(t, r, other, 1)
				changed = codexCollabTestSet(t, changed, "call_id", "other_call")
			case "unknown_id":
				changed = codexCollabTestSet(t, changed, "id", "unknown_item")
			case "only_index":
				changed = []byte(`{"type":"function_call","name":"send_message","namespace":"collaboration-xy","arguments":"{}"}`)
			case "duplicate_identity":
				changed = []byte(`{"type":"function_call","name":"send_message","name":"spawn_agent","namespace":"collaboration-xy","id":"item_1","arguments":"{}"}`)
			}
			var err error
			if mode == "duplicate_done" {
				_, err = r.SSEData("response.output_item.done", []byte(`{"output_index":0,"item":`+string(changed)+`}`))
			} else {
				_, err = r.SSEData("response.completed", []byte(`{"response":{"output":[`+string(changed)+`]}}`))
			}
			require.Error(t, err)
		})
	}
}

// The existing path accepts identical done frames; adding a new error here
// would violate success preservation. Conflicting executable identities remain
// rejected by the protective test above, even after delivery confirmation.
func TestCodexCollabRestorerIdenticalDoneCompatibility(t *testing.T) {
	r := newCodexCollabRestorer(codexCollabTestPlan(t))
	r.BeginResponse(1, 1)
	item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review the change"}`)
	first := codexCollabRestoreDone(t, r, item, 0)
	codexCollabConfirmDone(r, first)
	second := codexCollabRestoreDone(t, r, item, 0)
	require.Equal(t, first, second)
	require.Equal(t, 1, r.Stats().Calls)
	require.Equal(t, 1, r.Stats().MarkerAdded)
}

func TestCodexCollabRestorerConsistentSnapshotPreservesFormatting(t *testing.T) {
	r := newCodexCollabRestorer(codexCollabTestPlan(t))
	r.BeginResponse(1, 1)
	item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review the change"}`)
	done := codexCollabRestoreDone(t, r, item, 0)
	codexCollabConfirmDone(r, done)
	formatted := []byte(`{ "id": "item_1", "call_id": "call_1", "type": "function_call", "namespace": "collaboration-xy", "name": "spawn_agent", "arguments": "{\"message\":\"please review the change\"}" }`)
	body := []byte(`{ "output": [` + string(formatted) + `] }`)
	expected, err := newCodexCollabRestorer(codexCollabTestPlan(t)).ResponseJSON(body)
	require.NoError(t, err)
	out, err := r.ResponseJSON(body)
	require.NoError(t, err)
	require.Equal(t, expected, out)
	require.Zero(t, r.Stats().Contradicted)
}

func TestCodexCollabRestorerUnrelatedArgumentCorrelationCompatibility(t *testing.T) {
	r := newCodexCollabRestorer(codexCollabTestPlan(t))
	for _, index := range []int{0, 1} {
		frame := codexCollabTestSet(t, []byte(`{"type":"response.function_call_arguments.delta","name":"lookup","item_id":"bridge_item","encrypted_function_args":[]}`), "output_index", index)
		out, err := r.SSEData("response.function_call_arguments.delta", frame)
		require.NoError(t, err)
		require.Equal(t, frame, out)
	}
	item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review the change"}`)
	codexCollabRestoreDone(t, r, item, 0)
	other := codexCollabTestSet(t, item, "id", "second_item")
	other = codexCollabTestSet(t, other, "call_id", "second_call")
	codexCollabRestoreDone(t, r, other, 1)
	item = codexCollabTestSet(t, item, "item_id", "second_item")
	_, err := r.ResponseJSON([]byte(`{"output":[` + string(item) + `]}`))
	require.NoError(t, err)
}

func TestCodexCollabRestorerUnrelatedItemsRemainOpaque(t *testing.T) {
	for _, item := range []string{
		`{"type":"message","content":"first","content":"second"}`,
		`{"type":"function_call","namespace":"other","name":"lookup","arguments":"first","arguments":"second"}`,
		`{"type":"function_call","name":"lookup","arguments":"first","arguments":"second"}`,
	} {
		r := newCodexCollabRestorer(codexCollabTestPlan(t))
		body := []byte(`{ "output": [` + item + `] }`)
		out, err := r.ResponseJSON(body)
		require.NoError(t, err)
		require.Equal(t, body, out)
		require.Zero(t, r.Stats().Calls)
	}
	for _, item := range []string{
		`{"type":"function_call","namespace":"other","namespace":"collaboration-xy","name":"spawn_agent"}`,
		`{"type":"function_call","namespace":"other","name":"lookup","name":"collaboration-xy.spawn_agent"}`,
		`{"type":"message","type":"function_call","content":"first","content":"second"}`,
	} {
		_, err := newCodexCollabRestorer(codexCollabTestPlan(t)).ResponseJSON([]byte(`{"output":[` + item + `]}`))
		require.Error(t, err)
	}
	for _, body := range []string{
		`{"tools":[{"type":"function","name":"lookup","description":"first","description":"second"}]}`,
		`{"tool_choice":{"type":"function","name":"lookup","extra":1,"extra":2}}`,
	} {
		out, err := newCodexCollabRestorer(codexCollabTestPlan(t)).ResponseJSON([]byte(body))
		require.NoError(t, err)
		require.Equal(t, body, string(out))
	}
	for _, body := range []string{
		`{"tools":[{"type":"namespace","name":"other","tools":[{"type":"function","name":"collaboration-xy.spawn_agent"}]}]}`,
		`{"tool_choice":{"type":"allowed_tools","tools":[{"type":"function","name":"collaboration-xy.spawn_agent"}]}}`,
	} {
		out, err := newCodexCollabRestorer(codexCollabTestPlan(t)).ResponseJSON([]byte(body))
		require.NoError(t, err)
		require.NotContains(t, string(out), "collaboration-xy")
	}
}

func TestCodexCollabRestorerStrictVeto(t *testing.T) {
	for _, test := range []struct {
		name, message string
		strictVeto    bool
		alwaysVeto    bool
		wholeJSON     bool
	}{
		{name: "dot boundary", message: strings.Repeat("a", 63) + ".", strictVeto: true},
		{name: "colon boundary", message: strings.Repeat("a", 63) + ":", strictVeto: true},
		{name: "semicolon boundary", message: strings.Repeat("a", 63) + ";", strictVeto: true},
		{name: "below boundary", message: strings.Repeat("a", 62) + "."},
		{name: "whitespace", message: strings.Repeat("a.", 40) + " task"},
		{name: "unicode", message: strings.Repeat("文", 30)},
		{name: "known prefix", message: " gAAAA_cipher", alwaysVeto: true},
		{name: "existing opaque", message: strings.Repeat("a", 32), alwaysVeto: true},
		{name: "JSON object", message: `{"task":"` + strings.Repeat("a", 80) + `"}`, wholeJSON: true},
		{name: "JSON string", message: `"` + strings.Repeat("a.", 40) + `"`, wholeJSON: true},
	} {
		for _, enabled := range []bool{false, true} {
			t.Run(test.name+"/"+map[bool]string{false: "off", true: "on"}[enabled], func(t *testing.T) {
				settings := resilience.Defaults()
				settings.Enabled, settings.Collab.StrictMarkerVeto = true, enabled
				r := NewCollabResponseRestorer(codexCollabSharedTestPlan(), settings, 1, 1)
				args, err := json.Marshal(map[string]string{"message": test.message})
				require.NoError(t, err)
				item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", string(args))
				out := codexCollabTestSSE(t, r, "response.output_item.done", item)
				veto := test.alwaysVeto || enabled && test.strictVeto
				require.Equal(t, !veto, gjson.GetBytes(out, "encrypted_function_args").Exists())
				require.Equal(t, string(args), gjson.GetBytes(out, "arguments").String())
				// Inspecting the message is part of the switch: off, it costs nothing.
				require.Equal(t, enabled && test.wholeJSON, r.Stats().MsgJSON == 1)
				require.Equal(t, enabled && test.strictVeto, r.Stats().StrictVetoed == 1)
			})
		}
	}
}

func codexCollabSharedTestPlan() *CollabPlan {
	return &CollabPlan{HistoricalTools: map[string]struct{}{"spawn_agent": {}}, EffectiveTools: map[string]CollabToolState{
		"spawn_agent": {Available: true, Type: CollabToolTypeFunction, HasMessage: true, MessageMarkerRemoved: true},
	}}
}

func TestCodexCollabRestorerEffectivePlanAndFrozenSettings(t *testing.T) {
	for _, mode := range []string{"available", "historical", "removed", "custom", "no_message", "marker_kept", "extra_encrypted"} {
		t.Run(mode, func(t *testing.T) {
			plan := codexCollabSharedTestPlan()
			state := plan.EffectiveTools["spawn_agent"]
			switch mode {
			case "removed":
				state.Available = false
			case "custom":
				state.Type = CollabToolTypeCustom
			case "no_message":
				state.HasMessage = false
			case "marker_kept":
				state.MessageMarkerRemoved = false
			case "extra_encrypted":
				state.ExtraEncrypted = true
			}
			plan.EffectiveTools["spawn_agent"] = state
			if mode == "historical" {
				delete(plan.EffectiveTools, "spawn_agent")
			}
			r := NewCollabResponseRestorer(plan, resilience.Defaults(), 1, 1)
			plan.EffectiveTools["spawn_agent"] = CollabToolState{}
			item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review the change"}`)
			out := codexCollabTestSSE(t, r, "response.output_item.done", item)
			require.Equal(t, mode == "available", gjson.GetBytes(out, "encrypted_function_args").Exists())
		})
	}
	settings := resilience.Defaults()
	settings.Enabled, settings.Collab.StrictMarkerVeto = false, true
	r := NewCollabResponseRestorer(codexCollabSharedTestPlan(), settings, 1, 1)
	settings.Enabled = true
	require.False(t, r.strictMarkerVeto)
	strict := NewCollabResponseRestorer(codexCollabSharedTestPlan(), settings, 1, 1)
	settings.Collab.StrictMarkerVeto = false
	strict.BeginResponse(2, 2)
	require.True(t, strict.strictMarkerVeto)
	plan := codexCollabSharedTestPlan()
	plan.EffectiveTools["wait_agent"] = plan.EffectiveTools["spawn_agent"]
	nonMessage := NewCollabResponseRestorer(plan, settings, 1, 1)
	item := codexCollabTestItem(t, "wait_agent", "collaboration-xy", `{"message":"please wait"}`)
	out := codexCollabTestSSE(t, nonMessage, "response.output_item.done", item)
	require.False(t, gjson.GetBytes(out, "encrypted_function_args").Exists())
}
