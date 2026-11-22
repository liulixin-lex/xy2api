//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func codexCollabLargeOpaqueFrames() []struct {
	name, event string
	body        []byte
} {
	large := strings.Repeat("a", 1<<20)
	return []struct {
		name, event string
		body        []byte
	}{
		{"image", "response.output_item.done", []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"image_generation_call","id":"image_item","result":"` + large + `"}}`)},
		{"arguments", "response.function_call_arguments.delta", []byte(`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"call_item","delta":"` + large + `"}`)},
	}
}

func TestCodexCollabRestorerOpaqueFrameValidation(t *testing.T) {
	for _, frame := range codexCollabLargeOpaqueFrames() {
		t.Run(frame.name, func(t *testing.T) {
			r := newCodexCollabRestorer(codexCollabTestPlan(t))
			out, err := r.SSEData(frame.event, frame.body)
			require.NoError(t, err)
			require.Equal(t, frame.body, out)
			require.Empty(t, r.items)
		})
	}
	for _, frame := range []string{
		`{"type":"response.function_call_arguments.delta","item_id":"first","item_id":"second","delta":"unchanged"}`,
		`{"type":"response.function_call_arguments.delta","delta":"first","delta":"second"}`,
		`{"type":"response.output_item.done","item":{"type":"image_generation_call","type":"function_call","result":"unchanged"}}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","namespace":"other","namespace":"collaboration-xy","name":"spawn_agent"}}`,
	} {
		out, err := newCodexCollabRestorer(codexCollabTestPlan(t)).SSEData("", []byte(frame))
		require.Error(t, err)
		require.Empty(t, out)
	}
	for _, item := range []string{
		`{"type":"image_generation_call","result":"first","result":"second"}`,
		`{"result":"first","result":"second","type":"image_generation_call"}`,
		`{"type":"function_call","name":"lookup","arguments":"first","arguments":"second"}`,
	} {
		frame := []byte(`{"type":"response.output_item.done","item":` + item + `}`)
		out, err := newCodexCollabRestorer(codexCollabTestPlan(t)).SSEData("", frame)
		require.NoError(t, err)
		require.Equal(t, frame, out)
	}
}

func BenchmarkCodexCollabRestorerOpaqueFramesNoHooks(b *testing.B) {
	_, plan, _ := planCodexCollaborationPlaintext(codexCollabTestRequest())
	for _, frame := range codexCollabLargeOpaqueFrames() {
		b.Run(frame.name, func(b *testing.B) {
			r := newCodexCollabRestorer(plan)
			b.ReportAllocs()
			b.SetBytes(int64(len(frame.body)))
			b.ResetTimer()
			for b.Loop() {
				if _, err := r.SSEData(frame.event, frame.body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
