package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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

func anthropicCompatDiagnosticThinking(model string) (map[string]any, map[string]any) {
	if isClaudeVersionAtLeast(model, 4, 6) {
		return map[string]any{"type": "adaptive"}, map[string]any{"effort": "high"}
	}
	return map[string]any{"type": "enabled", "budget_tokens": 1024}, nil
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

	messages := []map[string]any{{"role": "user", "content": "Calculate 17 × 23, then call compat_ping with no arguments to check the connection."}}
	thinking, outputConfig := anthropicCompatDiagnosticThinking(model)
	payload := map[string]any{
		"model": model, "max_tokens": 4096, "stream": true,
		"tools":    []map[string]any{{"name": "compat_ping", "description": "Check the connection; takes no arguments.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}}},
		"thinking": thinking,
	}
	if outputConfig != nil {
		payload["output_config"] = outputConfig
	}
	if profile == nil {
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
		s.sendAnthropicCompatDiagnosticComplete(c, false, err.Error())
		return err
	}
	sendTurn := func() ([]json.RawMessage, error) {
		payload["messages"] = messages
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		adapted, adaptedHeaders, err := BuildAnthropicCompatDiagnosticRequest(account, body, headers)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, apiURL, bytes.NewReader(adapted))
		if err != nil {
			return nil, err
		}
		req.Header = adaptedHeaders
		setAnthropicAPIKeyAuthHeader(req.Header, account, token, account.GetBaseURL())
		proxyURL := ""
		if account.ProxyID != nil && account.Proxy != nil {
			proxyURL = account.Proxy.URL()
		}
		resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
		if err != nil {
			return nil, fmt.Errorf("Request failed: %w", err)
		}
		return s.readAnthropicCompatDiagnosticTurn(c, resp)
	}
	if profile == nil {
		if _, err := sendTurn(); err != nil {
			return fail("adapted_turn", err)
		}
		s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
		return nil
	}
	content, err := sendTurn()
	if err != nil {
		return fail("adapted_turn", err)
	}
	signed, thinkingReturned := false, false
	results := []map[string]any{}
	for _, block := range content {
		switch gjson.GetBytes(block, "type").String() {
		case "thinking":
			thinkingReturned = true
			signed = signed || gjson.GetBytes(block, "signature").String() != ""
		case "redacted_thinking":
			thinkingReturned = true
			signed = signed || gjson.GetBytes(block, "data").String() != ""
		case "tool_use":
			id := gjson.GetBytes(block, "id").String()
			if id == "" {
				return fail("adapted_turn", fmt.Errorf("Diagnostic tool call has no ID"))
			}
			results = append(results, map[string]any{"type": "tool_result", "tool_use_id": id, "content": "OK"})
		}
	}
	if !thinkingReturned {
		return fail("adapted_turn", fmt.Errorf("模型本次没有返回 thinking"))
	}
	if !signed {
		return fail("adapted_turn", fmt.Errorf("Model returned thinking without signed history"))
	}
	messages = append(messages, map[string]any{"role": "assistant", "content": content})
	if len(results) > 0 {
		messages = append(messages, map[string]any{"role": "user", "content": results})
	} else {
		messages = append(messages, map[string]any{"role": "user", "content": "Confirm the connection with OK; do not call any tools."})
	}
	s.sendEvent(c, TestEvent{Type: "compat_step", Code: "adapted_turn", Status: "passed"})
	if _, err := sendTurn(); err != nil {
		return fail("signed_continuation", err)
	}
	s.sendEvent(c, TestEvent{Type: "compat_step", Code: "signed_continuation", Status: "passed"})
	s.sendAnthropicCompatDiagnosticComplete(c, true, "")
	return nil
}

func (s *AccountTestService) sendAnthropicCompatDiagnosticComplete(c *gin.Context, success bool, message string) {
	if suppress, ok := c.Get(accountTestSuppressCompletionContextKey); ok {
		if suppressCompletion, _ := suppress.(bool); suppressCompletion {
			return
		}
	}
	event := map[string]any{"type": "test_complete", "success": success}
	if message != "" {
		event["error"] = message
	}
	payload, _ := json.Marshal(event)
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", payload); err != nil {
		return
	}
	c.Writer.Flush()
}

// readAnthropicCompatDiagnosticTurn retains assistant blocks and validates the
// terminal event without depending on gateway response observers.
func (s *AccountTestService) readAnthropicCompatDiagnosticTurn(c *gin.Context, resp *http.Response) ([]json.RawMessage, error) {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, body)
	}
	stream, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("Diagnostic stream read error: %w", err)
	}
	if len(stream) > 8<<20 {
		return nil, fmt.Errorf("Diagnostic SSE stream is too large")
	}
	blocks := []json.RawMessage{}
	omit := make(map[int]bool)
	started, stopped := false, false
	reason := ""
	activeBlock := -1
	var partialJSON string
	var consumeErr error
	forEachOpenAISSEFrameWithRawEvent(string(stream), func(event string, data []byte) {
		if consumeErr != nil || stopped {
			return
		}
		if !gjson.ValidBytes(data) {
			consumeErr = fmt.Errorf("Invalid diagnostic SSE JSON")
			return
		}
		kind := gjson.GetBytes(data, "type").String()
		if kind == "" {
			kind = event
		}
		index := int(gjson.GetBytes(data, "index").Int())
		if strings.HasPrefix(kind, "content_block_") && (!started || index < 0) {
			consumeErr = fmt.Errorf("Invalid diagnostic content block index")
			return
		}
		switch kind {
		case "message_start":
			started = true
			for _, block := range gjson.GetBytes(data, "message.content").Array() {
				blocks = append(blocks, json.RawMessage(block.Raw))
			}
		case "content_block_start":
			block := gjson.GetBytes(data, "content_block")
			if !block.IsObject() || index != len(blocks) {
				consumeErr = fmt.Errorf("Invalid diagnostic content block")
				return
			}
			activeBlock, partialJSON = index, ""
			blocks = append(blocks, json.RawMessage(block.Raw))
			if text := block.Get("text").String(); text != "" {
				s.sendEvent(c, TestEvent{Type: "content", Text: text})
			}
		case "content_block_delta":
			if index != activeBlock {
				consumeErr = fmt.Errorf("Diagnostic delta has no content block")
				return
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
				partialJSON += delta.Get("partial_json").String()
			default:
				omit[index] = true
				return
			}
			if field == "" {
				return
			}
			value := delta.Get(field).String()
			blocks[index], err = sjson.SetBytes(blocks[index], field, gjson.GetBytes(blocks[index], field).String()+value)
			if err != nil {
				consumeErr = err
				return
			}
			if field == "text" {
				s.sendEvent(c, TestEvent{Type: "content", Text: value})
			}
		case "content_block_stop":
			if index != activeBlock {
				consumeErr = fmt.Errorf("Diagnostic stop has no open content block")
				return
			}
			activeBlock = -1
			if partialJSON != "" {
				if !gjson.Valid(partialJSON) || !gjson.Parse(partialJSON).IsObject() {
					consumeErr = fmt.Errorf("Incomplete diagnostic tool arguments")
					return
				}
				blocks[index], err = sjson.SetRawBytes(blocks[index], "input", []byte(partialJSON))
				if err != nil {
					consumeErr = err
					return
				}
			}
		case "message_delta":
			reason = gjson.GetBytes(data, "delta.stop_reason").String()
		case "error":
			consumeErr = fmt.Errorf("Upstream diagnostic error: %s", gjson.GetBytes(data, "error.message").String())
		case "message_stop":
			if !started || reason == "" || activeBlock >= 0 {
				consumeErr = fmt.Errorf("Incomplete Anthropic diagnostic terminal event")
				return
			}
			stopped = true
		}
	})
	if consumeErr != nil {
		return nil, consumeErr
	}
	if !stopped {
		return nil, fmt.Errorf("Compatibility diagnostic ended without a complete Anthropic terminal event")
	}
	content := make([]json.RawMessage, 0, len(blocks))
	for i, block := range blocks {
		if !omit[i] {
			content = append(content, block)
		}
	}
	return content, nil
}
