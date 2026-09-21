// Package statestream validates response framing while retaining only routing
// metadata. Output arrays and strings are scanned without buffering their contents.
package statestream

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

var errInvalid = errors.New("invalid response stream")
var kept = map[string]bool{"type": true, "object": true, "response": true, "status": true, "model": true, "id": true, "error": true, "code": true}

func Walk(body io.Reader, encoding string, consume func([]byte, string) error) error {
	reader := body
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
	case "gzip":
		r, e := gzip.NewReader(body)
		if e != nil {
			return e
		}
		defer func() { _ = r.Close() }()
		reader = r
	case "deflate":
		r, e := zlib.NewReader(body)
		if e != nil {
			return e
		}
		defer func() { _ = r.Close() }()
		reader = r
	case "br":
		reader = brotli.NewReader(body)
	case "zstd":
		r, e := zstd.NewReader(body, zstd.WithDecoderMaxMemory(8<<20), zstd.WithDecoderConcurrency(1))
		if e != nil {
			return e
		}
		defer r.Close()
		reader = r
	default:
		return errors.New("unsupported content encoding")
	}
	r := bufio.NewReaderSize(reader, 8192)
	for {
		b, e := r.Peek(1)
		if e != nil {
			return e
		}
		if !space(b[0]) {
			break
		}
		_, _ = r.ReadByte()
	}
	first, _ := r.Peek(1)
	if first[0] == '{' {
		data, e := project(r)
		if e != nil {
			return e
		}
		return consume(data, "")
	}
	event := ""
	var data []byte
	flush := func() error {
		if data == nil {
			event = ""
			return nil
		}
		e := consume(data, event)
		data = nil
		event = ""
		return e
	}
events:
	for {
		prefix, e := r.Peek(5)
		if e == nil && string(prefix) == "data:" {
			if data != nil {
				return errInvalid
			}
			er := &eventReader{reader: r, begin: true}
			br := bufio.NewReaderSize(er, 4096)
			for {
				p, e := br.Peek(1)
				if e != nil {
					if e == io.EOF {
						event = ""
						continue events
					}
					return e
				}
				if !space(p[0]) {
					break
				}
				_, _ = br.ReadByte()
			}
			p, _ := br.Peek(1)
			if p[0] == '[' {
				raw, e := io.ReadAll(io.LimitReader(br, 16))
				if e != nil || string(bytes.TrimSpace(raw)) != "[DONE]" {
					return errInvalid
				}
				data = []byte("[DONE]")
			} else {
				var e error
				data, e = project(br)
				if e != nil {
					return e
				}
			}
			continue
		}
		line, e := smallLine(r)
		if e != nil && e != io.EOF {
			return e
		}
		trimmed := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		switch {
		case trimmed == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(trimmed, "event:"):
			event = strings.TrimSpace(trimmed[6:])
			if len(event) > 128 {
				return errInvalid
			}
		case strings.HasPrefix(trimmed, ":"), strings.HasPrefix(trimmed, "id:"), strings.HasPrefix(trimmed, "retry:"):
		default:
			return errInvalid
		}
		if e == io.EOF {
			return flush()
		}
	}
}
func smallLine(r *bufio.Reader) (string, error) {
	b, e := r.ReadSlice('\n')
	if e == bufio.ErrBufferFull {
		return "", errInvalid
	}
	return string(b), e
}

// A reader over adjacent SSE data fields. Each physical line is streamed in
// fixed-size fragments, including JSON output strings spanning many megabytes.
type eventReader struct {
	reader  *bufio.Reader
	begin   bool
	pending []byte
	end     bool
}

func (e *eventReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(e.pending) == 0 {
		if e.end {
			return 0, io.EOF
		}
		if e.begin {
			prefix, err := e.reader.Peek(5)
			if err != nil || string(prefix) != "data:" {
				e.end = true
				return 0, io.EOF
			}
			_, _ = e.reader.Discard(5)
			if b, err := e.reader.Peek(1); err == nil && b[0] == ' ' {
				_, _ = e.reader.Discard(1)
			}
			e.begin = false
		}
		fragment, err := e.reader.ReadSlice('\n')
		e.pending = append(e.pending[:0], fragment...)
		if err != nil && err != bufio.ErrBufferFull && err != io.EOF {
			return 0, err
		}
		switch err {
		case io.EOF:
			e.end = true
		case nil:
			e.begin = true
		}
	}
	n := copy(p, e.pending)
	e.pending = e.pending[n:]
	return n, nil
}
func space(b byte) bool { return b == ' ' || b == '\n' || b == '\r' || b == '\t' }
func next(r *bufio.Reader) (byte, error) {
	for {
		b, e := r.ReadByte()
		if e != nil {
			return 0, e
		}
		if !space(b) {
			return b, nil
		}
	}
}
func project(r *bufio.Reader) ([]byte, error) {
	b, e := next(r)
	if e != nil || b != '{' {
		return nil, errInvalid
	}
	v, e := value(r, b, true, 0)
	if e != nil {
		return nil, e
	}
	if _, e = next(r); e != io.EOF {
		return nil, errInvalid
	}
	return json.Marshal(v)
}
func value(r *bufio.Reader, b byte, keep bool, depth int) (any, error) {
	if depth > 64 {
		return nil, errInvalid
	}
	switch b {
	case '{':
		var out map[string]any
		if keep {
			out = map[string]any{}
		}
		b, e := next(r)
		if e != nil {
			return nil, e
		}
		if b == '}' {
			return out, nil
		}
		for {
			if b != '"' {
				return nil, errInvalid
			}
			key, e := str(r, keep, 256)
			if e != nil {
				return nil, e
			}
			b, e = next(r)
			if e != nil || b != ':' {
				return nil, errInvalid
			}
			b, e = next(r)
			if e != nil {
				return nil, e
			}
			retain := keep && kept[strings.ToLower(key)]
			if retain {
				if _, exists := out[strings.ToLower(key)]; exists {
					return nil, errInvalid
				}
			}
			child, e := value(r, b, retain, depth+1)
			if e != nil {
				return nil, e
			}
			if retain {
				out[strings.ToLower(key)] = child
			}
			b, e = next(r)
			if e != nil {
				return nil, e
			}
			if b == '}' {
				return out, nil
			}
			if b != ',' {
				return nil, errInvalid
			}
			b, e = next(r)
			if e != nil {
				return nil, e
			}
		}
	case '[':
		// Routing fields must never hide in output/tool arrays.
		b, e := next(r)
		if e != nil {
			return nil, e
		}
		if b == ']' {
			return []any{}, nil
		}
		for {
			if _, e = value(r, b, false, depth+1); e != nil {
				return nil, e
			}
			b, e = next(r)
			if e != nil {
				return nil, e
			}
			if b == ']' {
				return []any{}, nil
			}
			if b != ',' {
				return nil, errInvalid
			}
			b, e = next(r)
			if e != nil {
				return nil, e
			}
		}
	case '"':
		return str(r, keep, 4096)
	case 't', 'f', 'n':
		literal := "true"
		var result any = true
		if b == 'f' {
			literal = "false"
			result = false
		}
		if b == 'n' {
			literal = "null"
			result = nil
		}
		for i := 1; i < len(literal); i++ {
			c, e := r.ReadByte()
			if e != nil || c != literal[i] {
				return nil, errInvalid
			}
		}
		return result, nil
	default:
		raw := []byte{b}
		for {
			p, e := r.Peek(1)
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil, e
			}
			c := p[0]
			if space(c) || c == ',' || c == '}' || c == ']' {
				break
			}
			_, _ = r.ReadByte()
			raw = append(raw, c)
			if len(raw) > 128 {
				return nil, errInvalid
			}
		}
		if !json.Valid(raw) || raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
			return nil, errInvalid
		}
		return json.Number(raw), nil
	}
}
func str(r *bufio.Reader, keep bool, limit int) (string, error) {
	var raw []byte
	if keep {
		raw = append(raw, '"')
	}
	for {
		b, e := r.ReadByte()
		if e != nil {
			return "", e
		}
		if keep {
			raw = append(raw, b)
			if len(raw) > limit {
				return "", errInvalid
			}
		}
		if b == '"' {
			if !keep {
				return "", nil
			}
			var v string
			e := json.Unmarshal(raw, &v)
			if e != nil {
				return "", errInvalid
			}
			return v, nil
		}
		if b < 0x20 {
			return "", errInvalid
		}
		if b == '\\' {
			c, e := r.ReadByte()
			if e != nil {
				return "", e
			}
			if keep {
				raw = append(raw, c)
			}
			if !strings.ContainsRune("\"\\/bfnrtu", rune(c)) {
				return "", errInvalid
			}
			if c == 'u' {
				for i := 0; i < 4; i++ {
					h, e := r.ReadByte()
					if e != nil || (h < '0' || h > '9') && (h < 'a' || h > 'f') && (h < 'A' || h > 'F') {
						return "", errInvalid
					}
					if keep {
						raw = append(raw, h)
					}
				}
			}
		}
	}
}
