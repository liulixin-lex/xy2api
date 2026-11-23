//go:build unit

package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexCollabRestorerEquivalentNumbersPreserveBytes(t *testing.T) {
	for _, test := range []struct{ first, last string }{
		{"1", "1.0"}, {"1.0", "1e0"}, {"1e0", "1"}, {"0", "-0.0"},
		{"9007199254740993", "9007199254740993.0"},
		{"1e99999999999999999999", "10e99999999999999999998"},
	} {
		for _, location := range []string{"item", "arguments"} {
			t.Run(location+"/"+test.first+"/"+test.last, func(t *testing.T) {
				first := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"please review"}`)
				last := append([]byte(nil), first...)
				if location == "item" {
					first = codexCollabTestSet(t, first, "metadata", json.RawMessage(`{"values":[`+test.first+`]}`))
					last = codexCollabTestSet(t, last, "metadata", json.RawMessage(`{"values":[`+test.last+`]}`))
				} else {
					first = codexCollabTestSet(t, first, "arguments", `{"message":"please review","values":[`+test.first+`]}`)
					last = codexCollabTestSet(t, last, "arguments", `{"message":"please review","values":[`+test.last+`]}`)
				}
				r := newCodexCollabRestorer(codexCollabTestPlan(t))
				r.BeginResponse(1, 1)
				codexCollabConfirmDone(r, codexCollabRestoreDone(t, r, first, 0))
				legacy := newCodexCollabRestorer(codexCollabTestPlan(t))
				codexCollabRestoreDone(t, legacy, first, 0)
				body := []byte(`{ "output": [` + string(last) + `] }`)
				expected, err := legacy.ResponseJSON(body)
				require.NoError(t, err)
				out, err := r.ResponseJSON(body)
				require.NoError(t, err)
				require.Equal(t, string(expected), string(out))
			})
		}
	}
}

func TestCodexCollabRestorerVetoSkipsArgumentInspection(t *testing.T) {
	item := codexCollabTestItem(t, "spawn_agent", "collaboration-xy", `{"message":"{\"task\":\"review\"}","padding":"`+strings.Repeat("a", 1<<20)+`"}`)
	for _, mode := range []string{"nonempty", "empty", "history", "anonymous", "extra_encrypted"} {
		t.Run(mode, func(t *testing.T) {
			r := newCodexCollabRestorer(codexCollabTestPlan(t))
			r.strictMarkerVeto = true // the only mode that inspects the message
			state := &codexCollabItemState{tool: "spawn_agent"}
			body := item
			switch mode {
			case "nonempty":
				state.sawNonEmpty = true
			case "empty":
				body = codexCollabTestSet(t, body, "encrypted_function_args", []string{})
			case "history":
				state.decision = codexCollabMarkerVeto
			case "anonymous":
				state.anonymous = true
			case "extra_encrypted":
				r.plan.extraEncrypted["spawn_agent"] = true
			}
			out, err := r.patchFunctionItem(body, codexCollabItem{result: gjson.ParseBytes(body)}, state)
			require.NoError(t, err)
			require.Equal(t, body, out)
			require.Zero(t, r.Stats().MsgJSON, "count-only inspection must follow established veto checks")
		})
	}
}

func TestCodexCollabRestorerExactNumericComparison(t *testing.T) {
	for _, test := range []struct {
		left, right   string
		same, certain bool
	}{
		{"1000", "1e3", true, true},
		{"0.0010", "1e-3", true, true},
		{"-12.50", "-125e-1", true, true},
		{"9007199254740992", "9007199254740993", false, true},
		{"1", "2", false, true},
		{"1e99999999999999999999", "10e99999999999999999998", false, false},
	} {
		same, certain := codexCollabSameRestoredMeaning(`{"values":[`+test.left+`]}`, `{"values":[`+test.right+`]}`, false)
		require.Equal(t, test.certain, certain, test.left+"/"+test.right)
		if certain {
			require.Equal(t, test.same, same, test.left+"/"+test.right)
		}
	}
}

func BenchmarkCodexCollabRestorerEncryptedDoneNoHooks(b *testing.B) {
	_, plan, _ := planCodexCollaborationPlaintext(codexCollabTestRequest())
	frame := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"spawn_agent","namespace":"collaboration-xy","encrypted_function_args":["message"],"arguments":"{\"message\":\"gAAAA_` + strings.Repeat("a", 1<<20) + `\"}"}}`)
	r := newCodexCollabRestorer(plan)
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for b.Loop() {
		if _, err := r.SSEData("response.output_item.done", frame); err != nil {
			b.Fatal(err)
		}
	}
}
