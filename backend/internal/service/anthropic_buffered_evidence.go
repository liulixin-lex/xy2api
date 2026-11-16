package service

import (
	"errors"
	"fmt"
	"io"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
)

// Evidence comes from a complete upstream data payload, before typed decoding.
// Once an authoritative stop arrived, a transport close cannot undo it.
type anthropicBufferedEvidence struct {
	usagePresent  bool
	terminalSeen  bool
	terminalValid bool
	stopped       bool
	err           error
}

func (e *anthropicBufferedEvidence) observe(name, payload string) {
	if payload == "[DONE]" {
		e.terminalSeen, e.terminalValid, e.stopped = true, true, true
		return
	}
	if !gjson.Valid(payload) {
		return // Non-JSON keepalives do not invalidate a later authoritative stop.
	}
	root := gjson.Parse(payload)
	kind := root.Get("type").String()
	if kind == "" {
		kind = name
	}
	for _, path := range []string{"message.usage", "usage"} {
		u := root.Get(path)
		for _, field := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
			v := u.Get(field)
			if v.Type == gjson.Number && v.Num >= 0 {
				e.usagePresent = true
			}
		}
	}
	if e.stopped {
		return
	}
	if kind == "error" {
		e.terminalSeen, e.terminalValid = true, root.Get("error").IsObject()
		e.err = fmt.Errorf("anthropic upstream error: %s", extractUpstreamErrorMessage([]byte(payload)))
		return
	}
	switch kind {
	case "message_stop":
		e.terminalSeen, e.terminalValid, e.stopped = true, true, true
	case "message_delta":
		reason := root.Get("delta.stop_reason")
		if reason.Type == gjson.String && reason.Str != "" {
			e.terminalSeen = true
			_, _, err := apicompat.ProjectTerminal("anthropic", "anthropic", "", reason.Str, true, false)
			e.terminalValid = err == nil
			e.stopped = err == nil
			e.err = err
		}
	}
}

func (e *anthropicBufferedEvidence) finish(readErr error, hasResponse bool) error {
	if e.err != nil {
		return e.err
	}
	if e.stopped && hasResponse {
		return nil
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	return &apicompat.UpstreamConversionError{Field: "stream", Reason: "missing Anthropic terminal event", SourceProtocol: "anthropic"}
}

func (e *anthropicBufferedEvidence) facts() OpenAIForwardResult {
	source := "missing"
	if e.usagePresent {
		source = "upstream_sse_progressive"
	}
	return OpenAIForwardResult{ResponseFactsKnown: true, UsagePresent: e.usagePresent, UsageSource: source,
		TerminalSeen: e.terminalSeen, TerminalValid: e.terminalValid, ResponseBodyKind: "sse"}
}
