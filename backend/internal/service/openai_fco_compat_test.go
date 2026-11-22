package service

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFCOCompatRulesGolden(t *testing.T) {
	// Golden names and page rules are ported from the upstream snapshot at
	// commit 328db26f4.
	rules := DefaultOpenAIFCORules([]string{"custom_name", "browser_info", "bad-name"})
	require.Equal(t, []string{"custom_name"}, rules.AdminNames)
	body := []byte(`{"model":"m","metadata":{"large":9007199254740993},"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"  browser  "},{"type":"function_call_output","namespace":"agent","name":"terminal_info","call_id":null,"output":"term"},{"type":"function_call_output","namespace":"agent","name":"custom_name","output":" custom "}]}`)
	got, summary, err := normalizeOpenAIFCOBody(body, rules)
	require.NoError(t, err)
	require.Equal(t, 3, summary.Matched)
	require.Contains(t, string(got), `external_browser_info`)
	require.Contains(t, string(got), `external_terminal_info`)
	require.Contains(t, string(got), `external_custom_name`)
	require.Contains(t, string(got), "9007199254740993")
}

func TestFCOCompatPreservesUnchangedItemBytes(t *testing.T) {
	rules := DefaultOpenAIFCORules(nil)
	body := []byte("{\n  \"metadata\" : { \"n\" : 9007199254740993 },\n  \"input\" : [ { \"type\" : \"message\", \"content\" : \"keep\" }, { \"type\" : \"function_call_output\", \"namespace\" : \"agent\", \"name\" : \"browser_info\", \"output\" : \"x\" } ]\n}")
	got, _, err := normalizeOpenAIFCOBody(body, rules)
	require.NoError(t, err)
	require.Contains(t, string(got), `"type" : "message", "content" : "keep"`)
	require.Contains(t, string(got), "9007199254740993")
}

func TestFCOCompatIdempotentAndRejectsShapes(t *testing.T) {
	rules := DefaultOpenAIFCORules(nil)
	for _, body := range [][]byte{
		[]byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","call_id":"x","output":"ok"}]}`),
		[]byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":[]}]}`),
		[]byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"unknown","output":"ok"}]}`),
		[]byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"one","output":"two"}]}`),
	} {
		got, _, err := normalizeOpenAIFCOBody(body, rules)
		require.NoError(t, err)
		require.Equal(t, string(body), string(got))
	}
	body := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"ok"}]}`)
	one, _, _ := normalizeOpenAIFCOBody(body, rules)
	two, _, _ := normalizeOpenAIFCOBody(one, rules)
	require.Equal(t, string(one), string(two))
	nullItem := gjson.Parse(`{"type":"function_call_output","namespace":"agent","name":"browser_info","call_id":null,"output":"x"}`)
	match, ok := matchOpenAIFCOItem(nullItem, rules)
	require.True(t, ok)
	require.Equal(t, "ext", match.Layer)
}

func TestFCOCompatSummaryBuckets(t *testing.T) {
	s := summarizeOpenAIFCOBody([]byte(`{"previous_response_id":null,"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"x"},{"type":"function_call","call_id":"c","name":"f"},{"type":"function_call_output","namespace":"agent","name":"x","call_id":null,"output":[]}]}`), DefaultOpenAIFCORules(nil))
	require.Equal(t, "2", s.IndependentFCOCount)
	require.Equal(t, "1", s.CallsWithCallID)
	require.False(t, s.PreviousResponseID)
}

func TestFCORefusalSignatures(t *testing.T) {
	for _, tc := range []struct {
		status       int
		msg, variant string
		want         bool
	}{
		{400, "function_call_output requires call_id on HTTP requests; continuation via previous_response_id is only supported on Responses WebSocket v2", "complete", true},
		{400, "function_call_output requires call_id on HTTP requests", "truncated", true},
		{400, "function_call_output requires call_id on HTTP requests; unpaired tool outputs are only accepted on Responses WebSocket v2", "unpaired", true},
		{400, "function_call_output requires call_id on HTTP requests: arbitrary", "", false},
		{500, "function_call_output requires call_id on HTTP requests", "", false},
	} {
		got := classifyOpenAIFCORefusal(tc.status, []byte(`{"error":{"message":"`+tc.msg+`"}}`))
		require.Equal(t, tc.want, got.Matched)
		require.Equal(t, tc.variant, got.Variant)
	}
}

// Cases adapted from upstream commit 328db26f4, including deliberate call_id differences.
func TestFCOCompatGoldenTemplatesAndLayers(t *testing.T) {
	rules := DefaultOpenAIFCORules([]string{"custom_name"})
	cases := []struct {
		name, output, layer, version string
		wrapped                      bool
	}{
		{"browser_info", " \n雪 </external_browser_info> \"\\\t& ", "core", fcoCoreVersion, false},
		{"goal_objective", " \n雪 </external_goal_objective> \"\\\t& ", "core", fcoCoreVersion, false},
		{"untrusted_context", " \n雪 </external_untrusted_context> \"\\\t& ", "core", fcoCoreVersion, false},
		{"ide_context", " \n雪 </external_ide_context> \"\\\t& ", "core", fcoCoreVersion, false},
		{"untrusted_content", " \n雪 </external_untrusted_content> \"\\\t& ", "core", fcoCoreVersion, false},
		{"proposed_plan", " \n雪 </external_proposed_plan> \"\\\t& ", "core", fcoCoreVersion, false},
		{"codex_apps_open_page", " {\"page_id\":null,\"text\":\"雪 & < >\"} ", "core", fcoCoreVersion, true},
		{"codex_apps_selected_page_part_1_of_2", "Companion Page snapshot part 1 of 2:\n雪 & < >\n", "core", fcoCoreVersion, true},
		{"terminal_info", " \n雪 </external_terminal_info> \"\\\t& ", "ext", fcoExtVersion, false},
		{"browser_tabs", " \n雪 </external_browser_tabs> \"\\\t& ", "ext", fcoExtVersion, false},
		{"custom_name", " \n雪 </external_custom_name> \"\\\t& ", "admin", fcoAdminVersion, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item, err := json.Marshal(map[string]any{"type": "function_call_output", "namespace": "agent", "name": tc.name, "output": tc.output})
			require.NoError(t, err)
			body := append(append([]byte(`{"input":[`), item...), []byte(`]}`)...)
			match, ok := matchOpenAIFCOItem(gjson.ParseBytes(item), rules)
			require.True(t, ok)
			require.Equal(t, tc.layer, match.Layer)
			require.Equal(t, tc.version, match.Version)
			text := "<external_" + tc.name + ">" + tc.output + "</external_" + tc.name + ">"
			if tc.wrapped {
				text = "<external_" + tc.name + ">\n" + tc.output + "\n</external_" + tc.name + ">"
			}
			quoted, err := json.Marshal(text)
			require.NoError(t, err)
			expected := `{"input":[{"content":[{"text":` + string(quoted) + `,"type":"input_text"}],"role":"user","type":"message"}]}`
			got, summary, err := normalizeOpenAIFCOBody(body, rules)
			require.NoError(t, err)
			require.Equal(t, expected, string(got))
			require.Equal(t, 1, summary.Matched)
			again, _, err := normalizeOpenAIFCOBody(got, rules)
			require.NoError(t, err)
			require.Equal(t, got, again)
		})
	}
	match, ok := matchOpenAIFCOItem(gjson.Parse(`{"type":"function_call_output","namespace":"agent","name":"browser_info","call_id":null,"output":"x"}`), rules)
	require.True(t, ok)
	require.Equal(t, "ext", match.Layer)
	require.Equal(t, fcoExtVersion, match.Version)
}

func TestFCOCompatRegressionShapes(t *testing.T) {
	rules := DefaultOpenAIFCORules(nil)
	item := `[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"x"}]`
	body := []byte(`{"shadow":` + item + `,"nested":{"input":` + item + `},"input":` + item + `,"after":` + item + `,"string":"` + strings.ReplaceAll(item, `"`, `\"`) + `"}`)
	got, _, err := normalizeOpenAIFCOBody(body, rules)
	require.NoError(t, err)
	require.Equal(t, item, gjson.GetBytes(got, "shadow").Raw)
	require.Equal(t, item, gjson.GetBytes(got, "after").Raw)
	require.Equal(t, "message", gjson.GetBytes(got, "input.0.type").String())
	require.Equal(t, item, gjson.GetBytes(got, "nested.input").Raw)
	again, _, err := normalizeOpenAIFCOBody(got, rules)
	require.NoError(t, err)
	require.Equal(t, got, again)
	for _, raw := range []string{`["function_call_output"]`, `"function_call_output"`, `{"input":[{"type":"function_call_output","namespace":"agent","name":"codex_apps_selected_page_part_1_of_99999999999999999999","output":"Companion Page snapshot part 1 of 9223372036854775807:\nx"}]}`} {
		got, _, err := normalizeOpenAIFCOBody([]byte(raw), rules)
		require.NoError(t, err)
		require.Equal(t, raw, string(got))
	}
	for _, previous := range []string{`null`, `""`, `"   "`, `42`} {
		s := summarizeOpenAIFCOBody([]byte(`{"previous_response_id":`+previous+`,"input":`+item+`}`), rules)
		require.False(t, s.PreviousResponseID)
	}
	s := summarizeOpenAIFCOBody([]byte(`{"previous_response_id":" x ","input":`+item+`}`), rules)
	require.True(t, s.PreviousResponseID)
	page := []byte(`{"input":[{"type":"function_call_output","namespace":"agent","name":"codex_apps_selected_page_part_1_of_2","output":"Companion Page snapshot part 1 of 2:\nx"}]}`)
	_, summary, err := normalizeOpenAIFCOBody(page, rules)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"codex_apps_selected_page_part": 1}, summary.Names)
	require.Equal(t, []string{"codex_apps_selected_page_part"}, summarizeOpenAIFCOBody(page, rules).Names)
}

func TestFCOCompatUpstreamNegativeCases(t *testing.T) {
	rules := DefaultOpenAIFCORules(nil)
	names := []string{"browser_info", "goal_objective", "untrusted_context", "untrusted_content", "ide_context", "proposed_plan"}
	for _, name := range names {
		for _, override := range []map[string]any{{"call_id": "real"}, {"call_id": 42}, {"call_id": ""}, {"call_id": " \t\n"}, {"namespace": "other"}, {"namespace": nil}, {"namespace": 42}, {"output": nil}, {"output": []any{}}} {
			item := map[string]any{"type": "function_call_output", "namespace": "agent", "name": name, "output": "context"}
			for k, v := range override {
				item[k] = v
			}
			body, err := json.Marshal(map[string]any{"input": []any{item}})
			require.NoError(t, err)
			got, _, err := normalizeOpenAIFCOBody(body, rules)
			require.NoError(t, err)
			require.Equal(t, body, got)
		}
		for _, key := range []string{"namespace", "output"} {
			item := map[string]any{"type": "function_call_output", "namespace": "agent", "name": name, "output": "context"}
			delete(item, key)
			body, _ := json.Marshal(map[string]any{"input": []any{item}})
			got, _, err := normalizeOpenAIFCOBody(body, rules)
			require.NoError(t, err)
			require.Equal(t, body, got)
		}
		for _, output := range []string{"", " \t"} {
			item := map[string]any{"type": "function_call_output", "namespace": "agent", "name": name, "output": output}
			body, _ := json.Marshal(map[string]any{"input": []any{item}})
			got, summary, err := normalizeOpenAIFCOBody(body, rules)
			require.NoError(t, err)
			want := name == "untrusted_content" || (name == "proposed_plan" && output != "")
			require.Equal(t, want, summary.Matched > 0)
			if !want {
				require.Equal(t, body, got)
			}
		}
	}
	for _, raw := range []string{`{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"x","output":"y"}]}`, `{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"x"}]} trailing`} {
		got, _, err := normalizeOpenAIFCOBody([]byte(raw), rules)
		require.NoError(t, err)
		require.Equal(t, raw, string(got))
	}
}

func TestFCORefusalConflictingFields(t *testing.T) {
	prefix := fcoRefusalUnpairedPrefix
	for _, tc := range []struct {
		payload string
		want    bool
	}{
		{`{"error":{"message":"` + prefix + ` client advice"}}`, true},
		{`{"error":{"message":"` + fcoRefusalPrefix + ` advice"}}`, false},
		{`{"error":{"message":"function_call_output requires item_reference ids matching each call_id on HTTP requests"}}`, false},
		{`not json`, false},
		{`{"error":{"message":"` + fcoRefusalPrefix + `","code":"different"}}`, true},
		{`{"error":{"message":"` + fcoRefusalPrefix + `","param":"input"}}`, true},
		{`{"error":{"message":"` + fcoRefusalPrefix + `","code":null,"param":""}}`, true},
	} {
		require.Equal(t, tc.want, classifyOpenAIFCORefusal(400, []byte(tc.payload)).Matched, tc.payload)
	}
}

func TestFCOObservationDoesNotAppendOpsEvents(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	first := appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "http_error", Message: "existing"})
	payload := []byte(`{"error":{"message":"` + fcoRefusalPrefix + `"}}`)
	observeOpenAIFCOHTTPError(context.Background(), c, []byte(`{"input":[{"type":"function_call_output"}]}`), payload, 400, DefaultOpenAIFCORules(nil))
	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Same(t, first, events[0])
}

func TestFCOSuccessFastPath(t *testing.T) {
	body := []byte(`{"input":"hello"}`)
	rules := DefaultOpenAIFCORules(nil)
	before := fcoSuccessSampleCounter.Load()
	allocs := testing.AllocsPerRun(100, func() { observeOpenAIFCOSuccess(context.Background(), body, rules) })
	require.Zero(t, allocs)
	require.Equal(t, before, fcoSuccessSampleCounter.Load())
}

func TestFCOMigrationClassification(t *testing.T) {
	require.True(t, schedMigrationMessage(fcoRefusalUnpairedPrefix+" advice"))
	require.False(t, schedMigrationMessage("relay-x: insufficient balance"))
}

// Page context cases adapted from upstream commit 328db26f4.
func TestFCOPageContextUpstreamCases(t *testing.T) {
	rules := DefaultOpenAIFCORules(nil)
	valid := map[string]any{"type": "function_call_output", "namespace": "agent", "name": "codex_apps_open_page", "output": `{"page_id":null}`}
	cases := []struct {
		key   string
		value any
	}{
		{"call_id", "real"}, {"call_id", 42}, {"namespace", "codex_app"}, {"namespace", ""}, {"name", "read_file"}, {"type", "custom_tool_call_output"},
		{"output", "some text"}, {"output", `{"other":null}`}, {"output", `{"page_id":1}`}, {"output", `{"page_id":null,"page_id":"other"}`},
		{"name", "codex_apps_selected_page_part_1_of_1"}, {"name", "codex_apps_selected_page_part_2_of_1"},
	}
	for _, tc := range cases {
		item := map[string]any{}
		for k, v := range valid {
			item[k] = v
		}
		item[tc.key] = tc.value
		body, _ := json.Marshal(map[string]any{"input": []any{item}})
		got, _, err := normalizeOpenAIFCOBody(body, rules)
		require.NoError(t, err)
		require.Equal(t, body, got)
	}
	items := []any{valid, map[string]any{"type": "function_call_output", "namespace": "agent", "name": "codex_apps_selected_page_part_1_of_2", "output": "Companion Page snapshot part 1 of 2:\n{\"page_id\":\"page1\",\"document_view\":\"hello"}, map[string]any{"type": "function_call_output", "namespace": "agent", "name": "codex_apps_selected_page_part_2_of_2", "output": "Companion Page snapshot part 2 of 2:\n world\"}"}, map[string]any{"role": "user", "content": "hello"}}
	body, _ := json.Marshal(map[string]any{"input": items})
	got, summary, err := normalizeOpenAIFCOBody(body, rules)
	require.NoError(t, err)
	require.Equal(t, 3, summary.Matched)
	require.Equal(t, "hello", gjson.GetBytes(got, "input.3.content").String())
	again, _, err := normalizeOpenAIFCOBody(got, rules)
	require.NoError(t, err)
	require.Equal(t, got, again)
	chunks := []string{"# Plan\n", " \n\t", "1. Run tests."}
	for _, chunk := range chunks {
		item := map[string]any{"type": "function_call_output", "namespace": "agent", "name": "proposed_plan", "output": chunk}
		body, _ := json.Marshal(map[string]any{"input": []any{item}})
		got, summary, err := normalizeOpenAIFCOBody(body, rules)
		require.NoError(t, err)
		require.Equal(t, 1, summary.Matched)
		require.Equal(t, "<external_proposed_plan>"+chunk+"</external_proposed_plan>", gjson.GetBytes(got, "input.0.content.0.text").String())
		again, _, err := normalizeOpenAIFCOBody(got, rules)
		require.NoError(t, err)
		require.Equal(t, got, again)
	}
}

func TestFCORefusalShapeWithNoIndependentOutputs(t *testing.T) {
	cases := []struct{ name, input, shape string }{
		{"empty", `[{"type":"function_call_output","namespace":"agent","name":"browser_info","call_id":"","output":"x"}]`, "empty"},
		{"whitespace", `[{"type":"function_call_output","namespace":"agent","name":"browser_info","call_id":" \t","output":"x"}]`, "whitespace"},
		{"other output missing id", `[{"type":"function_call_output","namespace":"agent","name":"browser_info","call_id":"call_1","output":"x"},{"type":"custom_tool_call_output","output":"x"}]`, "string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"previous_response_id":"resp","input":` + tc.input + `}`)
			summary := observeOpenAIFCOWire(body, DefaultOpenAIFCORules(nil))
			require.Equal(t, "0", summary.IndependentFCOCount)
			require.Equal(t, []string{tc.shape}, summary.CallIDShapes)
			require.Equal(t, []string{"core"}, summary.Layers)
			require.Equal(t, []string{"browser_info"}, summary.Names)
			require.Equal(t, []string{"string"}, summary.OutputShapes)
			require.True(t, summary.PreviousResponseID)
			fast := summarizeOpenAIFCOBodyMode(body, DefaultOpenAIFCORules(nil), false)
			require.Equal(t, "0", fast.IndependentFCOCount)
			require.Empty(t, fast.CallIDShapes)
		})
	}
}

func TestFCORefusalParamAndSuffixBoundaries(t *testing.T) {
	for _, tc := range []struct {
		field string
		want  bool
	}{
		{`"code":"invalid_request_error"`, true}, {`"code":0`, true}, {`"code":{"unexpected":true}`, true},
		{`"param":"input"`, true}, {`"param":"input[0].call_id"`, true}, {`"param":"model"`, false}, {`"param":""`, true}, {`"param":42`, true}, {`"param":null`, true},
	} {
		payload := []byte(`{"error":{"message":"` + fcoRefusalPrefix + `",` + tc.field + `}}`)
		require.Equal(t, tc.want, classifyOpenAIFCORefusal(400, payload).Matched, tc.field)
	}
	for _, tc := range []struct {
		suffix string
		want   bool
	}{{"", true}, {". advice", true}, {"; advice", true}, {", advice", true}, {": advice", true}, {" advice", true}, {"\tadvice", true}, {"X", false}, {"2", false}, {"-advice", false}} {
		payload, _ := json.Marshal(map[string]any{"error": map[string]any{"message": fcoRefusalUnpairedPrefix + tc.suffix}})
		require.Equal(t, tc.want, classifyOpenAIFCORefusal(400, payload).Matched, tc.suffix)
	}
}
