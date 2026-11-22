//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/tidwall/gjson"
)

type replayUpstream struct {
	mu          sync.Mutex
	accountBase int64
	c           replayCase
	turn        int
	counts      map[int64]int
	sends       []replaySend
	cancel      context.CancelFunc
	learning    func() []string
}

type replayStallingBody struct {
	prefix *strings.Reader
	ctx    context.Context
}

func (b *replayStallingBody) Read(p []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(p)
	}
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-time.After(3 * time.Second):
		return 0, context.DeadlineExceeded
	}
}
func (*replayStallingBody) Close() error { return nil }

func replayAccount(id int64) string {
	switch id {
	case 1:
		return "first"
	case 2:
		return "second"
	default:
		return "unexpected"
	}
}

func replaySurvived(body []byte) []string {
	out := []string{}
	var task, arguments bool
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "agent_message" && strings.Contains(item.Get("content").Raw, "cipher-task") {
			task = true
		}
		if item.Get("type").String() == "function_call" && strings.Contains(item.Get("arguments").String(), "cipher-") {
			arguments = true
		}
	}
	for _, content := range []struct{ name, marker string }{
		{"task", "cipher-task"}, {"task_envelope", "task envelope"}, {"reasoning", "cipher-reasoning"}, {"reasoning_second", "cipher-second"}, {"reasoning_third", "cipher-third"},
		{"reasoning_summary", "reasoning summary"}, {"reasoning_summary_second", "second summary"}, {"reasoning_summary_third", "third summary"}, {"compaction", "cipher-compaction"}, {"ordinary", "cipher-ordinary"}, {"question", "preserve this question"},
		{"arguments", "cipher-arguments"}, {"plaintext_task", "readable task"}, {"tool_context", "synthetic tool context"}, {"search_history", "synthetic query"},
	} {
		present := strings.Contains(string(body), content.marker)
		if content.name == "task" {
			present = task
		}
		if content.name == "arguments" {
			present = arguments
		}
		if present {
			out = append(out, content.name)
		}
	}
	return out
}

func replayResponse(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func replayError(code, message, param string) string {
	raw, _ := json.Marshal(map[string]any{"error": map[string]string{"type": "invalid_request_error", "code": code, "message": message, "param": param}})
	return string(raw)
}

func replayFrame(kind, data string) string { return "event: " + kind + "\ndata: " + data + "\n\n" }

func (u *replayUpstream) rejection(status int, payload string) *http.Response {
	if u.c.Form == "outer_gateway" {
		return replayResponse(http.StatusBadGateway, "application/json", payload)
	}
	if u.c.Form == "http" {
		return replayResponse(status, "application/json", payload)
	}
	err := gjson.Get(payload, "error").Raw
	failed := `{"type":"response.failed","response":{"id":"resp_fixture","object":"response","status":"failed","error":` + err + `,"usage":{"input_tokens":1,"output_tokens":1}}}`
	if u.c.Form == "failed_json" {
		return replayResponse(200, "application/json", gjson.Get(failed, "response").Raw)
	}
	frames := ""
	if u.c.Form == "heartbeat_failure" {
		frames += replayFrame("ping", `{"type":"ping"}`)
	}
	if u.c.Form == "created_failure" || u.c.Form == "post_output_failure" {
		frames += replayFrame("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_fixture","status":"in_progress"}}`)
	}
	if u.c.Form == "post_output_failure" {
		frames += replayFrame("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":1,"delta":"partial answer"}`)
	}
	return replayResponse(200, "text/event-stream", frames+replayFrame("response.failed", failed))
}

func (u *replayUpstream) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	service.RecordOpsUpstreamSendBoundary(req.Context(), id)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	id -= u.accountBase
	u.counts[id]++
	send := replaySend{Account: replayAccount(id), Body: replayDigest(body), Survived: replaySurvived(body), Learning: u.learning(), Rule: "success"}
	send.Items = map[string]string{}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		key := item.Get("id").String()
		if key == "" {
			key = "anonymous_" + replayDigest([]byte(item.Raw))
		}
		send.Items[key] = replayDigest([]byte(item.Raw))
	}
	u.sends = append(u.sends, send)
	setRule := func(kind string) { u.sends[len(u.sends)-1].Rule = kind }
	if u.c.SharedQuota > 0 && len(u.sends) > u.c.SharedQuota {
		setRule("shared_quota")
		return u.rejection(429, replayError("rate_limit_exceeded", "synthetic shared quota exhausted", "")), nil
	}
	rules := u.c.First
	if id == 2 {
		rules = u.c.Second
	}
	if u.turn < 0 {
		rules = nil
	}
	for _, rule := range rules {
		var payload string
		status := http.StatusBadRequest
		switch rule.Kind {
		case "named_always":
			payload = replayError("invalid_encrypted_content", "Encrypted content could not be verified for item "+rule.Item, "")
		case "named":
			for _, item := range gjson.GetBytes(body, "input").Array() {
				if (item.Get("id").String() == rule.Item || item.Get("id").String() == "amsg_new_task" && rule.Item == "amsg_task") && strings.Contains(item.Raw, `"encrypted_content"`) {
					payload = replayError("invalid_encrypted_content", "Encrypted content could not be verified for item "+item.Get("id").String(), "")
					break
				}
			}
		case "any_encrypted":
			if strings.Contains(string(body), `"encrypted_content"`) {
				payload = replayError("invalid_encrypted_content", "Encrypted content could not be verified", "")
			}
		case "missing":
			payload = replayError("invalid_encrypted_content", "Encrypted content could not be verified for item amsg_missing", "")
		case "ambiguous":
			if strings.Contains(string(body), `"encrypted_content"`) {
				payload = replayError("invalid_encrypted_content", "Encrypted content could not be verified for item rs_alpha", "input[0].encrypted_content")
			}
		case "wrapped_once", "wrapped_encrypted", "wrapped_always":
			if rule.Kind == "wrapped_always" || (rule.Kind == "wrapped_once" && u.counts[id] == 1) || (rule.Kind == "wrapped_encrypted" && strings.Contains(string(body), "cipher-task")) {
				setRule(rule.Kind)
				return replayResponse(200, "text/event-stream", replayFrame("response.failed", `{"type":"response.failed","response":{"id":"resp_fixture","status":"failed","error":{"type":"upstream_error","code":"400","status_code":400,"message":"request rejected"}}}`)), nil
			}
		case "reject_placeholder":
			if strings.Contains(string(body), "[encrypted content omitted]") {
				status = 422
				payload = replayError("invalid_request_error", "placeholder form rejected", "")
			}
		case "reasoning_shape":
			for _, item := range gjson.GetBytes(body, "input").Array() {
				if item.Get("type").String() == "reasoning" && !item.Get("encrypted_content").Exists() {
					payload = replayError("invalid_request_error", "reasoning requires encrypted content", "")
				}
			}
		case "ordinary_error":
			payload = replayError("invalid_request_error", "invalid synthetic request", "")
		case "conflict":
			status = 409
			payload = replayError("provenance_conflict", "conversation source conflict", "")
		case "rate_limit":
			status = 429
			payload = replayError("rate_limit_exceeded", "Rate limit reached for conversation source", "")
		case "server_error":
			status = 500
			payload = replayError("server_error", "synthetic upstream unavailable", "")
		case "first_turn_fails":
			if u.turn == 0 {
				payload = replayError("invalid_encrypted_content", "Encrypted content could not be verified for item amsg_task", "")
			}
		case "tool_output":
			for _, item := range gjson.GetBytes(body, "input").Array() {
				if item.Get("type").String() == "function_call_output" && !item.Get("call_id").Exists() {
					payload = replayError("", "function_call_output requires call_id on HTTP requests", "")
				}
			}
		case "field":
			if gjson.GetBytes(body, rule.Item).Exists() {
				payload = replayError("unknown_parameter", "Unknown parameter: '"+rule.Item+"'.", rule.Item)
			}
		case "web_search":
			if !strings.Contains(gjson.GetBytes(body, "tools").Raw, `"web_search"`) {
				status = 500
				payload = replayError("internal_error", "Response protection is unavailable", "")
			}
		case "args_reject":
			payload = replayError("invalid_encrypted_content", "Encrypted function arguments could not be decrypted", "input[0].arguments")
		case "args_placeholder_accepted":
			unreadable := false
			for _, item := range gjson.GetBytes(body, "input").Array() {
				if item.Get("type").String() == "function_call" && strings.Contains(item.Get("arguments").String(), "cipher-") {
					unreadable = true
				}
			}
			if unreadable {
				payload = replayError("invalid_encrypted_content", "Encrypted function arguments could not be decrypted", "input[0].arguments")
			}
		case "args_accept", "args_plaintext":
		case "hang":
			setRule(rule.Kind)
			// The real transport also has a response-header deadline. Budget-off
			// sends can intentionally detach the client's cancellation context.
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(3 * time.Second):
				return nil, context.DeadlineExceeded
			}
		case "cancel":
			setRule(rule.Kind)
			u.cancel()
			return nil, context.Canceled
		case "created_stall":
			setRule(rule.Kind)
			resp := replayResponse(200, "text/event-stream", "")
			resp.Body = &replayStallingBody{prefix: strings.NewReader(replayFrame("response.created", `{"type":"response.created","response":{"id":"resp_fixture","status":"in_progress"}}`)), ctx: req.Context()}
			return resp, nil
		case "executable_failure":
			setRule(rule.Kind)
			frames := replayFrame("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_fixture","call_id":"call_fixture","name":"synthetic_action","arguments":"{}"}}`)
			frames += replayFrame("response.failed", `{"type":"response.failed","response":{"id":"resp_fixture","status":"failed","error":{"type":"server_error","code":"server_error","message":"synthetic failure after call"}}}`)
			return replayResponse(200, "text/event-stream", frames), nil
		default:
			return nil, fmt.Errorf("unknown synthetic rule %q", rule.Kind)
		}
		if payload != "" {
			setRule(rule.Kind)
			return u.rejection(status, payload), nil
		}
	}
	if strings.HasSuffix(req.URL.Path, "/chat/completions") {
		if gjson.GetBytes(body, "stream").Bool() {
			return replayResponse(200, "text/event-stream", `data: {"id":"chatcmpl_fixture","object":"chat.completion.chunk","created":946684800,"model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant","content":"fixture answer"},"finish_reason":null}]}`+"\n\n"+`data: {"id":"chatcmpl_fixture","object":"chat.completion.chunk","created":946684800,"model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`+"\n\ndata: [DONE]\n\n"), nil
		}
		return replayResponse(200, "application/json", `{"id":"chatcmpl_fixture","object":"chat.completion","created":946684800,"model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"fixture answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`), nil
	}
	response := `{"id":"resp_fixture","object":"response","created_at":946684800,"model":"gpt-5.5","status":"completed","output":[{"type":"message","id":"msg_fixture","role":"assistant","status":"completed","content":[{"type":"output_text","text":"fixture answer","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	if u.c.Stream || u.c.Form != "http" {
		return replayResponse(200, "text/event-stream", replayFrame("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_fixture","status":"in_progress"}}`)+replayFrame("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":1,"delta":"fixture answer"}`)+replayFrame("response.completed", `{"type":"response.completed","sequence_number":2,"response":`+response+`}`)), nil
	}
	return replayResponse(200, "application/json", response), nil
}

func (u *replayUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}
