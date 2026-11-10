package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The observer buffers only one SSE event, opaque signature fragments and the
// small success tail. Ordinary response content is forwarded incrementally.
type anthropicCompatResponseBody struct {
	source                      io.ReadCloser
	reader                      *bufio.Reader
	ctx                         context.Context
	service                     *GatewayService
	plan                        *anthropicCompatPlan
	limit                       int
	pending                     []byte
	tail                        []byte
	usageTail                   []byte
	opaqueBytes                 int
	terminalErr                 error
	done                        bool
	sawStart, sawReason, failed bool
	blocks                      map[int]*anthropicCompatResponseBlock
	values                      []anthropicCompatOpaque
}
type anthropicCompatResponseBlock struct {
	kind   string
	opaque strings.Builder
}

// Diagnostics share protocol completion validation, without storing synthetic
// diagnostic history in the production provenance index.
func newAnthropicCompatDiagnosticBody(ctx context.Context, source io.ReadCloser) io.ReadCloser {
	return &anthropicCompatResponseBody{source: source, reader: bufio.NewReaderSize(source, 32*1024), ctx: ctx, limit: 8 << 20, blocks: make(map[int]*anthropicCompatResponseBlock)}
}

func (s *GatewayService) observeAnthropicCompatResponse(ctx context.Context, resp *http.Response, plan *anthropicCompatPlan) {
	if plan == nil || !plan.Active || resp == nil || resp.StatusCode >= 400 {
		return
	}
	limit := min(defaultMaxLineSize, 8<<20)
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		limit = min(s.cfg.Gateway.MaxLineSize, 8<<20)
	}
	if resp.Request != nil {
		ctx = resp.Request.Context()
	}
	resp.Body = &anthropicCompatResponseBody{source: resp.Body, reader: bufio.NewReaderSize(resp.Body, 32*1024), ctx: ctx, service: s, plan: plan, limit: limit, blocks: make(map[int]*anthropicCompatResponseBlock)}
}

func (b *anthropicCompatResponseBody) Close() error { return b.source.Close() }
func (b *anthropicCompatResponseBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(b.pending) == 0 && !b.done {
		b.next()
	}
	if len(b.pending) > 0 {
		n := copy(p, b.pending)
		b.pending = b.pending[n:]
		return n, nil
	}
	if b.terminalErr != nil {
		return 0, b.terminalErr
	}
	return 0, io.EOF
}

func (b *anthropicCompatResponseBody) event() ([]byte, error) {
	var event []byte
	lineBytes := 0
	var lineFirst byte
	for {
		fragment, err := b.reader.ReadSlice('\n')
		if len(event)+len(fragment) > b.limit {
			return nil, bufio.ErrTooLong
		}
		if lineBytes == 0 && len(fragment) > 0 {
			lineFirst = fragment[0]
		}
		lineBytes += len(fragment)
		event = append(event, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return event, err
		}
		if (lineBytes == 1 && lineFirst == '\n') || (lineBytes == 2 && lineFirst == '\r' && bytes.HasSuffix(fragment, []byte("\n"))) {
			return event, nil
		}
		lineBytes = 0
	}
}

func compatSSEPayload(event []byte) (string, []byte) {
	name := ""
	var data []string
	for _, raw := range strings.Split(string(event), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if strings.HasPrefix(line, "event:") {
			name = strings.TrimPrefix(line, "event:")
			name = strings.TrimPrefix(name, " ")
		} else if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	return name, []byte(strings.Join(data, "\n"))
}

func (b *anthropicCompatResponseBody) incomplete(err error) {
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	b.releaseHeldUsage()
	b.done = true
	b.terminalErr = err
}

func (b *anthropicCompatResponseBody) releaseHeldUsage() {
	if len(b.tail) > 0 && len(b.usageTail) > 0 {
		// Only usage is released on failure; no successful stop signal escapes.
		b.pending = append(b.pending, []byte("event: message_delta\ndata: "+string(b.usageTail)+"\n\n")...)
	}
	b.tail = nil
}

// Usage deltas are patches, including nested cache-creation details. A later
// output-only patch must not erase input/cache tokens observed in an earlier one.
func mergeCompatUsageSnapshot(previous, patch []byte, depth int) ([]byte, error) {
	if depth > 16 || len(previous)+len(patch) > 64<<10 {
		return nil, fmt.Errorf("anthropic usage snapshot exceeds its limit")
	}
	fields := make(map[string]json.RawMessage)
	if len(previous) > 0 {
		if err := json.Unmarshal(previous, &fields); err != nil {
			return nil, err
		}
	}
	var updates map[string]json.RawMessage
	if err := json.Unmarshal(patch, &updates); err != nil {
		return nil, err
	}
	for key, value := range updates {
		if len(value) > 0 && value[0] == '{' && len(fields[key]) > 0 && fields[key][0] == '{' {
			merged, err := mergeCompatUsageSnapshot(fields[key], value, depth+1)
			if err != nil {
				return nil, err
			}
			fields[key] = merged
		} else {
			fields[key] = value
		}
	}
	return json.Marshal(fields)
}

func (b *anthropicCompatResponseBody) observeUsage(payload []byte) error {
	patch := gjson.GetBytes(payload, "usage")
	if !patch.Exists() {
		return nil
	}
	if !patch.IsObject() {
		return fmt.Errorf("invalid Anthropic usage patch")
	}
	usage, err := mergeCompatUsageSnapshot([]byte(gjson.GetBytes(b.usageTail, "usage").Raw), []byte(patch.Raw), 0)
	if err != nil {
		return err
	}
	b.usageTail, err = sjson.SetRawBytes([]byte(`{"type":"message_delta","delta":{}}`), "usage", usage)
	return err
}

func (b *anthropicCompatResponseBody) next() {
	event, readErr := b.event()
	if len(event) == 0 {
		if readErr == nil {
			readErr = io.ErrUnexpectedEOF
		}
		if b.failed && errors.Is(readErr, io.EOF) {
			b.done = true
			return
		}
		b.incomplete(readErr)
		return
	}
	eventName, payload := compatSSEPayload(event)
	if len(payload) > 0 && !gjson.ValidBytes(payload) {
		b.incomplete(fmt.Errorf("invalid Anthropic SSE JSON"))
		return
	}
	typeName := gjson.GetBytes(payload, "type").String()
	if eventName == "error" || gjson.GetBytes(payload, "error").Exists() {
		b.failed = true
	}
	if eventName != "" && typeName != "" && eventName != typeName {
		b.incomplete(fmt.Errorf("anthropic SSE event type mismatch"))
		return
	}
	if typeName == "" && eventName == "error" {
		typeName = "error"
	}
	if len(payload) > 0 && typeName != "" {
		// Downstream protocol converters consume one JSON data line per event.
		// Normalize multiline, data-only and comment-interleaved SSE here.
		var compact bytes.Buffer
		if err := json.Compact(&compact, payload); err != nil {
			b.incomplete(err)
			return
		}
		event = []byte("event: " + typeName + "\ndata: " + compact.String() + "\n\n")
	}
	if typeName == "message_stop" && readErr != nil {
		b.incomplete(io.ErrUnexpectedEOF)
		return
	}
	switch typeName {
	case "message_start":
		b.sawStart = true
	case "error":
		b.failed = true
		b.releaseHeldUsage()
	case "content_block_start":
		if len(b.blocks) >= anthropicCompatSignatureLimit {
			b.incomplete(compatUnavailable("Upstream response has too many open content blocks"))
			return
		}
		index := int(gjson.GetBytes(payload, "index").Int())
		if _, exists := b.blocks[index]; exists {
			b.incomplete(fmt.Errorf("invalid duplicate Anthropic content block"))
			return
		}
		block := &anthropicCompatResponseBlock{kind: gjson.GetBytes(payload, "content_block.type").String()}
		switch block.kind {
		case "thinking":
			_, _ = block.opaque.WriteString(gjson.GetBytes(payload, "content_block.signature").String())
		case "redacted_thinking":
			_, _ = block.opaque.WriteString(gjson.GetBytes(payload, "content_block.data").String())
		}
		b.opaqueBytes += block.opaque.Len()
		if b.opaqueBytes > anthropicCompatOpaqueBytesLimit {
			b.incomplete(compatUnavailable("Signed response exceeds the gateway opaque byte limit"))
			return
		}
		b.blocks[index] = block
	case "content_block_delta":
		if gjson.GetBytes(payload, "delta.type").String() == "signature_delta" {
			index := int(gjson.GetBytes(payload, "index").Int())
			block := b.blocks[index]
			if block == nil || block.kind != "thinking" {
				b.incomplete(fmt.Errorf("anthropic signature delta has no open thinking block"))
				return
			}
			b.opaqueBytes += len(gjson.GetBytes(payload, "delta.signature").String())
			if b.opaqueBytes > anthropicCompatOpaqueBytesLimit {
				b.incomplete(compatUnavailable("Signed response exceeds the gateway opaque byte limit"))
				return
			}
			_, _ = block.opaque.WriteString(gjson.GetBytes(payload, "delta.signature").String())
		}
	case "content_block_stop":
		index := int(gjson.GetBytes(payload, "index").Int())
		block := b.blocks[index]
		if block == nil {
			b.incomplete(fmt.Errorf("anthropic content block stop has no open block"))
			return
		}
		if block.opaque.Len() > 0 {
			if len(b.values) >= anthropicCompatSignatureLimit {
				b.incomplete(compatUnavailable("Signed response exceeds the gateway provenance limit"))
				return
			}
			b.values = append(b.values, anthropicCompatOpaque{block.kind, block.opaque.String()})
		}
		delete(b.blocks, index)
	case "message_delta":
		if err := b.observeUsage(payload); err != nil {
			b.incomplete(err)
			return
		}
		if gjson.GetBytes(payload, "delta.stop_reason").String() != "" {
			b.sawReason = true
			b.tail = append(b.tail, event...)
			if len(b.tail) > 64<<10 {
				b.incomplete(bufio.ErrTooLong)
			}
			if readErr != nil {
				b.incomplete(io.ErrUnexpectedEOF)
			}
			return
		}
	case "message_stop":
		if !b.sawStart || !b.sawReason || b.failed || len(b.blocks) != 0 {
			b.incomplete(fmt.Errorf("incomplete Anthropic successful terminal"))
			return
		}
		if b.plan != nil {
			if err := b.service.registerAnthropicCompat(b.ctx, b.plan, b.values); err != nil {
				b.incomplete(err)
				return
			}
		}
		b.pending = append(b.pending, b.tail...)
		b.tail = nil
		b.pending = append(b.pending, event...)
		b.done = true
		return
	}
	if len(b.tail) > 0 {
		b.tail = append(b.tail, event...)
		if len(b.tail) > 64<<10 {
			b.incomplete(bufio.ErrTooLong)
			return
		}
	} else {
		b.pending = append(b.pending, event...)
	}
	if readErr != nil {
		if b.failed && errors.Is(readErr, io.EOF) {
			b.done = true
		} else {
			b.incomplete(io.ErrUnexpectedEOF)
		}
	}
}

func anthropicCompatObserved(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	_, ok := resp.Body.(*anthropicCompatResponseBody)
	return ok
}

func (s *GatewayService) commitAnthropicCompatJSON(ctx context.Context, cPlan *anthropicCompatPlan, body []byte) error {
	if cPlan == nil || !cPlan.Active {
		return nil
	}
	if !gjson.ValidBytes(body) || gjson.GetBytes(body, "type").String() != "message" || gjson.GetBytes(body, "stop_reason").String() == "" {
		return fmt.Errorf("incomplete Anthropic non-streaming response")
	}
	values := make([]anthropicCompatOpaque, 0)
	opaqueBytes := 0
	var limitErr error
	gjson.GetBytes(body, "content").ForEach(func(_, block gjson.Result) bool {
		kind := block.Get("type").String()
		value := ""
		switch kind {
		case "thinking":
			value = block.Get("signature").String()
		case "redacted_thinking":
			value = block.Get("data").String()
		}
		if value != "" {
			opaqueBytes += len(value)
			if opaqueBytes > anthropicCompatOpaqueBytesLimit || len(values) >= anthropicCompatSignatureLimit {
				limitErr = compatUnavailable("Signed response exceeds the gateway provenance limits")
				return false
			}
			values = append(values, anthropicCompatOpaque{kind, value})
		}
		return true
	})
	if limitErr != nil {
		return limitErr
	}
	return s.registerAnthropicCompat(ctx, cPlan, values)
}
