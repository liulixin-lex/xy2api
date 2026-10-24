package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
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

// This buffer sits after the existing per-line billing/classification path.
// It owns only event bytes, never a callback capturing the existing writers:
// such a callback would move those writers to the heap even when L-01 is off.
type openAIUpstreamIdentitySSEBuffer struct {
	pending   []byte
	lineStart int
	limit     int
}

func (b *openAIUpstreamIdentitySSEBuffer) Append(value string) ([]byte, error) {
	var output []byte
	for len(value) > 0 {
		n := strings.IndexByte(value, '\n')
		if n < 0 {
			n = len(value)
		} else {
			n++
		}
		if len(b.pending)+n > b.limit {
			return nil, fmt.Errorf("upstream identity SSE event exceeds response limit")
		}
		b.pending = append(b.pending, value[:n]...)
		value = value[n:]
		if b.pending[len(b.pending)-1] == '\n' {
			blank := len(bytes.TrimSpace(b.pending[b.lineStart:])) == 0
			b.lineStart = len(b.pending)
			if blank {
				frame, err := b.Finish()
				if err != nil {
					return nil, err
				}
				output = append(output, frame...)
			}
		}
	}
	return output, nil
}

func (b *openAIUpstreamIdentitySSEBuffer) Finish() ([]byte, error) {
	if len(b.pending) == 0 {
		return nil, nil
	}
	restored, err := restoreOpenAIUpstreamIdentitySSE(b.pending)
	if err != nil {
		return nil, err
	}
	b.pending = b.pending[:0]
	b.lineStart = 0
	return restored, nil
}
