//go:build unit

package service

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexCollabRestorerNonExecutableDelivery(t *testing.T) {
	for _, executable := range []bool{false, true} {
		r := newCodexCollabRestorer(codexCollabTestPlan(t))
		r.BeginResponse(1, 1)
		item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review the change"}`)
		codexCollabTestSSE(t, r, "response.output_item.added", item)
		message := codexCollabTestSet(t, item, "type", "message")
		message = codexCollabTestSet(t, message, "namespace", "collaboration")
		done := codexCollabRestoreDone(t, r, message, 0)
		hooks := &resilience.CollabDeliveryHooks{}
		hooks.Register(r)
		hooks.Written(context.Background(), resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1,
			EventType: "response.output_item.done", Executable: executable, Successful: true, BytesWritten: len(done)}, done)
		out, err := r.ResponseJSON([]byte(`{"output":[` + string(item) + `]}`))
		require.NoError(t, err)
		require.Equal(t, "function_call", gjson.GetBytes(out, "output.0.type").String())
		require.Equal(t, "[]", gjson.GetBytes(out, "output.0.encrypted_function_args").Raw)
	}
}

func TestCodexCollabRestorerDeliveredMarkerSurvivesLateEvidence(t *testing.T) {
	r := newCodexCollabRestorer(codexCollabTestPlan(t))
	r.BeginResponse(1, 1)
	item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review the change"}`)
	done := codexCollabRestoreDone(t, r, item, 0)
	codexCollabConfirmDone(r, done)
	_, err := r.SSEData("response.function_call_arguments.delta", []byte(`{"type":"response.function_call_arguments.delta","item_id":"item_1","encrypted_function_args":["message"]}`))
	require.NoError(t, err)
	out, err := r.ResponseJSON([]byte(`{"output":[` + string(item) + `]}`))
	require.NoError(t, err)
	require.Equal(t, gjson.GetBytes(done, "item").Raw, gjson.GetBytes(out, "output.0").Raw)
}

func TestCodexCollabRestorerRestoredMeaningPreservesBytes(t *testing.T) {
	for _, name := range []string{"spawn_agent", "collaboration-xy.spawn_agent", "collaboration-xy__spawn_agent"} {
		for _, marker := range []string{"", `, "encrypted_function_args": []`} {
			r := newCodexCollabRestorer(codexCollabTestPlan(t))
			r.BeginResponse(1, 1)
			item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review the change"}`)
			done := codexCollabRestoreDone(t, r, item, 0)
			codexCollabConfirmDone(r, done)
			body := []byte(`{ "output": [{ "id": "item_1", "call_id": "call_1", "type": "function_call", "namespace": "collaboration-xy", "name": "` + name + `", "arguments": "{\"message\":\"please review the change\"}"` + marker + ` }] }`)
			legacy := newCodexCollabRestorer(codexCollabTestPlan(t))
			codexCollabRestoreDone(t, legacy, item, 0)
			expected, err := legacy.ResponseJSON(body)
			require.NoError(t, err)
			out, err := r.ResponseJSON(body)
			require.NoError(t, err)
			require.Equal(t, string(expected), string(out), name+marker)
		}
	}
}

func TestCodexCollabRestorerSnapshotUncertainMeaningFallsBack(t *testing.T) {
	for _, arguments := range []string{
		`{ "message": "please review the change" }`,
		`{"message":"first","message":"second"}`,
		`invalid`,
	} {
		r := newCodexCollabRestorer(codexCollabTestPlan(t))
		r.BeginResponse(1, 1)
		legacy := newCodexCollabRestorer(codexCollabTestPlan(t))
		item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review the change"}`)
		done := codexCollabRestoreDone(t, r, item, 0)
		codexCollabConfirmDone(r, done)
		codexCollabRestoreDone(t, legacy, item, 0)
		changed := codexCollabTestSet(t, item, "arguments", arguments)
		body := []byte(`{"output":[` + string(changed) + `]}`)
		expected, err := legacy.ResponseJSON(body)
		require.NoError(t, err)
		out, err := r.ResponseJSON(body)
		require.NoError(t, err)
		require.Equal(t, string(expected), string(out))
	}
}

func TestCodexCollabRestorerLargeSnapshotFallsBack(t *testing.T) {
	r := newCodexCollabRestorer(codexCollabTestPlan(t))
	r.BeginResponse(1, 1)
	item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"gAAAA_`+strings.Repeat("a", 1<<20)+`"}`)
	item = codexCollabTestSet(t, item, "encrypted_function_args", []string{"message"})
	done := codexCollabRestoreDone(t, r, item, 0)
	codexCollabConfirmDone(r, done)
	state := r.items["item:item_1"]
	require.Zero(t, len(state.deliveredSnapshot), "large calls must not retain another payload")
	require.Zero(t, state.pendingBytes)
	changed := codexCollabTestSet(t, item, "name", "send_message")
	_, err := r.ResponseJSON([]byte(`{"output":[` + string(changed) + `]}`))
	require.ErrorContains(t, err, "identity changed")
}

func TestCodexCollabRestorerSnapshotBudget(t *testing.T) {
	r := newCodexCollabRestorer(codexCollabTestPlan(t))
	r.BeginResponse(1, 1)
	var last []byte
	for i := range 24 {
		item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"gAAAA_`+strings.Repeat("a", 60<<10)+`"}`)
		item = codexCollabTestSet(t, item, "id", fmt.Sprintf("item_%d", i))
		item = codexCollabTestSet(t, item, "call_id", fmt.Sprintf("call_%d", i))
		item = codexCollabTestSet(t, item, "encrypted_function_args", []string{"message"})
		done := codexCollabRestoreDone(t, r, item, i)
		codexCollabConfirmDone(r, done)
		last = item
	}
	require.Positive(t, r.snapshotBytes)
	require.LessOrEqual(t, r.snapshotBytes, codexCollabSnapshotBudget)
	state := r.items["item:item_23"]
	require.Zero(t, len(state.deliveredSnapshot))
	last = codexCollabTestSet(t, last, "name", "send_message")
	_, err := r.SSEData("response.output_item.done", []byte(`{"output_index":23,"item":`+string(last)+`}`))
	require.ErrorContains(t, err, "identity changed")
	r.BeginResponse(2, 2)
	require.Zero(t, r.snapshotBytes)
}

func TestCodexCollabRestorerHookExecutableIdentity(t *testing.T) {
	for _, mode := range []string{"function", "custom", "non_executable", "unknown", "wrong_generation", "wrong_send"} {
		t.Run(mode, func(t *testing.T) {
			r := newCodexCollabRestorer(codexCollabTestPlan(t))
			r.BeginResponse(1, 1)
			item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review the change"}`)
			if mode == "custom" {
				item = codexCollabTestSet(t, item, "type", "custom_tool_call")
			}
			if mode == "unknown" {
				item = codexCollabTestSet(t, item, "name", "unknown")
			}
			done := codexCollabRestoreDone(t, r, item, 0)
			event := resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1, EventType: "response.output_item.done",
				Executable: mode != "non_executable", Successful: true, BytesWritten: len(done)}
			if mode == "wrong_generation" {
				event.Generation = 2
			}
			if mode == "wrong_send" {
				event.SendSeq = 2
			}
			hooks := &resilience.CollabDeliveryHooks{}
			hooks.Register(r)
			hooks.Received(context.Background(), event, done)
			hooks.AboutToWrite(context.Background(), event, done)
			require.Zero(t, r.snapshotBytes)
			hooks.Written(context.Background(), event, done)
			require.Equal(t, mode == "function" || mode == "custom", r.snapshotBytes > 0)
			stored := strings.Clone(r.items["item:item_1"].deliveredSnapshot)
			clear(done)
			require.Equal(t, stored, r.items["item:item_1"].deliveredSnapshot)
		})
	}
}

func BenchmarkCodexCollabRestorerUnrelatedImage(b *testing.B) {
	_, plan, _ := planCodexCollaborationPlaintext(codexCollabTestRequest())
	frame := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"image_generation_call","id":"image_item","result":"` + strings.Repeat("a", 1<<20) + `"}}`)
	for _, enabled := range []bool{false, true} {
		b.Run(fmt.Sprintf("generation_%t", enabled), func(b *testing.B) {
			r := newCodexCollabRestorer(plan)
			if enabled {
				r.BeginResponse(1, 1)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(frame)))
			b.ResetTimer()
			for b.Loop() {
				if _, err := r.SSEData("response.output_item.done", frame); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCodexCollabRestorerEncryptedRetention(b *testing.B) {
	_, plan, _ := planCodexCollaborationPlaintext(codexCollabTestRequest())
	for _, enabled := range []bool{false, true} {
		b.Run(fmt.Sprintf("generation_%t", enabled), func(b *testing.B) {
			var retained uint64
			for range b.N {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				r := newCodexCollabRestorer(plan)
				if enabled {
					r.BeginResponse(1, 1)
				}
				for i := range 24 {
					frame := []byte(fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":{"type":"function_call","id":"item_%d","call_id":"call_%d","name":"spawn_agent","namespace":"collaboration-xy","encrypted_function_args":["message"],"arguments":"{\"message\":\"gAAAA_%s\"}"}}`, i, i, i, strings.Repeat("a", 1<<20)))
					if _, err := r.SSEData("response.output_item.done", frame); err != nil {
						b.Fatal(err)
					}
				}
				runtime.GC()
				runtime.ReadMemStats(&after)
				retained += after.HeapAlloc - before.HeapAlloc
				runtime.KeepAlive(r)
			}
			b.ReportMetric(float64(retained)/float64(b.N), "retained-B/op")
		})
	}
}
