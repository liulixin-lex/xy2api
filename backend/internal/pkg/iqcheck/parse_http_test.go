package iqcheck

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

func finalEvent(answer string) string {
	return `data: {"type":"response.completed","response":{"id":"fixture","status":"completed","model":"fixture","output":[{"type":"reasoning","content":"opaque"},{"type":"message","role":"assistant","phase":"commentary","content":"opaque"},{"type":"message","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"` + answer + `"}]}]}}` + "\n\n"
}
func TestHTTPStreamContracts(t *testing.T) {
	stream := ": heartbeat\n\nevent: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"29\"}\n\n" +
		"data: {\"type\":\"response.auxiliary\",\"response\":\"extension\"}\n\n" + finalEvent("21")
	for _, tc := range []struct {
		name, body, media, status, reason string
		detected                          bool
	}{
		{"oauth stream", stream, "text/event-stream; charset=utf-8", "smart", "correct_answer", false},
		{"CRLF", strings.ReplaceAll(stream, "\n", "\r\n"), "text/event-stream", "smart", "correct_answer", false},
		{"missing content type", stream, "", "smart", "correct_answer", true},
		{"mislabeled stream", stream, "application/json", "smart", "correct_answer", true},
		{"generic media", stream, "application/octet-stream", "smart", "correct_answer", true},
		{"wrong answer", finalEvent("29"), "text/event-stream", "degraded", "wrong_answer", false},
		{"HTML", `<html>21</html>`, "text/html", "unknown", "unsupported_media_type", false},
		{"bad JSON", "data: {bad}\n\n", "text/event-stream", "unknown", "invalid_event_json", false},
		{"critical duplicate", `data: {"type":"response.created","type":"response.completed"}` + "\n\n" + finalEvent("21"), "text/event-stream", "unknown", "duplicate_critical_field", false},
		{"no completion", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"21\"}\n\n", "text/event-stream", "unknown", "incomplete_response", false},
		{"conflict", finalEvent("21") + finalEvent("29"), "text/event-stream", "unknown", "conflicting_final_response", false},
		{"same completion", finalEvent("21") + finalEvent("21"), "text/event-stream", "smart", "correct_answer", false},
		{"error after completion", finalEvent("21") + "data: {\"type\":\"error\"}\n\n", "text/event-stream", "unknown", "upstream_error", false},
		{"done without completion", "data: [DONE]\n\n", "text/event-stream", "unknown", "incomplete_response", false},
		{"after done", finalEvent("21") + "data: [DONE]\n\n" + finalEvent("21"), "text/event-stream", "unknown", "data_after_done", false},
		{"wrong event name", "event: response.failed\n" + finalEvent("21"), "text/event-stream", "unknown", "event_type_mismatch", false},
		{"multiline", "id: 123\nretry: 300\ndata: {\"type\":\ndata: \"response.auxiliary\"}\n\n" + finalEvent("21"), "text/event-stream", "smart", "correct_answer", false},
		{"tail frame", strings.TrimRight(finalEvent("21"), "\n"), "text/event-stream", "smart", "correct_answer", false},
		{"bad terminal", `data: {"type":"response.completed","response":{"status":"completed","output":"21"}}` + "\n\n", "text/event-stream", "unknown", "invalid_event_shape", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {tc.media}}, Body: io.NopCloser(strings.NewReader(tc.body))}, false, "http")
			if r.Status != tc.status || r.Reason != tc.reason || r.Diagnostic == nil || r.Diagnostic.FormatDetected != tc.detected {
				t.Fatalf("%+v diagnostic=%+v", r, r.Diagnostic)
			}
			if r.Diagnostic.BytesRead > MaxResponseBytes+1 {
				t.Fatal(r.Diagnostic)
			}
		})
	}
}

type brokenReader struct{ err error }

func (r brokenReader) Read([]byte) (int, error) { return 0, r.err }
func TestHTTPBoundsCompressionAndDiagnostics(t *testing.T) {
	var zipped bytes.Buffer
	w := gzip.NewWriter(&zipped)
	_, _ = w.Write([]byte(finalEvent("21")))
	_ = w.Close()
	r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Content-Encoding": {"gzip"}}, Body: io.NopCloser(&zipped)}, false, "plugin")
	if r.Status != "smart" || r.Diagnostic.Transport != "plugin" {
		t.Fatal(r)
	}
	r = ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Content-Encoding": {"gzip"}}, Body: io.NopCloser(strings.NewReader("not-gzip"))}, false, "http")
	if r.Reason != "response_decompression_failed" {
		t.Fatal(r)
	}
	r = Parse(strings.NewReader("data: "+strings.Repeat("x", MaxEventBytes)+"\n\n"), true, false)
	if r.Reason != "event_too_large" {
		t.Fatal(r)
	}
	r = Parse(strings.NewReader(strings.Repeat("data: {\"type\":\"x\"}\n\n", MaxEvents+1)), true, false)
	if r.Reason != "too_many_events" {
		t.Fatal(r)
	}
	r = Parse(io.MultiReader(strings.NewReader(finalEvent("21")), brokenReader{context.DeadlineExceeded}), true, false)
	if r.Reason != "timeout" || r.Status != "unknown" {
		t.Fatal(r)
	}
	r = Parse(strings.NewReader(`data: {"type":"response.auxiliary","SECRET_KEY":{"type":1,"type":2}}`+"\n\n"), true, false)
	if strings.Contains(string(r.Diagnostic.JSON()), "SECRET_KEY") {
		t.Fatal(r.Diagnostic)
	}
	d := &Diagnostic{RequestID: strings.Repeat("x", 10000), EventType: strings.Repeat("x", 10000), Field: strings.Repeat("x", 10000)}
	if len(d.JSON()) > 4096 {
		t.Fatal("unbounded diagnostic")
	}
	for _, code := range []int{400, 401, 429, 500, 503} {
		r = ParseHTTP(&http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("secret"))}, false, "http")
		if r.Reason != httpCode(code) || r.Answer != "" {
			t.Fatal(r)
		}
	}
}

func TestOAuthContractFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/oauth-responses.sse")
	if err != nil {
		t.Fatal(err)
	}
	r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(bytes.NewReader(raw))}, false, "http")
	if r.Status != "smart" || r.Answer != `{"answer":21}` || r.Diagnostic.EventIndex != 9 {
		t.Fatalf("%+v %+v", r, r.Diagnostic)
	}
	for _, codec := range []string{"br", "zstd"} {
		var b bytes.Buffer
		if codec == "br" {
			w := brotli.NewWriter(&b)
			_, _ = w.Write(raw)
			_ = w.Close()
		} else {
			w, e := zstd.NewWriter(&b)
			if e != nil {
				t.Fatal(e)
			}
			_, _ = w.Write(raw)
			_ = w.Close()
		}
		r = ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Content-Encoding": {codec}}, Body: io.NopCloser(&b)}, false, "plugin")
		if r.Status != "smart" {
			t.Fatal(codec, r)
		}
	}
}

func TestDiagnosticFailureEventAndCompression(t *testing.T) {
	for _, codec := range []string{"br", "zstd", "deflate"} {
		r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Content-Encoding": {codec}}, Body: io.NopCloser(strings.NewReader("invalid compressed bytes"))}, false, "plugin")
		if r.Status != "unknown" || r.Reason != "response_decompression_failed" {
			t.Fatalf("%s: %+v", codec, r)
		}
	}
	r := Parse(strings.NewReader("event: response.completed\ndata: {bad}\n\n"), true, false)
	if r.Diagnostic.EventType != "response.completed" || r.Diagnostic.EventIndex != 1 {
		t.Fatal(r.Diagnostic)
	}
}
