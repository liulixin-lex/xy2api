package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// BuildAnthropicCompatDiagnosticRequest shares the production profile, identity,
// body and header functions. Diagnostics use an isolated session and never access the provenance store.
func BuildAnthropicCompatDiagnosticRequest(account *Account, body []byte, headers http.Header) ([]byte, http.Header, error) {
	profile, err := ResolveAnthropicCompatProfile(account)
	if err != nil {
		return nil, nil, err
	}
	out := headers.Clone()
	if profile == nil {
		return body, out, nil
	}
	if !profile.SupportsModel(gjson.GetBytes(body, "model").String()) {
		return nil, nil, compatInvalid("anthropic_compat_model_unsupported", "Diagnostic model is outside the profile capability range")
	}
	anchor := headers.Get("X-Claude-Code-Session-Id")
	if anchor == "" {
		anchor = uuid.NewString()
	}
	scope := &anthropicCompatScope{Entry: "diagnostic", Anchor: anchor}
	metadata, err := compatIdentity(profile, account, scope)
	if err != nil {
		return nil, nil, err
	}
	plan := &anthropicCompatPlan{Active: true, Profile: profile, Metadata: metadata}
	plan.Beta, err = compatResolveBeta(plan, headers.Get("anthropic-beta"), "", nil)
	if err != nil {
		return nil, nil, err
	}
	body, err = compatInjectBody(body, profile, metadata)
	if err != nil {
		return nil, nil, err
	}
	applyAnthropicCompatHeaders(out, plan)
	return body, out, nil
}

// testAnthropicCompatConnection sends two ordinary-client turns directly to the
// account upstream. The adapter and response validator have no provenance store.
func (s *AccountTestService) testAnthropicCompatConnection(c *gin.Context, account *Account, model, apiURL, token string, profile *AnthropicCompatProfile) error {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	start := TestEvent{Type: "test_start", Model: model, ActualAccountID: account.ID, TestScope: "ordinary_client_compatibility"}
	if profile != nil {
		start.CompatProfile = "claude_code"
	}
	s.sendEvent(c, start)

	messages := []map[string]any{{"role": "user", "content": "Think briefly, then call compat_ping with no arguments to check the connection."}}
	payload := map[string]any{
		"model": model, "max_tokens": 4096, "stream": true,
		"tools":    []map[string]any{{"name": "compat_ping", "description": "Check the connection; takes no arguments.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}}},
		"thinking": map[string]any{"type": "enabled", "budget_tokens": 1024},
	}
	if spec, ok := claude.Claude55(model); ok {
		payload["thinking"] = map[string]any{"type": "adaptive"}
		payload["output_config"] = map[string]any{"effort": spec.DefaultEffort}
	}
	if profile == nil {
		// Explicit compatibility mode on an off account remains the existing
		// one-turn ordinary-client connectivity control.
		messages = []map[string]any{{"role": "user", "content": "Reply with OK."}}
		payload = map[string]any{"model": model, "max_tokens": 64, "stream": true}
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", "XY2API-account-diagnostic/1.0")
	headers.Set("anthropic-version", "2023-06-01")
	account.ApplyHeaderOverrides(headers)
	// One isolated anchor is used for both turns, independent of admin sessions.
	headers.Set("X-Claude-Code-Session-Id", uuid.NewString())
	fail := func(code string, err error) error {
		if profile == nil {
			return s.sendErrorAndEnd(c, err.Error())
		}
		s.sendEvent(c, TestEvent{Type: "compat_step", Code: code, Status: "failed", Error: err.Error()})
		logAccountTestError(c, err.Error())
		s.sendEvent(c, TestEvent{Type: "test_complete", Error: err.Error()})
		return err
	}
	for _, code := range []string{"adapted_turn", "signed_continuation"} {
		payload["messages"] = messages
		body, err := json.Marshal(payload)
		if err != nil {
			return fail(code, err)
		}
		adapted, adaptedHeaders, err := BuildAnthropicCompatDiagnosticRequest(account, body, headers)
		if err != nil {
			return fail(code, err)
		}
		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, apiURL, bytes.NewReader(adapted))
		if err != nil {
			return fail(code, err)
		}
		req.Header = adaptedHeaders
		setAnthropicAPIKeyAuthHeader(req.Header, account, token, account.GetBaseURL())
		proxyURL := ""
		if account.ProxyID != nil && account.Proxy != nil {
			proxyURL = account.Proxy.URL()
		}
		resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
		if err != nil {
			return fail(code, fmt.Errorf("Request failed: %w", err))
		}
		content, reason, err := s.readAnthropicCompatDiagnosticTurn(c, resp)
		if err != nil {
			return fail(code, err)
		}
		if profile == nil {
			break
		}
		if code == "signed_continuation" {
			if reason != "end_turn" {
				return fail(code, fmt.Errorf("Signed continuation did not complete normally: %s", reason))
			}
			s.sendEvent(c, TestEvent{Type: "compat_step", Code: code, Status: "passed"})
			break
		}
		signed := false
		results := []map[string]any{}
		for _, block := range content {
			switch gjson.GetBytes(block, "type").String() {
			case "thinking":
				signed = signed || gjson.GetBytes(block, "signature").String() != ""
			case "redacted_thinking":
				signed = signed || gjson.GetBytes(block, "data").String() != ""
			case "tool_use":
				id := gjson.GetBytes(block, "id").String()
				if id == "" {
					return fail(code, fmt.Errorf("Diagnostic tool call has no ID"))
				}
				results = append(results, map[string]any{"type": "tool_result", "tool_use_id": id, "content": "OK"})
			}
		}
		if !signed {
			return fail(code, fmt.Errorf("Adapted turn returned no signed thinking history"))
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": content})
		if len(results) > 0 {
			messages = append(messages, map[string]any{"role": "user", "content": results})
		} else {
			// Some models ignore a tool request. Still exercise acceptance of
			// the entire signed assistant history with a normal follow-up.
			messages = append(messages, map[string]any{"role": "user", "content": "Confirm the connection with OK; do not call any tools."})
		}
		s.sendEvent(c, TestEvent{Type: "compat_step", Code: code, Status: "passed"})
	}
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

// readAnthropicCompatDiagnosticTurn retains every assistant block, including
// unknown block fields. Only streamed fields are assembled; no signed content is
// rewritten. Validation is local to diagnostics, independent of gateway observers.
func (s *AccountTestService) readAnthropicCompatDiagnosticTurn(c *gin.Context, resp *http.Response) ([]json.RawMessage, string, error) {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("API returned %d: %s", resp.StatusCode, body)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 32*1024), 8<<20)
	blocks := []json.RawMessage{}
	partial := make(map[int]string)
	open := make(map[int]string)
	started := false
	eventName := ""
	var dataLines []string
	eventBytes := 0
	reason := ""
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		eventBytes += len(line)
		if eventBytes > 8<<20 {
			return nil, "", fmt.Errorf("Diagnostic SSE event is too large")
		}
		if line != "" {
			if strings.HasPrefix(line, "event:") {
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
			continue
		}
		data := []byte(strings.Join(dataLines, "\n"))
		name := eventName
		dataLines, eventName, eventBytes = nil, "", 0
		if len(data) == 0 {
			continue
		}
		if !gjson.ValidBytes(data) {
			return nil, "", fmt.Errorf("Invalid diagnostic SSE JSON")
		}
		kind := gjson.GetBytes(data, "type").String()
		if name != "" && name != kind {
			return nil, "", fmt.Errorf("Diagnostic SSE event type mismatch")
		}
		index := int(gjson.GetBytes(data, "index").Int())
		if strings.HasPrefix(kind, "content_block_") {
			value := gjson.GetBytes(data, "index")
			if value.Type != gjson.Number || value.Float() != float64(index) || index < 0 || !started {
				return nil, "", fmt.Errorf("Invalid diagnostic content block index")
			}
		}

		switch kind {
		case "message_start":
			if started {
				return nil, "", fmt.Errorf("Duplicate diagnostic message start")
			}
			started = true
			for _, block := range gjson.GetBytes(data, "message.content").Array() {
				blocks = append(blocks, json.RawMessage(block.Raw))
			}
		case "content_block_start":
			if !gjson.GetBytes(data, "content_block").IsObject() {
				return nil, "", fmt.Errorf("Invalid diagnostic content block")
			}
			if index != len(blocks) {
				return nil, "", fmt.Errorf("Invalid diagnostic content block order")
			}
			open[index] = gjson.GetBytes(data, "content_block.type").String()
			blocks = append(blocks, json.RawMessage(gjson.GetBytes(data, "content_block").Raw))
			if text := gjson.GetBytes(blocks[index], "text").String(); text != "" {
				s.sendEvent(c, TestEvent{Type: "content", Text: text})
			}
		case "content_block_delta":
			if index < 0 || index >= len(blocks) || open[index] == "" {
				return nil, "", fmt.Errorf("Diagnostic delta has no content block")
			}
			delta := gjson.GetBytes(data, "delta")
			field := ""
			switch delta.Get("type").String() {
			case "text_delta":
				field = "text"
			case "thinking_delta":
				field = "thinking"
			case "signature_delta":
				field = "signature"
			case "input_json_delta":
				partial[index] += delta.Get("partial_json").String()
			default:
				return nil, "", fmt.Errorf("Unsupported diagnostic content delta: %s", delta.Get("type").String())
			}
			if (field == "text" && open[index] != "text") || ((field == "thinking" || field == "signature") && open[index] != "thinking") || (delta.Get("type").String() == "input_json_delta" && open[index] != "tool_use") {
				return nil, "", fmt.Errorf("Diagnostic delta does not match its content block")
			}
			if field != "" {
				value := delta.Get(field).String()
				updated, err := sjson.SetBytes(blocks[index], field, gjson.GetBytes(blocks[index], field).String()+value)
				if err != nil {
					return nil, "", err
				}
				blocks[index] = updated
				if field == "text" {
					s.sendEvent(c, TestEvent{Type: "content", Text: value})
				}
			}
		case "content_block_stop":
			if open[index] == "" {
				return nil, "", fmt.Errorf("Diagnostic stop has no open content block")
			}
			delete(open, index)
			if value, exists := partial[index]; exists {
				if !gjson.Valid(value) || !gjson.Parse(value).IsObject() {
					return nil, "", fmt.Errorf("Incomplete diagnostic tool arguments")
				}
				updated, err := sjson.SetRawBytes(blocks[index], "input", []byte(value))
				if err != nil {
					return nil, "", err
				}
				blocks[index] = updated
				delete(partial, index)
			}
		case "message_delta":
			if value := gjson.GetBytes(data, "delta.stop_reason").String(); value != "" {
				reason = value
			}
		case "error":
			return nil, "", fmt.Errorf("Upstream diagnostic error: %s", gjson.GetBytes(data, "error.message").String())
		case "message_stop":
			if !started || reason == "" || len(open) > 0 {
				return nil, "", fmt.Errorf("Incomplete Anthropic diagnostic terminal event")
			}
			return blocks, reason, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, "", fmt.Errorf("Diagnostic stream read error: %w", err)
	}
	return nil, "", fmt.Errorf("Compatibility diagnostic ended without a complete Anthropic terminal event")
}
