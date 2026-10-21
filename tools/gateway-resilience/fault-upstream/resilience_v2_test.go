package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func faultRequest(spec, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/f/"+spec+path, strings.NewReader(body))
	w := httptest.NewRecorder()
	handle(w, r)
	return w
}

func faultEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		out = append(out, event)
	}
	return out
}

func TestResilienceV2PreludeFailures(t *testing.T) {
	for _, tc := range []struct {
		fault         string
		middle        []string
		code, message string
	}{
		{"F31", nil, "server_is_overloaded", "Overloaded"},
		{"F32", nil, "server_error", "The service is busy"},
		{"F33", []string{"response.in_progress"}, "server_is_overloaded", "Overloaded"},
		{"F34", []string{"codex.response.metadata"}, "server_is_overloaded", "Overloaded"},
		{"F34B", []string{"response.in_progress", "response.output_item.added", "response.content_part.added"}, "server_is_overloaded", "Overloaded"},
	} {
		t.Run(tc.fault, func(t *testing.T) {
			w := faultRequest(tc.fault+";after=0", "/v1/responses", `{"model":"fault-model","stream":true}`)
			if w.Code != 200 {
				t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
			}
			events := faultEvents(t, w.Body.String())
			if tc.fault == "F34B" {
				if len(events) == 0 || events[0]["type"] != "response.output_text.delta" || events[0]["delta"] != "" || strings.Contains(w.Body.String(), "SSE-Keep-Alive") {
					t.Fatalf("expected unmarked empty delta: %#v", events)
				}
				events = events[1:]
			}
			want := 2 + len(tc.middle)
			if len(events) != want {
				t.Fatalf("events = %#v", events)
			}
			if events[0]["type"] != "response.created" || events[want-1]["type"] != "response.failed" {
				t.Fatalf("unexpected sequence: %#v", events)
			}
			for i, wantType := range tc.middle {
				if events[i+1]["type"] != wantType {
					t.Fatalf("middle event %d = %#v, want %s", i, events[i+1], wantType)
				}
			}
			created := events[0]["response"].(map[string]any)
			failed := events[want-1]["response"].(map[string]any)
			if created["id"] != failed["id"] || !strings.Contains(created["id"].(string), strings.ToLower(tc.fault)) {
				t.Fatalf("response IDs must identify the failing attempt: %#v", events)
			}
			err := failed["error"].(map[string]any)
			if err["code"] != tc.code || err["message"] != tc.message {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestResilienceV2CapabilityProbes(t *testing.T) {
	for _, id := range []string{"F31", "F32", "F33", "F34", "F34B", "F36", "F37"} {
		t.Run(id, func(t *testing.T) {
			w := faultRequest(id, "/v1/responses", `{"stream":false,"tool_choice":"required"}`)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"function_call"`) {
				t.Fatalf("capability probe failed: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestResilienceV2RawChatErrorDone(t *testing.T) {
	w := faultRequest("F35", "/v1/chat/completions", `{"stream":true}`)
	if w.Code != 200 || !strings.HasSuffix(w.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("missing DONE after error: %d %s", w.Code, w.Body.String())
	}
	events := faultEvents(t, w.Body.String())
	if len(events) != 1 || events[0]["error"] == nil || events[0]["choices"] != nil {
		t.Fatalf("expected error-only stream: %#v", events)
	}
	probe := faultRequest("F35", "/v1/responses", `{"stream":false,"tool_choice":"required"}`)
	if probe.Code != 404 {
		t.Fatalf("raw Chat fixture must reject Responses, got %d", probe.Code)
	}
}

func TestResilienceV2StreamingOnly(t *testing.T) {
	w := faultRequest("F36", "/v1/responses", `{"stream":false}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "This group only accepts streaming requests") {
		t.Fatalf("nonstream = %d %s", w.Code, w.Body.String())
	}
	w = faultRequest("F36;first=0", "/v1/responses", `{"stream":true}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "response.completed") {
		t.Fatalf("stream = %d %s", w.Code, w.Body.String())
	}
}

func TestResilienceV2RecoverableOverload(t *testing.T) {
	w := faultRequest("F37;reset=8", "/v1/responses", `{"stream":false}`)
	if w.Code != 503 || w.Header().Get("Retry-After") != "8" || !strings.Contains(w.Body.String(), "server_is_overloaded") {
		t.Fatalf("overload = %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	w = faultRequest("F37;reset=8;after=0", "/v1/responses", `{"stream":true}`)
	if w.Code != 503 || w.Header().Get("Retry-After") != "8" || !strings.Contains(w.Body.String(), "server_is_overloaded") {
		t.Fatalf("stream overload = %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}
