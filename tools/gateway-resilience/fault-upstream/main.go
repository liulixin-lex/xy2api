// Command fault-upstream is a fault-injecting upstream for gateway
// resilience tests (staging-validation §5). It speaks the OpenAI Responses
// and Chat Completions, Anthropic Messages, Gemini and image shapes, plus
// the sub2api /v1/usage and new-api billing endpoints.
//
// A scenario is chosen per request by the X-Fault header or by a path prefix
// (so an account base_url can pin it):
//
//	X-Fault: F02;after=5
//	POST /f/F02;after=5/v1/responses
//
// Scenarios: see the scenarios table below or GET /_scenarios. Every
// request is recorded (GET /_requests) with its outcome, so tests can prove
// that a client disconnect cancelled the upstream call.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var scenarios = map[string]string{
	"F00":  "normal response (first=seconds before the first token)",
	"F01":  "slow response headers (secs)",
	"F02":  "200 SSE, then response.failed busy after `after` seconds",
	"F03":  "200 SSE, then response.failed rate_limit_exceeded",
	"F04":  "200 SSE, then response.failed insufficient_quota",
	"F05":  "200 SSE, then response.failed context_length_exceeded",
	"F06":  "stall after some output (secs, then the connection is dropped)",
	"F07":  "502 Cloudflare error page, Retry-After 60",
	"F08":  "524 Cloudflare timeout page, Retry-After 120",
	"F09":  "503 without body",
	"F10":  "402 insufficient balance",
	"F11":  "403 insufficient balance",
	"F12":  "429 Insufficient account balance",
	"F13":  "429 rate limit with reset time (reset=seconds)",
	"F14":  "429 rate limit without reset time",
	"F15":  "401 invalid API key",
	"F16":  "529 overloaded / server_is_overloaded (status=503 for the OpenAI form)",
	"F17":  "normal long reasoning (first=seconds before the first token)",
	"F18":  "image generation taking secs",
	"F19":  "GET /v1/usage balance (state key=…, see /_control)",
	"F20":  "new-api billing endpoints (state key=…)",
	"F21":  "Anthropic stream: message_start, then stall (secs)",
	"F22":  "Gemini stream (first=seconds)",
	"F23":  "slow stream: a delta every `every` seconds for `secs` seconds, then completed with usage",
	"F24":  "relay keep-alive: empty SSE-Keep-Alive deltas (one before response.created) every `every` seconds for `secs` seconds, then response.failed busy; with output=1 a real delta comes first and the stream then completes",
	"F25":  "pooled relay: a Responses request carrying previous_response_id gets a vague 400 (Upstream request failed); others answer normally",
	"F26":  "200 SSE with some output, then an in-stream failure (code=busy|rate_limit_exceeded, default busy) after `after` seconds",
	"F27":  "403 content-safety refusal in Zhipu's shape (top-level code request_check_blocked)",
	"F28":  "Anthropic relay queue timeout: 200 SSE whose first event, after `after` seconds, is an error rate_limit_error (gateway_concurrency_limit)",
	"F29":  "Codex backend relay: a Responses request replaying web_search_call input without declaring web_search gets 200 SSE, then response.failed \"response protection is unavailable\"; others answer normally",
	"F30":  "502 Cloudflare JSON error answer (origin_bad_gateway, detail: the origin is overloaded or misconfigured, retry_after 60 in the body)",
	"F31":  "Responses: response.created, then response.failed server_is_overloaded after 50 ms (after)",
	"F32":  "Responses: response.created, then response.failed server_error / The service is busy (after)",
	"F33":  "Responses: response.created, response.in_progress, then response.failed server_is_overloaded (after)",
	"F34":  "Responses: response.created, unknown codex.response.metadata, then response.failed (conservative no-retry case; not observed in production)",
	"F34B": "Responses: empty output_text delta before created, known prefix events, then response.failed (before, after)",
	"F35":  "raw Chat only: error data frame followed by [DONE]; Responses endpoint returns 404",
	"F36":  "nonstream requests get 400 This group only accepts streaming requests with OpenAI error.param=null; streams succeed",
	"F37":  "503 server_is_overloaded with a known Retry-After (reset=8, delay for CDN commitment)",
	"BAL":  "normal while the balance of state key=… is positive, F12 otherwise (reset=seconds adds a relay's exhausted ratelimit headers)",
	"MIX":  "random fault with probability p percent (default 5), otherwise normal",
}

type fault struct {
	id     string
	params map[string]string
}

func (f fault) secs(name string, def float64) time.Duration {
	if v, err := strconv.ParseFloat(f.params[name], 64); err == nil {
		return time.Duration(v * float64(time.Second))
	}
	return time.Duration(def * float64(time.Second))
}

func (f fault) num(name string, def int) int {
	if v, err := strconv.Atoi(f.params[name]); err == nil {
		return v
	}
	return def
}

func parseFault(spec string) fault {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return fault{id: "F00", params: map[string]string{}}
	}
	parts := strings.FieldsFunc(spec, func(r rune) bool { return r == ';' || r == ',' })
	f := fault{id: strings.ToUpper(parts[0]), params: map[string]string{}}
	for _, p := range parts[1:] {
		if k, v, ok := strings.Cut(p, "="); ok {
			f.params[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return f
}

// ---- request records -------------------------------------------------------

type record struct {
	ID        int64     `json:"id"`
	Fault     string    `json:"fault"`
	Spec      string    `json:"spec,omitempty"` // the full scenario spec, to tell accounts apart
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Model     string    `json:"model,omitempty"`
	Stream    bool      `json:"stream"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended"`
	Outcome   string    `json:"outcome"` // completed, cancelled, dropped, error_status
	Status    int       `json:"status"`
	Bytes     int64     `json:"bytes"`
	RequestID string    `json:"request_id,omitempty"`
}

type recorder struct {
	mu   sync.Mutex
	next int64
	recs []*record
}

func (r *recorder) start(rec *record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	rec.ID = r.next
	r.recs = append(r.recs, rec)
	if len(r.recs) > 20000 {
		r.recs = append([]*record(nil), r.recs[len(r.recs)-10000:]...)
	}
}

func (r *recorder) list(since int64) []record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []record{}
	for _, rec := range r.recs {
		if rec.ID > since {
			out = append(out, *rec)
		}
	}
	return out
}

var records = &recorder{}

// ---- balance state ---------------------------------------------------------

type balanceState struct {
	mu     sync.Mutex
	values map[string]float64
}

func (b *balanceState) get(key string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if v, ok := b.values[key]; ok {
		return v
	}
	return 100
}

func (b *balanceState) set(key string, v float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.values[key] = v
}

var balances = &balanceState{values: map[string]float64{}}

// ---- writer helpers --------------------------------------------------------

type countingWriter struct {
	http.ResponseWriter
	rec *record
}

func (w *countingWriter) WriteHeader(code int) {
	w.rec.Status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *countingWriter) Write(p []byte) (int, error) {
	if w.rec.Status == 0 {
		w.rec.Status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.rec.Bytes += int64(n)
	return n, err
}

func (w *countingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func sse(w http.ResponseWriter, event string, v any) error {
	payload, _ := json.Marshal(v)
	var err error
	if event != "" {
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
	} else {
		_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return err
}

func startSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// ---- protocols ---------------------------------------------------------------

type protocol string

const (
	protoResponses protocol = "responses"
	protoChat      protocol = "chat"
	protoAnthropic protocol = "anthropic"
	protoGemini    protocol = "gemini"
	protoImages    protocol = "images"
	protoOther     protocol = "other"
)

func protocolOf(path string) protocol {
	switch {
	case strings.HasSuffix(path, "/responses"):
		return protoResponses
	case strings.HasSuffix(path, "/chat/completions"):
		return protoChat
	case strings.HasSuffix(path, "/messages"):
		return protoAnthropic
	case strings.Contains(path, ":generateContent") || strings.Contains(path, ":streamGenerateContent"):
		return protoGemini
	case strings.Contains(path, "/images/"):
		return protoImages
	}
	return protoOther
}

const deltaText = "fault-upstream says hello. "

type usage struct{ in, out, cached int }

func defaultUsage() usage { return usage{in: 120, out: 24, cached: 64} }

// normal writes a successful response; stallAfter > 0 stops after a few
// deltas and keeps the connection silent (F06).
func normal(ctx context.Context, w http.ResponseWriter, p protocol, model string, stream bool, first, stallAfter time.Duration, failCode string, failAfter time.Duration) string {
	u := defaultUsage()
	if !stream {
		if !sleepCtx(ctx, first) {
			return "cancelled"
		}
		writeJSON(w, http.StatusOK, nonStreamBody(p, model, u))
		return "completed"
	}
	startSSE(w)
	id := fmt.Sprintf("resp_fault_%d", time.Now().UnixNano())
	switch p {
	case protoAnthropic:
		_ = sse(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": model, "content": []any{},
			"usage": map[string]any{"input_tokens": u.in, "output_tokens": 1, "cache_read_input_tokens": u.cached}}})
	case protoResponses:
		_ = sse(w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "model": model}})
	}
	if failCode != "" {
		if !sleepCtx(ctx, failAfter) {
			return "cancelled"
		}
		writeFailFrame(w, p, failCode)
		return "failed_in_stream"
	}
	if !sleepCtx(ctx, first) {
		return "cancelled"
	}
	if p == protoAnthropic {
		_ = sse(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	}
	for i := 0; i < 3; i++ {
		if err := writeDelta(w, p, id, model); err != nil {
			return "cancelled"
		}
		if !sleepCtx(ctx, 200*time.Millisecond) {
			return "cancelled"
		}
	}
	if stallAfter > 0 {
		if !sleepCtx(ctx, stallAfter) {
			return "cancelled"
		}
		return "dropped"
	}
	writeStreamEnd(w, p, id, model, u)
	return "completed"
}

// outputThenFail sends some output and then an in-stream failure, as relays
// do when their upstream overloads mid-answer (production 2026-10-16, R2).
func outputThenFail(ctx context.Context, w http.ResponseWriter, p protocol, model, code string, after time.Duration) string {
	if code == "" {
		code = "busy"
	}
	startSSE(w)
	id := fmt.Sprintf("resp_fault_%d", time.Now().UnixNano())
	switch p {
	case protoAnthropic:
		_ = sse(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": model, "content": []any{},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 1}}})
		_ = sse(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	case protoResponses:
		_ = sse(w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "model": model}})
	}
	for i := 0; i < 2; i++ {
		if err := writeDelta(w, p, id, model); err != nil {
			return "cancelled"
		}
	}
	if !sleepCtx(ctx, after) {
		return "cancelled"
	}
	writeFailFrame(w, p, code)
	return "failed_in_stream"
}

// preludeFailure carries no text output. Its stable attempt prefix lets the
// client assertions prove the failed upstream's opening event was discarded.
func preludeFailure(ctx context.Context, w http.ResponseWriter, model string, f fault) string {
	startSSE(w)
	id := fmt.Sprintf("resp_fault_%s_%d", strings.ToLower(f.id), time.Now().UnixNano())
	response := map[string]any{"id": id, "status": "in_progress", "model": model}
	if f.id == "F34B" {
		// Probes observed a delta before created without inspecting its content.
		// This synthetic empty case has no SSE-Keep-Alive marker, so its handling
		// is covered independently from the old relay keep-alive fixture.
		if err := sse(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": ""}); err != nil {
			return "cancelled"
		}
		if !sleepCtx(ctx, f.secs("before", 0.1)) {
			return "cancelled"
		}
	}
	if err := sse(w, "response.created", map[string]any{"type": "response.created", "response": response}); err != nil {
		return "cancelled"
	}
	if f.id == "F33" || f.id == "F34B" {
		if err := sse(w, "response.in_progress", map[string]any{"type": "response.in_progress", "response": response}); err != nil {
			return "cancelled"
		}
	}
	if f.id == "F34" {
		if err := sse(w, "codex.response.metadata", map[string]any{"type": "codex.response.metadata", "response_id": id}); err != nil {
			return "cancelled"
		}
	}
	if f.id == "F34B" {
		itemID := "msg_" + strings.TrimPrefix(id, "resp_fault_")
		item := map[string]any{"id": itemID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}
		if err := sse(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "response_id": id, "output_index": 0, "item": item}); err != nil {
			return "cancelled"
		}
		part := map[string]any{"type": "output_text", "text": "", "annotations": []any{}}
		if err := sse(w, "response.content_part.added", map[string]any{"type": "response.content_part.added", "response_id": id, "item_id": itemID, "output_index": 0, "content_index": 0, "part": part}); err != nil {
			return "cancelled"
		}
	}
	if !sleepCtx(ctx, f.secs("after", 0.05)) {
		return "cancelled"
	}
	code, message := "server_is_overloaded", "Overloaded"
	if f.id == "F32" {
		code, message = "server_error", "The service is busy"
	}
	if err := sse(w, "response.failed", map[string]any{"type": "response.failed", "response": map[string]any{
		"id": id, "status": "failed", "error": map[string]any{"type": "server_error", "code": code, "message": message}}}); err != nil {
		return "cancelled"
	}
	return "failed_in_stream"
}

// relayKeepalive imitates relays that send empty "SSE-Keep-Alive" deltas,
// the first one before response.created, and then fail before any output.
// With output, a real delta comes before the frames and the stream then
// completes: a relay that keeps a quiet period alive only with the frames.
func relayKeepalive(ctx context.Context, w http.ResponseWriter, model string, total, every time.Duration, output bool) string {
	startSSE(w)
	keepalive := map[string]any{"type": "response.output_text.delta", "item_id": "SSE-Keep-Alive", "output_index": 0,
		"content_index": 0, "delta": "", "SSE-Keep-Alive": true}
	_ = sse(w, "response.output_text.delta", keepalive)
	id := fmt.Sprintf("resp_fault_%d", time.Now().UnixNano())
	_ = sse(w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "model": model}})
	if output {
		_ = writeDelta(w, protoResponses, id, model)
	}
	deadline := time.Now().Add(total)
	for time.Now().Before(deadline) {
		if !sleepCtx(ctx, every) {
			return "cancelled"
		}
		if err := sse(w, "response.output_text.delta", keepalive); err != nil {
			return "cancelled"
		}
	}
	if output {
		_ = writeDelta(w, protoResponses, id, model)
		writeStreamEnd(w, protoResponses, id, model, defaultUsage())
		return "completed"
	}
	writeFailFrame(w, protoResponses, "busy")
	return "failed_in_stream"
}

// slowStream sends a delta every `every` until `total` has passed, then
// finishes normally with usage (a long generation a client may abandon).
func slowStream(ctx context.Context, w http.ResponseWriter, p protocol, model string, total, every time.Duration) string {
	startSSE(w)
	id := fmt.Sprintf("resp_fault_%d", time.Now().UnixNano())
	if p == protoResponses {
		_ = sse(w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "model": model}})
	}
	if p == protoAnthropic {
		_ = sse(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": model, "content": []any{},
			"usage": map[string]any{"input_tokens": 120, "output_tokens": 1}}})
		_ = sse(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	}
	deadline := time.Now().Add(total)
	for time.Now().Before(deadline) {
		if err := writeDelta(w, p, id, model); err != nil {
			return "cancelled"
		}
		if !sleepCtx(ctx, every) {
			return "cancelled"
		}
	}
	writeStreamEnd(w, p, id, model, defaultUsage())
	return "completed"
}

func writeDelta(w http.ResponseWriter, p protocol, id, model string) error {
	switch p {
	case protoResponses:
		return sse(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "msg_1", "output_index": 0, "content_index": 0, "delta": deltaText})
	case protoChat:
		return sse(w, "", map[string]any{"id": id, "object": "chat.completion.chunk", "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": deltaText}}}})
	case protoAnthropic:
		return sse(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": deltaText}})
	case protoGemini:
		return sse(w, "", map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": deltaText}}}}}})
	}
	_, err := io.WriteString(w, deltaText)
	return err
}

func writeStreamEnd(w http.ResponseWriter, p protocol, id, model string, u usage) {
	switch p {
	case protoResponses:
		_ = sse(w, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{
			"id": id, "status": "completed", "model": model,
			"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": strings.Repeat(deltaText, 3)}}}},
			"usage":  map[string]any{"input_tokens": u.in, "output_tokens": u.out, "total_tokens": u.in + u.out, "input_tokens_details": map[string]any{"cached_tokens": u.cached}}}})
	case protoChat:
		_ = sse(w, "", map[string]any{"id": id, "object": "chat.completion.chunk", "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": u.in, "completion_tokens": u.out, "total_tokens": u.in + u.out, "prompt_tokens_details": map[string]any{"cached_tokens": u.cached}}})
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	case protoAnthropic:
		_ = sse(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		_ = sse(w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": u.out}})
		_ = sse(w, "message_stop", map[string]any{"type": "message_stop"})
	case protoGemini:
		_ = sse(w, "", map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": ""}}}, "finishReason": "STOP"}},
			"usageMetadata": map[string]any{"promptTokenCount": u.in, "candidatesTokenCount": u.out, "totalTokenCount": u.in + u.out, "cachedContentTokenCount": u.cached}})
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func nonStreamBody(p protocol, model string, u usage) any {
	text := strings.Repeat(deltaText, 3)
	switch p {
	case protoResponses:
		return map[string]any{"id": "resp_fault", "object": "response", "status": "completed", "model": model,
			"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}},
			"usage":  map[string]any{"input_tokens": u.in, "output_tokens": u.out, "total_tokens": u.in + u.out, "input_tokens_details": map[string]any{"cached_tokens": u.cached}}}
	case protoChat:
		return map[string]any{"id": "chatcmpl-fault", "object": "chat.completion", "model": model,
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": u.in, "completion_tokens": u.out, "total_tokens": u.in + u.out, "prompt_tokens_details": map[string]any{"cached_tokens": u.cached}}}
	case protoAnthropic:
		return map[string]any{"id": "msg_fault", "type": "message", "role": "assistant", "model": model,
			"content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": u.in, "output_tokens": u.out, "cache_read_input_tokens": u.cached}}
	case protoGemini:
		return map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}}, "finishReason": "STOP"}},
			"usageMetadata": map[string]any{"promptTokenCount": u.in, "candidatesTokenCount": u.out, "totalTokenCount": u.in + u.out}}
	}
	return map[string]any{"ok": true}
}

func answersNormally(f fault, key string) bool {
	switch f.id {
	case "F00", "F17", "F22", "F23", "F24", "F25", "F26", "F29", "F31", "F32", "F33", "F34", "F34B", "F36", "F37":
		// F26 fails streams only; the capability probe is not streamed.
		return true
	case "BAL":
		return balances.get(key) > 0
	}
	return false
}

func toolCallBody(model string) any {
	return map[string]any{"id": "resp_fault_probe", "object": "response", "status": "completed", "model": model,
		"output": []any{map[string]any{"type": "function_call", "id": "fc_fault", "call_id": "call_fault",
			"name": "probe_ping", "arguments": `{"ok":true}`, "status": "completed"}},
		"usage": map[string]any{"input_tokens": 40, "output_tokens": 8, "total_tokens": 48}}
}

func writeFailFrame(w http.ResponseWriter, p protocol, code string) {
	messages := map[string]string{
		"busy":                    "The server is busy. Please try again later.",
		"rate_limit_exceeded":     "Rate limit reached. Please try again in 20s.",
		"insufficient_quota":      "You exceeded your current quota, please check your plan and billing details.",
		"context_length_exceeded": "Your input exceeds the context window of this model.",
	}
	switch p {
	case protoAnthropic:
		t := map[string]string{"busy": "overloaded_error", "rate_limit_exceeded": "rate_limit_error", "insufficient_quota": "billing_error", "context_length_exceeded": "invalid_request_error"}[code]
		_ = sse(w, "error", map[string]any{"type": "error", "error": map[string]any{"type": t, "message": messages[code]}})
	case protoChat:
		_ = sse(w, "", map[string]any{"error": map[string]any{"code": code, "message": messages[code]}})
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	default:
		_ = sse(w, "response.failed", map[string]any{"type": "response.failed", "response": map[string]any{
			"id": "resp_fault", "status": "failed", "error": map[string]any{"code": code, "message": messages[code]}}})
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func cloudflarePage(w http.ResponseWriter, status int, title string, retryAfter int) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	w.Header().Set("Server", "cloudflare")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<!DOCTYPE html><html><head><title>%d: %s</title></head><body><h1>%s</h1><p>Error code %d</p><p>Cloudflare Ray ID: fault</p><p>retry_after: %d</p></body></html>", status, title, title, status, retryAfter)
}

// cloudflareJSONError writes Cloudflare's JSON 502 answer in the shape a relay
// behind Cloudflare returned in production on 2026-10-20 (scrubbed).
func cloudflareJSONError(w http.ResponseWriter) {
	w.Header().Set("Server", "cloudflare")
	writeJSON(w, http.StatusBadGateway, map[string]any{
		"type":             "https://developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-5xx-errors/error-502/",
		"title":            "Error 502: Bad gateway",
		"status":           502,
		"detail":           "The web server reported a bad gateway error. This typically indicates the origin is overloaded or misconfigured.",
		"error_code":       502,
		"error_name":       "origin_bad_gateway",
		"error_category":   "origin",
		"ray_id":           "fault",
		"cloudflare_error": true,
		"retryable":        true,
		"retry_after":      60,
	})
}

func apiError(w http.ResponseWriter, p protocol, status int, errType, code, message string) {
	if p == protoAnthropic {
		writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": errType, "message": message}})
		return
	}
	body := map[string]any{"type": errType, "message": message}
	if code != "" {
		body["code"] = code
	}
	writeJSON(w, status, map[string]any{"error": body})
}

// ---- handler -----------------------------------------------------------------

type requestBody struct {
	Model      string `json:"model"`
	Stream     bool   `json:"stream"`
	Input      any    `json:"input"`
	ToolChoice any    `json:"tool_choice"`
	Tools      []struct {
		Type string `json:"type"`
	} `json:"tools"`
}

// replaysUndeclaredWebSearch reports a Responses request whose input replays
// web_search_call items while its tools do not declare web_search.
func (b requestBody) replaysUndeclaredWebSearch() bool {
	for _, t := range b.Tools {
		if strings.HasPrefix(t.Type, "web_search") {
			return false
		}
	}
	items, _ := b.Input.([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && m["type"] == "web_search_call" {
			return true
		}
	}
	return false
}

func pickMix(f fault) fault {
	p := f.num("p", 5)
	if rand.Intn(100) >= p*len(mixFaults) {
		return fault{id: "F00", params: f.params}
	}
	return fault{id: mixFaults[rand.Intn(len(mixFaults))], params: map[string]string{"after": "2", "secs": "20", "first": "1"}}
}

var mixFaults = []string{"F01", "F02", "F03", "F06", "F07", "F09", "F13", "F14", "F16"}

func handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	spec := r.Header.Get("X-Fault")
	if strings.HasPrefix(path, "/f/") {
		rest := strings.TrimPrefix(path, "/f/")
		seg, tail, _ := strings.Cut(rest, "/")
		if spec == "" {
			spec = seg
		}
		path = "/" + tail
	}
	f := parseFault(spec)
	if f.id == "MIX" {
		f = pickMix(f)
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	var body requestBody
	_ = json.Unmarshal(raw, &body)
	p := protocolOf(path)
	stream := body.Stream || strings.Contains(path, ":streamGenerateContent")
	if choice, ok := body.ToolChoice.(string); p == protoResponses && !stream && ok && choice == "required" {
		f.params["tool"] = "1"
	}
	if f.id == "F25" && p == protoResponses && bytes.Contains(raw, []byte(`"previous_response_id"`)) {
		f.params["chained"] = "1"
	}
	if f.id == "F29" && p == protoResponses && body.replaysUndeclaredWebSearch() {
		f.params["undeclared"] = "1"
	}
	model := body.Model
	if model == "" {
		model = "fault-model"
	}
	rec := &record{Fault: f.id, Spec: spec, Method: r.Method, Path: path, Model: model, Stream: stream, Started: time.Now(),
		RequestID: r.Header.Get("X-Request-Id")}
	records.start(rec)
	cw := &countingWriter{ResponseWriter: w, rec: rec}
	outcome := serve(r.Context(), cw, p, path, model, stream, f)
	if r.Context().Err() != nil && outcome == "completed" {
		outcome = "cancelled"
	}
	rec.Outcome = outcome
	rec.Ended = time.Now()
	if outcome == "dropped" {
		panic(http.ErrAbortHandler)
	}
}

func serve(ctx context.Context, w http.ResponseWriter, p protocol, path, model string, stream bool, f fault) string {
	key := f.params["key"]
	switch {
	case strings.HasSuffix(path, "/v1/usage"):
		writeJSON(w, http.StatusOK, map[string]any{"remaining": balances.get(key), "unit": "USD"})
		return "completed"
	case strings.HasSuffix(path, "/dashboard/billing/subscription"):
		writeJSON(w, http.StatusOK, map[string]any{"object": "billing_subscription", "hard_limit_usd": 100.0})
		return "completed"
	case strings.HasSuffix(path, "/dashboard/billing/usage"):
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "total_usage": (100 - balances.get(key)) * 100})
		return "completed"
	case strings.HasSuffix(path, "/models"):
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []any{map[string]any{"id": model, "object": "model"}}})
		return "completed"
	}

	// Real relays support tools: answer the gateway's Responses capability
	// probe (tool_choice "required") with a function call whenever the
	// scenario would answer normally, so accounts keep the native path.
	if f.params["tool"] == "1" && answersNormally(f, key) {
		writeJSON(w, http.StatusOK, toolCallBody(model))
		return "completed"
	}

	// delay holds any scenario back, so an error can follow the gateway's
	// pre-output heartbeats.
	if d := f.secs("delay", 0); d > 0 && !sleepCtx(ctx, d) {
		return "cancelled"
	}

	switch f.id {
	case "F31", "F32", "F33", "F34", "F34B":
		if p != protoResponses || !stream {
			return normal(ctx, w, p, model, stream, 0, 0, "", 0)
		}
		return preludeFailure(ctx, w, model, f)
	case "F35":
		if p == protoResponses {
			apiError(w, p, http.StatusNotFound, "invalid_request_error", "unsupported_endpoint", "Responses endpoint is not supported")
			return "error_status"
		}
		if !stream {
			apiError(w, p, http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded", "Rate limit reached.")
			return "error_status"
		}
		startSSE(w)
		writeFailFrame(w, protoChat, "rate_limit_exceeded")
		return "failed_in_stream"
	case "F36":
		if !stream {
			if p == protoAnthropic {
				apiError(w, p, http.StatusBadRequest, "invalid_request_error", "streaming_required", "This group only accepts streaming requests")
			} else {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
					"type":    "invalid_request_error",
					"code":    "streaming_required",
					"message": "This group only accepts streaming requests",
					"param":   nil,
				}})
			}
			return "error_status"
		}
		return normal(ctx, w, p, model, true, f.secs("first", 0.2), 0, "", 0)
	case "F37":
		w.Header().Set("Retry-After", strconv.Itoa(f.num("reset", 8)))
		apiError(w, p, http.StatusServiceUnavailable, "overloaded_error", "server_is_overloaded", "Overloaded")
		return "error_status"
	case "F23":
		return slowStream(ctx, w, p, model, f.secs("secs", 60), f.secs("every", 5))
	case "F24":
		if p != protoResponses || !stream {
			return normal(ctx, w, p, model, stream, 200*time.Millisecond, 0, "", 0)
		}
		return relayKeepalive(ctx, w, model, f.secs("secs", 3), f.secs("every", 1), f.params["output"] == "1")
	case "F25":
		if f.params["chained"] == "1" {
			apiError(w, p, http.StatusBadRequest, "invalid_request_error", "", "Upstream request failed")
			return "error_status"
		}
		return normal(ctx, w, p, model, stream, f.secs("first", 0.2), 0, "", 0)
	case "F26":
		return outputThenFail(ctx, w, p, model, f.params["code"], f.secs("after", 1))
	case "F28":
		startSSE(w)
		if !sleepCtx(ctx, f.secs("after", 2)) {
			return "cancelled"
		}
		_ = sse(w, "error", map[string]any{"type": "error", "error": map[string]any{"type": "rate_limit_error",
			"code": "gateway_concurrency_limit", "message": "Concurrency limit exceeded for account, please retry later"}})
		return "failed_in_stream"
	case "F29":
		if f.params["undeclared"] != "1" {
			return normal(ctx, w, p, model, stream, f.secs("first", 0.2), 0, "", 0)
		}
		if !stream {
			apiError(w, p, http.StatusBadGateway, "internal_error", "", "response protection is unavailable")
			return "error_status"
		}
		startSSE(w)
		_ = sse(w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_fault_ws", "status": "in_progress", "model": model}})
		_ = sse(w, "response.failed", map[string]any{"type": "response.failed", "response": map[string]any{
			"id": "resp_fault_ws", "status": "failed", "error": map[string]any{"type": "internal_error", "message": "response protection is unavailable"}}})
		return "failed_in_stream"
	case "F00", "F17", "F22":
		return normal(ctx, w, p, model, stream, f.secs("first", 0.2), 0, "", 0)
	case "F01":
		if !sleepCtx(ctx, f.secs("secs", 30)) {
			return "cancelled"
		}
		return normal(ctx, w, p, model, stream, 0, 0, "", 0)
	case "F02", "F03", "F04", "F05":
		code := map[string]string{"F02": "busy", "F03": "rate_limit_exceeded", "F04": "insufficient_quota", "F05": "context_length_exceeded"}[f.id]
		if !stream {
			status := map[string]int{"F02": 503, "F03": 429, "F04": 429, "F05": 400}[f.id]
			apiError(w, p, status, code, code, code)
			return "error_status"
		}
		return normal(ctx, w, p, model, true, 0, 0, code, f.secs("after", 5))
	case "F06":
		return normal(ctx, w, p, model, true, 0, f.secs("secs", 300), "", 0)
	case "F07":
		cloudflarePage(w, http.StatusBadGateway, "Bad gateway", 60)
		return "error_status"
	case "F08":
		cloudflarePage(w, 524, "A timeout occurred", 120)
		return "error_status"
	case "F30":
		cloudflareJSONError(w)
		return "error_status"
	case "F09":
		w.WriteHeader(http.StatusServiceUnavailable)
		return "error_status"
	case "F27":
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "request_check_blocked",
			"message": "Request rejected by preflight safety checks. Please modify the content and try again."})
		return "error_status"
	case "F10":
		apiError(w, p, http.StatusPaymentRequired, "insufficient_quota", "insufficient_balance", "Insufficient balance. Please recharge.")
		return "error_status"
	case "F11":
		apiError(w, p, http.StatusForbidden, "permission_error", "insufficient_balance", "insufficient balance")
		return "error_status"
	case "F12":
		apiError(w, p, http.StatusTooManyRequests, "insufficient_quota", "", "Insufficient account balance")
		return "error_status"
	case "F13":
		reset := f.num("reset", 30)
		w.Header().Set("Retry-After", strconv.Itoa(reset))
		w.Header().Set("x-ratelimit-reset-requests", fmt.Sprintf("%ds", reset))
		apiError(w, p, http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded", fmt.Sprintf("Rate limit reached. Please try again in %ds.", reset))
		return "error_status"
	case "F14":
		apiError(w, p, http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded", "Rate limit reached.")
		return "error_status"
	case "F15":
		apiError(w, p, http.StatusUnauthorized, "invalid_request_error", "invalid_api_key", "Incorrect API key provided.")
		return "error_status"
	case "F16":
		status := f.num("status", 529)
		apiError(w, p, status, "overloaded_error", "server_is_overloaded", "Overloaded")
		return "error_status"
	case "F18":
		if !sleepCtx(ctx, f.secs("secs", 120)) {
			return "cancelled"
		}
		writeJSON(w, http.StatusOK, map[string]any{"created": time.Now().Unix(),
			"data":  []any{map[string]any{"b64_json": tinyPNG}},
			"usage": map[string]any{"input_tokens": 20, "output_tokens": 1056, "total_tokens": 1076}})
		return "completed"
	case "F21":
		startSSE(w)
		_ = sse(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_fault", "type": "message", "role": "assistant", "model": model, "content": []any{},
			"usage": map[string]any{"input_tokens": 120, "output_tokens": 1}}})
		if !sleepCtx(ctx, f.secs("secs", 300)) {
			return "cancelled"
		}
		return "dropped"
	case "BAL":
		if balances.get(key) <= 0 {
			if reset := f.num("reset", 0); reset > 0 {
				// A relay that also reports an exhausted request window
				// (production: a Grok relay locked a group for 28 minutes).
				w.Header().Set("x-ratelimit-limit-requests", "100")
				w.Header().Set("x-ratelimit-remaining-requests", "0")
				w.Header().Set("x-ratelimit-reset-requests", strconv.FormatInt(time.Now().Add(time.Duration(reset)*time.Second).Unix(), 10))
			}
			apiError(w, p, http.StatusTooManyRequests, "insufficient_quota", "", "Insufficient account balance")
			return "error_status"
		}
		return normal(ctx, w, p, model, stream, f.secs("first", 0.2), 0, "", 0)
	}
	apiError(w, p, http.StatusBadRequest, "invalid_request_error", "unknown_fault", "unknown fault "+f.id)
	return "error_status"
}

// 1x1 transparent PNG.
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="

// ---- control -------------------------------------------------------------------

func control(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/_scenarios":
		keys := make([]string, 0, len(scenarios))
		for k := range scenarios {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]map[string]string, 0, len(keys))
		for _, k := range keys {
			out = append(out, map[string]string{"id": k, "behaviour": scenarios[k]})
		}
		writeJSON(w, http.StatusOK, out)
	case "/_requests":
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		writeJSON(w, http.StatusOK, records.list(since))
	case "/_control/balance":
		key := r.URL.Query().Get("key")
		if v := r.URL.Query().Get("value"); v != "" {
			amount, err := strconv.ParseFloat(v, 64)
			if err != nil {
				http.Error(w, "bad value", http.StatusBadRequest)
				return
			}
			balances.set(key, amount)
		}
		writeJSON(w, http.StatusOK, map[string]any{"key": key, "balance": balances.get(key)})
	case "/healthz":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

func main() {
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/_scenarios", control)
	mux.HandleFunc("/_requests", control)
	mux.HandleFunc("/_control/", control)
	mux.HandleFunc("/healthz", control)
	mux.HandleFunc("/", handle)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("fault-upstream listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}
