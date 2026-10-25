package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// Resolve the actual write marker once per response attempt, independently of
// observability and before any response scanning or allocation.
func openAIUpstreamIdentityResponseApplied(c *gin.Context, account *Account) bool {
	if !CachePolicyFromGin(c).UpstreamIdentity().Enabled() {
		return false
	}
	value, _ := c.Get(openAIUpstreamIdentityWireGinKey)
	wire, ok := value.(openAIUpstreamIdentityWire)
	return ok && account != nil && wire.accountID == account.ID && wire.uuid != ""
}

// Only an actual L-01 write enables restoration. Preserve each member and its
// position, replacing the injected value with the no-key null response shape.
// Never recurse into user output or tool content; an absent member stays absent.
func restoreOpenAIUpstreamIdentityResponse(body []byte) ([]byte, error) {
	nullKeys := func(input []byte) ([]byte, error) {
		var out []byte
		last := 0
		var invalid bool
		gjson.ParseBytes(input).ForEach(func(key, value gjson.Result) bool {
			if key.Str != "prompt_cache_key" || value.Raw == "null" {
				return true
			}
			end := value.Index + len(value.Raw)
			if value.Index < last || end > len(input) || len(value.Raw) == 0 {
				invalid = true
				return false
			}
			if out == nil {
				out = make([]byte, 0, len(input))
			}
			out = append(out, input[last:value.Index]...)
			out = append(out, "null"...)
			last = end
			return true
		})
		if invalid {
			return nil, fmt.Errorf("restore upstream response identity: invalid response object")
		}
		if out == nil {
			return input, nil
		}
		return append(out, input[last:]...), nil
	}
	body, err := nullKeys(body)
	if err != nil {
		return nil, err
	}
	// Visit every response member, including ambiguous duplicate envelopes.
	// A last-member-wins client must not recover an unvisited injected value.
	var out []byte
	last := 0
	gjson.ParseBytes(body).ForEach(func(key, value gjson.Result) bool {
		if key.Str != "response" || !value.IsObject() {
			return true
		}
		var restored []byte
		restored, err = nullKeys([]byte(value.Raw))
		if err != nil {
			return false
		}
		if string(restored) != value.Raw {
			if out == nil {
				out = make([]byte, 0, len(body))
			}
			out = append(out, body[last:value.Index]...)
			out = append(out, restored...)
			last = value.Index + len(value.Raw)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if out != nil {
		return append(out, body[last:]...), nil
	}
	return body, nil
}

// Restore complete SSE events, including JSON split across multiple data
// fields. Unchanged events keep every byte; changed multiline JSON is emitted
// as one compact data field while comments/event/id/retry lines are preserved.
func restoreOpenAIUpstreamIdentitySSE(body []byte) ([]byte, error) {
	var out []byte
	for start := 0; start < len(body); {
		end := start
		var payloads [][]byte
		var lines [][]byte
		for end < len(body) {
			next := bytes.IndexByte(body[end:], '\n')
			if next < 0 {
				next = len(body)
			} else {
				next += end + 1
			}
			line := body[end:next]
			lines = append(lines, line)
			end = next
			if bytes.HasPrefix(line, []byte("data:")) {
				payloads = append(payloads, bytes.TrimSpace(line[5:]))
			}
			if len(bytes.TrimSpace(line)) == 0 {
				break
			}
		}
		joined := bytes.Join(payloads, []byte("\n"))
		if len(joined) > 0 && !bytes.Equal(joined, []byte("[DONE]")) && !gjson.ValidBytes(joined) {
			return nil, fmt.Errorf("invalid SSE JSON")
		}
		restored, err := restoreOpenAIUpstreamIdentityResponse(joined)
		if err != nil {
			return nil, err
		}
		if bytes.Equal(restored, joined) {
			out = append(out, body[start:end]...)
		} else {
			if len(payloads) > 1 {
				var compact bytes.Buffer
				if err := json.Compact(&compact, restored); err != nil {
					return nil, fmt.Errorf("restore upstream response identity: invalid SSE event")
				}
				restored = compact.Bytes()
			}
			written := false
			for _, line := range lines {
				if !bytes.HasPrefix(line, []byte("data:")) {
					out = append(out, line...)
					continue
				}
				if written {
					continue
				}
				payload := bytes.TrimSpace(line[5:])
				offset := 5 + bytes.Index(line[5:], payload)
				out = append(out, line[:offset]...)
				out = append(out, restored...)
				out = append(out, line[offset+len(payload):]...)
				written = true
			}
		}
		start = end
	}
	return out, nil
}

// Literal fallback uses only the UUID written by this attempt. Embedded UUIDs
// are removed as bytes as well (including escaped quotes in error.message).
func literalOpenAIUpstreamIdentity(body []byte, uuid string) []byte {
	return literalOpenAIUpstreamIdentityPrefix(body, uuid, len(body), false, true)
}

// Only a complete, unescaped string value becomes null. An object key or an
// embedded UUID loses just the UUID bytes, retaining valid JSON syntax. Partial
// SSE output can inspect its retained suffix; ambiguous strings stay quoted.
func literalOpenAIUpstreamIdentityPrefix(body []byte, uuid string, cut int, prefixEscape, final bool) []byte {
	if uuid == "" || !bytes.Contains(body[:cut], []byte(uuid)) {
		return body[:cut]
	}
	out := make([]byte, 0, cut)
	last, search := 0, 0
	for search < cut {
		i := bytes.Index(body[search:cut], []byte(uuid))
		if i < 0 {
			break
		}
		i += search
		end := i + len(uuid)
		start, next := i, end
		nullValue := i > 0 && body[i-1] == '"' && end < cut && body[end] == '"'
		if nullValue {
			escaped := false
			j := i - 2
			for j >= 0 && body[j] == '\\' {
				escaped = !escaped
				j--
			}
			if j < 0 && prefixEscape {
				escaped = !escaped
			}
			j = end + 1
			for {
				for j < len(body) && (body[j] == ' ' || body[j] == '\t' || body[j] == '\n' || body[j] == '\r') {
					j++
				}
				// An oversize SSE JSON key may put its colon in the next data
				// field. These field prefixes are not part of the JSON payload.
				if !bytes.HasPrefix(body[j:], []byte("data:")) {
					break
				}
				j += len("data:")
			}
			nullValue = !escaped && (j < len(body) && body[j] != ':' || j == len(body) && final)
		}
		if nullValue {
			start, next = i-1, end+1
		}
		out = append(out, body[last:start]...)
		if nullValue {
			out = append(out, "null"...)
		}
		last, search = next, next
	}
	return append(out, body[last:cut]...)
}

func openAIUpstreamIdentityUUID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, _ := c.Get(openAIUpstreamIdentityWireGinKey)
	wire, _ := value.(openAIUpstreamIdentityWire)
	return wire.uuid
}

func recordOpenAIUpstreamIdentityRestoreFallback(c *gin.Context, kind string) {
	if kind == "none" || c == nil {
		return
	}
	traceKind := kind
	if kind == "error_body" {
		traceKind = "literal"
	}
	value, _ := c.Get(openAIUpstreamIdentityWireGinKey)
	wire, ok := value.(openAIUpstreamIdentityWire)
	if !ok || wire.restore == nil {
		return
	}
	// At most once per kind per attempt. Oversize dominates the trace enum.
	bit := uint32(1)
	if kind == "sse_oversize" { //nolint:staticcheck // QF1003: restore-only path kept byte-identical to the verified candidate; tagged switch deferred
		bit = 2
	} else if kind == "error_body" {
		bit = 4
	}
	old := wire.restore.fallback.Or(bit)
	if old&bit == 0 {
		resilience.Count("l01_restore_fallback_total", 0, 0, kind)
	}
	if t := cacheTraceFromGin(c); t != nil {
		t.mu.Lock()
		if t.data.L01Applied && t.data.L01RestoreFallback != "sse_oversize" {
			t.data.L01RestoreFallback = traceKind
		}
		t.mu.Unlock()
	}
}

func restoreOpenAIUpstreamIdentityLiteralForClient(c *gin.Context, body []byte, kind string) []byte {
	restored := literalOpenAIUpstreamIdentity(body, openAIUpstreamIdentityUUID(c))
	if !bytes.Equal(restored, body) {
		recordOpenAIUpstreamIdentityRestoreFallback(c, kind)
	}
	return restored
}

// Rule writers receive an already-decoded message, not a JSON document. Remove
// U as text so quotes around an embedded echo retain the baseline message shape.
func restoreOpenAIUpstreamIdentityErrorMessage(c *gin.Context, message string) string {
	restored := strings.ReplaceAll(message, openAIUpstreamIdentityUUID(c), "")
	if restored != message {
		recordOpenAIUpstreamIdentityRestoreFallback(c, "error_body")
	}
	return restored
}

// This wrapper never returns an error to forwarding or resilience. Structural
// parsing is optional: invalid objects and unknown envelopes use known-U bytes.
func restoreOpenAIUpstreamIdentityForClient(c *gin.Context, body []byte, sse bool) []byte {
	var restored []byte
	var err error
	if sse {
		restored, err = restoreOpenAIUpstreamIdentitySSE(body)
	} else if gjson.ValidBytes(body) {
		restored, err = restoreOpenAIUpstreamIdentityResponse(body)
	} else {
		err = fmt.Errorf("invalid response JSON")
	}
	if err != nil {
		recordOpenAIUpstreamIdentityRestoreFallback(c, "literal")
		return literalOpenAIUpstreamIdentity(body, openAIUpstreamIdentityUUID(c))
	}
	// Also covers unexpected envelopes and error strings left by structured work.
	return restoreOpenAIUpstreamIdentityLiteralForClient(c, restored, "literal")
}

// This buffer runs only for actually injected requests. Oversize events switch
// to bounded literal streaming, retaining a suffix so no UUID can cross a write.
// The billing/classification scanner and every disabled writer stay unchanged.
type openAIUpstreamIdentitySSEBuffer struct {
	pending           []byte
	lineNonWhitespace bool
	oversize          bool
	limit             int
	uuid              string
	context           *gin.Context
	literalEscape     bool
}

func (b *openAIUpstreamIdentitySSEBuffer) drainLiteral(final bool) []byte {
	cut := len(b.pending)
	if !final && b.uuid != "" {
		cut -= len(b.uuid) + 2
		if cut <= 0 {
			return nil
		}
		for _, token := range []string{`"` + b.uuid + `"`, b.uuid} {
			for start := 0; start < cut; {
				i := bytes.Index(b.pending[start:], []byte(token))
				if i < 0 {
					break
				}
				i += start
				if i < cut && i+len(token) > cut {
					cut = i
					break
				}
				start = i + len(token)
			}
		}
	}
	output := literalOpenAIUpstreamIdentityPrefix(b.pending, b.uuid, cut, b.literalEscape, final)
	// Copy before reusing the backing array: output may alias pending unchanged.
	output = append([]byte(nil), output...)
	for _, char := range b.pending[:cut] {
		if char == '\\' {
			b.literalEscape = !b.literalEscape
		} else {
			b.literalEscape = false
		}
	}
	b.pending = append(b.pending[:0], b.pending[cut:]...)
	return output
}

func (b *openAIUpstreamIdentitySSEBuffer) Append(value string) ([]byte, error) {
	var output []byte
	for len(value) > 0 {
		n := strings.IndexByte(value, '\n')
		completeLine := n >= 0
		if completeLine {
			n++
		} else {
			n = len(value)
		}
		segment := value[:n]
		blank := !b.lineNonWhitespace && strings.TrimSpace(segment) == ""
		if strings.TrimSpace(segment) != "" {
			b.lineNonWhitespace = true
		}
		b.pending = append(b.pending, segment...)
		value = value[n:]
		if !b.oversize && b.limit > 0 && len(b.pending) > b.limit {
			b.oversize = true
			recordOpenAIUpstreamIdentityRestoreFallback(b.context, "sse_oversize")
		}
		if b.oversize {
			output = append(output, b.drainLiteral(false)...)
		}
		if completeLine {
			b.lineNonWhitespace = false
			if blank {
				frame, _ := b.Finish()
				output = append(output, frame...)
			}
		}
	}
	return output, nil
}

func (b *openAIUpstreamIdentitySSEBuffer) Finish() ([]byte, error) {
	if len(b.pending) == 0 {
		b.oversize = false
		b.lineNonWhitespace = false
		b.literalEscape = false
		return nil, nil
	}
	var restored []byte
	if b.oversize {
		restored = b.drainLiteral(true)
	} else if b.context != nil {
		restored = restoreOpenAIUpstreamIdentityForClient(b.context, b.pending, true)
	} else {
		// Standalone callers have the same no-error contract.
		var err error
		restored, err = restoreOpenAIUpstreamIdentitySSE(b.pending)
		if err != nil {
			restored = literalOpenAIUpstreamIdentity(b.pending, b.uuid)
		}
	}
	b.pending = nil
	b.lineNonWhitespace = false
	b.oversize = false
	b.literalEscape = false
	return restored, nil
}

// Restore before the Responses-to-Chat converters consume any event. This is
// attempt-local and installed only after a real L-01 write. Buffering and literal
// fallback are shared with the native writer, including EOF and oversize events;
// only errors from the original upstream reader reach the compatibility parser.
type openAIUpstreamIdentitySSEBody struct {
	io.ReadCloser
	buffer  openAIUpstreamIdentitySSEBuffer
	ready   []byte
	readErr error
	scratch [4096]byte
}

func (s *OpenAIGatewayService) restoreOpenAIUpstreamIdentityChatBody(c *gin.Context, body io.ReadCloser) *openAIUpstreamIdentitySSEBody {
	limit := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		limit = s.cfg.Gateway.MaxLineSize
	}
	return &openAIUpstreamIdentitySSEBody{ReadCloser: body, buffer: openAIUpstreamIdentitySSEBuffer{
		limit: limit, uuid: openAIUpstreamIdentityUUID(c), context: c,
	}}
}

func (b *openAIUpstreamIdentitySSEBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(b.ready) == 0 && b.readErr == nil {
		n, err := b.ReadCloser.Read(b.scratch[:])
		if n > 0 {
			b.ready, _ = b.buffer.Append(string(b.scratch[:n]))
		}
		if err != nil {
			// Flush even on a non-EOF read error, then preserve that exact error.
			tail, _ := b.buffer.Finish()
			b.ready = append(b.ready, tail...)
			b.readErr = err
		}
		if n == 0 && err == nil {
			return 0, nil
		}
	}
	if len(b.ready) > 0 {
		n := copy(p, b.ready)
		b.ready = b.ready[n:]
		return n, nil
	}
	return 0, b.readErr
}
