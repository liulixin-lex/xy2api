package fcorelay

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVersionMatrix(t *testing.T) {
	fco := func(name, extra string) string {
		return `{"type":"function_call_output","namespace":"agent","name":"` + name + `","output":"x"` + extra + `}`
	}
	call := `{"type":"function_call","call_id":"c"}`
	custom := `{"type":"custom_tool_call","call_id":"c"}`
	cases := []struct {
		name, input string
		want        [3]int
	}{
		{"A", fco("browser_info", ""), [3]int{400, 200, 200}},
		{"B", fco("browser_info", "") + "," + call, [3]int{200, 200, 200}},
		{"C", fco("terminal_info", ""), [3]int{400, 400, 400}},
		{"D", fco("browser_info", `,"call_id":null`), [3]int{400, 400, 400}},
		{"E", `{"type":"message","role":"user","content":"<external_browser_info>x</external_browser_info>"}`, [3]int{200, 200, 200}},
		{"F", `{"type":"function_call_output","output":"x"}`, [3]int{400, 400, 400}},
		{"G", `{"type":"function_call_output","output":"x"},` + call, [3]int{200, 200, 200}},
		{"H", fco("unknown", ""), [3]int{400, 400, 400}},
		{"I", fco("browser_tabs", ""), [3]int{400, 400, 400}},
		{"J", fco("ide_context", ""), [3]int{400, 400, 200}},
		{"K", fco("unknown", "") + "," + custom, [3]int{200, 200, 200}},
	}
	versions := []string{VersionV0220, VersionV0222, VersionV0224}
	for _, tc := range cases {
		for i, version := range versions {
			t.Run(tc.name+"/"+version, func(t *testing.T) {
				w := httptest.NewRecorder()
				NewHandler(version).ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"input":[`+tc.input+`]}`)))
				if w.Code != tc.want[i] {
					t.Fatalf("status %d, want %d: %s", w.Code, tc.want[i], w.Body.String())
				}
			})
		}
	}
}

func TestValidationEdgeCases(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		message    string
	}{
		{"missing reference", `{"input":[{"type":"function_call_output","call_id":"call_1","output":"r"}]}`, 400, itemReferenceRefusal},
		{"matching reference", `{"input":[{"type":"function_call_output","call_id":"call_1"},{"type":"item_reference","id":"call_1"}]}`, 200, ""},
		{"reference exact", `{"input":[{"type":"function_call_output","call_id":" call_1 "},{"type":"item_reference","id":"call_1"}]}`, 400, itemReferenceRefusal},
		{"numeric output id", `{"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","call_id":42,"output":"x"}]}`, 400, itemReferenceRefusal},
		{"numeric context", `{"input":[{"type":"function_call","call_id":42},{"type":"function_call_output","name":"unknown"}]}`, 200, ""},
		{"empty previous", `{"previous_response_id":"","input":[{"type":"function_call_output","name":"unknown"}]}`, 400, refusal},
		{"snapshot duplicate", `{"input":[{"type":"function_call_output","namespace":"agent","name":"codex_apps_open_page","output":"{\"page_id\":null,\"page_id\":\"x\"}"}]}`, 400, refusal},
		{"request duplicate", `{"x":{"a":1,"a":2},"input":[{"type":"function_call_output","namespace":"agent","name":"browser_info","output":"x"}]}`, 400, refusal},
		{"other output missing id", `{"input":[{"type":"function_call_output","call_id":"c"},{"type":"custom_tool_call_output"}]}`, 400, refusal},
		{"overflow", `{"input":[{"type":"function_call_output","namespace":"agent","name":"codex_apps_selected_page_part_1_of_99999999999999999999","output":"Companion Page snapshot part 1 of 9223372036854775807:\nx"}]}`, 400, refusal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			NewHandler(VersionV0224).ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(tc.body)))
			if w.Code != tc.status || (tc.message != "" && !strings.Contains(w.Body.String(), tc.message)) {
				t.Fatalf("status %d body %s", w.Code, w.Body.String())
			}
		})
	}
	for _, version := range []string{"unknown", "0fcddce69"} {
		t.Run(version, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("expected panic")
				}
			}()
			NewHandler(version)
		})
	}
}
