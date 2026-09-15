package iqcheck

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

const MaxEventBytes = 128 * 1024
const MaxEvents = 4096

var errBodyLimit = errors.New("response_too_large")

type countedReader struct {
	r io.Reader
	n int64
}

func (r *countedReader) Read(b []byte) (int, error) {
	if r.n >= MaxResponseBytes+1 {
		return 0, errBodyLimit
	}
	if len(b) > MaxResponseBytes+1-int(r.n) {
		b = b[:MaxResponseBytes+1-int(r.n)]
	}
	n, e := r.r.Read(b)
	r.n += int64(n)
	if r.n > MaxResponseBytes {
		return n, errBodyLimit
	}
	return n, e
}

type parseFailure struct {
	code, field string
	offset      int64
}

func (e *parseFailure) Error() string { return e.code }
func failure(code string) error       { return &parseFailure{code: code} }

// Validate JSON without storing values. Paths contain only known schema names,
// never arbitrary upstream keys. Unknown event payloads need not match Response.
func validateEvent(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	known := map[string]bool{"type": true, "response": true, "status": true, "output": true, "content": true, "text": true, "answer": true, "role": true, "phase": true, "error": true, "model": true, "choices": true, "delta": true, "message": true, "finish_reason": true, "index": true, "id": true, "refusal": true}
	critical := map[string]bool{"type": true, "response": true, "status": true, "output": true, "content": true, "text": true, "answer": true, "role": true, "phase": true, "error": true, "choices": true, "delta": true, "message": true, "finish_reason": true, "index": true, "model": true, "id": true, "refusal": true}
	var walk func(int, string) error
	walk = func(depth int, path string) error {
		if depth > 32 {
			return &parseFailure{"invalid_event_json", path, dec.InputOffset()}
		}
		tok, err := dec.Token()
		if err != nil {
			return &parseFailure{"invalid_event_json", path, dec.InputOffset()}
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch d {
		case '{':
			keys := map[string]bool{}
			for dec.More() {
				k, e := dec.Token()
				if e != nil {
					return failure("invalid_event_json")
				}
				key, ok := k.(string)
				if !ok {
					return failure("invalid_event_json")
				}
				// Match encoding/json's case-insensitive field lookup before checking duplicates.
				if !known[key] {
					for name := range known {
						if strings.EqualFold(key, name) {
							key = name
							break
						}
					}
				}
				field := "extension"
				if known[key] {
					field = key
				}
				p := path + "." + field
				if keys[key] {
					code := "duplicate_event_field"
					if critical[key] {
						code = "duplicate_critical_field"
					}
					return &parseFailure{code, p, dec.InputOffset()}
				}
				keys[key] = true
				if e = walk(depth+1, p); e != nil {
					return e
				}
			}
			end, e := dec.Token()
			if e != nil || end != json.Delim('}') {
				return failure("invalid_event_json")
			}
		case '[':
			for dec.More() {
				if e := walk(depth+1, path+"[]"); e != nil {
					return e
				}
			}
			end, e := dec.Token()
			if e != nil || end != json.Delim(']') {
				return failure("invalid_event_json")
			}
		default:
			return failure("invalid_event_json")
		}
		return nil
	}
	if err := walk(0, "root"); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return &parseFailure{"invalid_event_json", "root", dec.InputOffset()}
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return failure("invalid_event_shape")
	}
	return nil
}

func decode(raw []byte, v any, field string) error {
	if err := json.Unmarshal(raw, v); err != nil {
		offset := int64(0)
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			offset = typeErr.Offset
		}
		return &parseFailure{"invalid_event_shape", field, offset}
	}
	return nil
}

type streamParser struct {
	chat, sse, completed, done bool
	answer, model, responseID  string
	d                          *Diagnostic
}

func (p *streamParser) consume(raw []byte, eventName string) error {
	p.d.EventType = safeToken(eventName, 96)
	if err := validateEvent(raw); err != nil {
		return err
	}
	var env struct {
		Type     string          `json:"type"`
		Error    json.RawMessage `json:"error"`
		Response json.RawMessage `json:"response"`
	}
	if err := decode(raw, &env, "root"); err != nil {
		return err
	}
	p.d.EventType = safeToken(env.Type, 96)
	if eventName != "" && env.Type != "" && eventName != env.Type {
		return failure("event_type_mismatch")
	}
	if p.done {
		return failure("data_after_done")
	}
	if env.Error != nil && string(env.Error) != "null" {
		return failure(ReadUpstreamError(raw, p.d))
	}
	if p.chat {
		return p.consumeChat(raw)
	}
	if !p.sse {
		return p.terminal(raw)
	}
	switch env.Type {
	case "error", "response.failed":
		return failure(ReadUpstreamError(raw, p.d))
	case "response.incomplete":
		return failure("incomplete_response")
	case "response.completed", "response.done":
		if len(env.Response) == 0 || string(env.Response) == "null" {
			return failure("invalid_terminal_response")
		}
		return p.terminal(env.Response)
	case "":
		return failure("invalid_event_shape")
	default:
		// Auxiliary events and deltas never supply a final answer. Their payloads
		// evolve independently of the terminal Response schema.
		return nil
	}
}

func (p *streamParser) terminal(raw []byte) error {
	var r struct {
		ID     string            `json:"id"`
		Status string            `json:"status"`
		Model  string            `json:"model"`
		Output []json.RawMessage `json:"output"`
		Error  json.RawMessage   `json:"error"`
	}
	if err := decode(raw, &r, "response"); err != nil {
		return err
	}
	if r.Error != nil && string(r.Error) != "null" {
		return failure(ReadUpstreamError(raw, p.d))
	}
	if r.Status != "completed" {
		return failure("incomplete_response")
	}
	ReadUsage(raw, p.d)
	if len(r.Model) > 256 {
		return failure("response_too_large")
	}
	var answer string
	for _, rawItem := range r.Output {
		var item struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Phase   string          `json:"phase"`
			Status  string          `json:"status"`
			Content json.RawMessage `json:"content"`
		}
		if err := decode(rawItem, &item, "response.output[]"); err != nil {
			return err
		}
		if item.Type != "message" || item.Role != "assistant" || item.Phase != "" && item.Phase != "final_answer" {
			continue
		}
		if item.Status != "" && item.Status != "completed" {
			return failure("incomplete_response")
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := decode(item.Content, &parts, "response.output[].content"); err != nil {
			return err
		}
		var message strings.Builder
		for _, part := range parts {
			if part.Type == "refusal" {
				return failure("refusal")
			}
			if part.Type == "output_text" {
				_, _ = message.WriteString(part.Text)
			}
		}
		if message.Len() > 0 {
			// Content parts share a message; independent final messages do not.
			if answer != "" {
				return failure("conflicting_final_response")
			}
			answer = message.String()
		}
	}
	if len(answer) > MaxAnswerBytes {
		return failure("response_too_large")
	}
	if p.completed && (p.answer != answer || p.responseID != r.ID || p.model != r.Model) {
		return failure("conflicting_final_response")
	}
	ReadUsage(raw, p.d)
	p.answer, p.model, p.responseID, p.completed = answer, r.Model, r.ID, true
	return nil
}

func (p *streamParser) consumeChat(raw []byte) error {
	var r struct {
		Model   string `json:"model"`
		Choices []struct {
			Index  int     `json:"index"`
			Finish *string `json:"finish_reason"`
			Delta  struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"delta"`
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := decode(raw, &r, "choices"); err != nil {
		return err
	}
	ReadUsage(raw, p.d)
	if len(r.Model) > 256 {
		return failure("response_too_large")
	}
	if r.Model != "" {
		p.model = r.Model
	}
	for _, c := range r.Choices {
		if c.Index != 0 {
			continue
		}
		if c.Delta.Refusal != "" || c.Message.Refusal != "" {
			return failure("refusal")
		}
		text := c.Message.Content
		if p.sse {
			text = c.Delta.Content
		}
		if p.completed && (text != "" || c.Finish != nil) {
			return failure("conflicting_final_response")
		}
		p.answer += text
		if len(p.answer) > MaxAnswerBytes {
			return failure("response_too_large")
		}
		if c.Finish != nil {
			if *c.Finish != "stop" {
				return failure("incomplete_response")
			}
			p.completed = true
		}
	}
	return nil
}

// Parse keeps the established explicit-format API for callers and fixtures.
func Parse(body io.Reader, sse, chat bool) Result {
	d := &Diagnostic{ParserVersion: ParserVersion, Stage: "decode", Protocol: "responses"}
	if chat {
		d.Protocol = "chat_completions"
	}
	return parse(body, sse, chat, d)
}

func parse(body io.Reader, sse, chat bool, d *Diagnostic) Result {
	reader := &countedReader{r: body}
	p := streamParser{chat: chat, sse: sse, d: d}
	finish := func(err error) Result {
		d.BytesRead = reader.n
		if err != nil {
			d.Code = "response_read_failed"
			var f *parseFailure
			switch {
			case errors.As(err, &f):
				d.Code = f.code
				d.Field = f.field
				d.Offset = f.offset
			case errors.Is(err, errBodyLimit):
				d.Code = "response_too_large"
			case errors.Is(err, context.DeadlineExceeded):
				d.Code = "timeout"
				d.Stage = "read"
			case errors.Is(err, context.Canceled):
				d.Code = "interrupted"
				d.Stage = "read"
			default:
				d.Stage = "read"
			}
			r := Unknown(d.Code)
			r.Diagnostic = d.Bounded()
			return r
		}
		if !p.completed {
			return finishUnknown(d, "incomplete_response")
		}
		d.Stage = "grade"
		r := Grade(p.answer)
		d.Code = r.Reason
		r.ReportedModel = p.model
		r.Diagnostic = d.Bounded()
		return r
	}
	if !sse {
		raw, err := io.ReadAll(reader)
		if err != nil {
			return finish(err)
		}
		return finish(p.consume(raw, ""))
	}
	br := bufio.NewReaderSize(reader, 4096)
	var data []string
	eventName := ""
	frameBytes := 0
	events := 0
	flush := func() error {
		if len(data) == 0 {
			eventName = ""
			frameBytes = 0
			return nil
		}
		events++
		d.EventIndex = events
		if events > MaxEvents {
			return failure("too_many_events")
		}
		joined := strings.Join(data, "\n")
		data = nil
		frameBytes = 0
		name := eventName
		eventName = ""
		if joined == "[DONE]" {
			if !p.completed {
				return failure("incomplete_response")
			}
			p.done = true
			return nil
		}
		return p.consume([]byte(joined), name)
	}
	var line []byte
	for {
		part, prefix, err := br.ReadLine()
		line = append(line, part...)
		if len(line)+frameBytes > MaxEventBytes {
			return finish(failure("event_too_large"))
		}
		if err != nil {
			if err == io.EOF {
				if e := flush(); e != nil {
					return finish(e)
				}
				return finish(nil)
			}
			return finish(err)
		}
		if prefix {
			continue
		}
		s := string(line)
		line = nil
		if s == "" {
			if e := flush(); e != nil {
				return finish(e)
			}
			continue
		}
		frameBytes += len(s) + 1
		if strings.HasPrefix(s, ":") {
			continue
		}
		field, value, _ := strings.Cut(s, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data = append(data, value)
		case "event":
			eventName = value
		}
	}
}

func finishUnknown(d *Diagnostic, code string) Result {
	d.Code = code
	r := Unknown(code)
	r.Diagnostic = d.Bounded()
	return r
}

// ParseHTTP resolves media type once and parses the decoded response stream.
// Transport normally decompresses; bounded decoding covers plugin responses too.
func ParseHTTP(resp *http.Response, chat bool, transport string) Result {
	d := &Diagnostic{ParserVersion: ParserVersion, Stage: "format", Protocol: "responses", Transport: transport}
	if chat {
		d.Protocol = "chat_completions"
	}
	if resp == nil || resp.Body == nil {
		return finishUnknown(d, "response_read_failed")
	}
	d.RetryVisibility = "single_application_call"
	if transport == "plugin" {
		d.RetryVisibility = "plugin_attempts_unknown"
	}
	d.RetryAfter, d.RetryAfterUnbounded = RetryAfter(resp.Header.Get("Retry-After"), time.Now().UTC())
	d.HTTPStatus = resp.StatusCode
	d.RequestID = resp.Header.Get("X-Request-Id")
	d.Encoding = strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	d.MediaType = media
	if resp.StatusCode == http.StatusOK && err != nil && resp.Header.Get("Content-Type") != "" {
		return finishUnknown(d, "response_format_mismatch")
	}
	var body io.Reader = resp.Body
	switch d.Encoding {
	case "", "identity":
	case "gzip":
		z, e := gzip.NewReader(body)
		if e != nil {
			return finishUnknown(d, "response_decompression_failed")
		}
		defer func() { _ = z.Close() }()
		body = z
	case "br":
		body = brotli.NewReader(body)
	case "zstd":
		z, e := zstd.NewReader(body, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(8<<20))
		if e != nil {
			return finishUnknown(d, "response_decompression_failed")
		}
		defer z.Close()
		body = z
	case "deflate":
		z, e := zlib.NewReader(body)
		if e != nil {
			return finishUnknown(d, "response_decompression_failed")
		}
		defer func() { _ = z.Close() }()
		body = z
	default:
		return finishUnknown(d, "unsupported_content_encoding")
	}
	if resp.StatusCode != http.StatusOK {
		d.Stage = "http"
		reason := httpCode(resp.StatusCode)
		raw, e := io.ReadAll(io.LimitReader(body, 8193))
		if e == nil {
			if parsed := ReadUpstreamError(raw, d); parsed != "upstream_error" {
				reason = parsed
			}
		}
		return finishUnknown(d, reason)
	}
	if media != "" && media != "application/json" && media != "text/event-stream" && media != "application/octet-stream" && media != "text/plain" {
		return finishUnknown(d, "unsupported_media_type")
	}
	br := bufio.NewReaderSize(body, 4096)
	// Read only through the first non-whitespace byte before choosing a decoder;
	// waiting for a fixed-size Peek would delay short streaming responses.
	var prefix []byte
	for len(prefix) < 4096 {
		b, e := br.ReadByte()
		if e != nil {
			d.Stage = "read"
			if errors.Is(e, context.DeadlineExceeded) {
				return finishUnknown(d, "timeout")
			}
			if errors.Is(e, context.Canceled) {
				return finishUnknown(d, "interrupted")
			}
			if d.Encoding != "" && d.Encoding != "identity" {
				return finishUnknown(d, "response_decompression_failed")
			}
			return finishUnknown(d, "response_read_failed")
		}
		prefix = append(prefix, b)
		if !strings.ContainsRune(" \r\n\t", rune(b)) {
			break
		}
	}
	first := bytes.TrimSpace(prefix)
	if len(first) == 0 {
		return finishUnknown(d, "response_format_mismatch")
	}
	var sse bool
	switch first[0] {
	case '{':
		sse = false
	case ':', 'd', 'e', 'i', 'r':
		sse = true
	default:
		return finishUnknown(d, "response_format_mismatch")
	}
	d.FormatDetected = (sse && media != "text/event-stream") || (!sse && media != "application/json")
	d.Stage = "decode"
	result := parse(io.MultiReader(bytes.NewReader(prefix), br), sse, chat, d)
	if d.Encoding != "" && d.Encoding != "identity" && result.Reason == "response_read_failed" {
		result = finishUnknown(result.Diagnostic, "response_decompression_failed")
	}
	return result
}

func httpCode(status int) string {
	// No upstream error message/body is retained here.
	return "http_" + strconv.Itoa(status)
}
