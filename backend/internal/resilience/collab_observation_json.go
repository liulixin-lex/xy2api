package resilience

import "bytes"

// Charge conservative linear bounds for span readers, and charge each search
// and fixed-size comparison separately. No observation can borrow another
// request's budget. Exhaustion unwinds only to inspectCollabIngress.
type collabIngressBudget struct{ remaining int64 }
type collabIngressExhausted struct{}

func (b *collabIngressBudget) spend(work int64) {
	if work > b.remaining {
		panic(collabIngressExhausted{})
	}
	b.remaining -= work
}

func (b *collabIngressBudget) fields(raw []byte) collabIngressFieldSet {
	// Includes the span walk and the fixed inventory of key comparisons.
	b.spend(32 * int64(len(raw)))
	return collabIngressFields(raw)
}

func (b *collabIngressBudget) each(raw []byte, visit func(key, value []byte)) {
	b.spend(2 * int64(len(raw)))
	collabRawEach(raw, visit)
}

func (b *collabIngressBudget) indexByte(raw []byte, c byte) int {
	span := raw
	if int64(len(span)) > b.remaining {
		span = span[:int(b.remaining)]
	}
	at := bytes.IndexByte(span, c)
	if at >= 0 {
		b.spend(int64(at + 1))
	} else {
		b.spend(int64(len(span)))
		if len(span) < len(raw) {
			panic(collabIngressExhausted{})
		}
	}
	return at
}

func (b *collabIngressBudget) functionName(raw []byte) (name []byte) {
	// A nested path uses the first function member with a name, even if an
	// earlier function was null, a scalar, or an object without that key.
	b.spend(8 * int64(len(raw)))
	if collabRawObject(raw) {
		collabRawEach(raw, func(key, value []byte) {
			if name == nil && collabStringEqual(key, "function") && collabRawObject(value) {
				collabRawEach(value, func(key, value []byte) {
					if name == nil && collabStringEqual(key, "name") {
						name = value
					}
				})
			}
		})
	}
	return name
}

// This is a span reader for already validated JSON, not a second validator.
// Strings remain borrowed raw slices; no request string is copied or unescaped.
type collabIngressFieldSet struct {
	typ, name, namespace, function, tools, role, content, output, encrypted, text, input, response []byte
}

func collabIngressFields(raw []byte) (fields collabIngressFieldSet) {
	if !collabRawObject(raw) {
		return
	}
	collabRawEach(raw, func(key, value []byte) {
		var dst *[]byte
		switch {
		case collabStringEqual(key, "type"):
			dst = &fields.typ
		case collabStringEqual(key, "name"):
			dst = &fields.name
		case collabStringEqual(key, "namespace"):
			dst = &fields.namespace
		case collabStringEqual(key, "function"):
			dst = &fields.function
		case collabStringEqual(key, "tools"):
			dst = &fields.tools
		case collabStringEqual(key, "role"):
			dst = &fields.role
		case collabStringEqual(key, "content"):
			dst = &fields.content
		case collabStringEqual(key, "output"):
			dst = &fields.output
		case collabStringEqual(key, "encrypted_content"):
			dst = &fields.encrypted
		case collabStringEqual(key, "text"):
			dst = &fields.text
		case collabStringEqual(key, "input"):
			dst = &fields.input
		case collabStringEqual(key, "response"):
			dst = &fields.response
		}
		if dst != nil && *dst == nil {
			*dst = value
		}
	})
	return
}

func collabRawObject(raw []byte) bool { return len(raw) > 0 && raw[0] == '{' }
func collabRawString(raw []byte) bool {
	return len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"'
}

func collabRawEach(raw []byte, visit func(key, value []byte)) {
	if len(raw) == 0 {
		return
	}
	if raw[0] != '{' && raw[0] != '[' {
		visit(nil, raw)
		return
	}
	object := raw[0] == '{'
	for i := 1; i < len(raw); {
		for i < len(raw) && (raw[i] <= ' ' || raw[i] == ',') {
			i++
		}
		if i >= len(raw) || raw[i] == '}' || raw[i] == ']' {
			return
		}
		var key []byte
		if object {
			end := collabRawEnd(raw, i)
			if end <= i {
				return
			}
			key = raw[i:end]
			i = end
			for i < len(raw) && (raw[i] <= ' ' || raw[i] == ':') {
				i++
			}
		}
		end := collabRawEnd(raw, i)
		if end <= i {
			return
		}
		visit(key, raw[i:end])
		i = end
	}
}

func collabRawEnd(raw []byte, start int) int {
	if start >= len(raw) {
		return start
	}
	if raw[start] == '"' {
		return collabRawStringEnd(raw, start)
	}
	if raw[start] != '{' && raw[start] != '[' {
		i := start
		for i < len(raw) && raw[i] != ',' && raw[i] != ']' && raw[i] != '}' && raw[i] > ' ' {
			i++
		}
		return i
	}
	depth := 1
	for i := start + 1; i < len(raw); i++ {
		switch raw[i] {
		case '"':
			i = collabRawStringEnd(raw, i) - 1
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(raw)
}

func collabRawStringEnd(raw []byte, start int) int {
	// Search quotes directly: escaped newlines/Unicode in a large string need
	// no per-byte decoding. Only an escaped quote can delay its closing quote.
	for i := start + 1; i < len(raw); {
		offset := bytes.IndexByte(raw[i:], '"')
		if offset < 0 {
			return len(raw)
		}
		i += offset
		slashes := 0
		for j := i - 1; j > start && raw[j] == '\\'; j-- {
			slashes++
		}
		if slashes%2 == 0 {
			return i + 1
		}
		i++
	}
	return len(raw)
}

func collabStringEqual(raw []byte, want string) bool {
	if !collabRawString(raw) || len(raw) < len(want)+2 || len(raw) > 6*len(want)+2 {
		return false
	}
	end, ok := collabMatchASCII(raw, 1, want)
	return ok && end == len(raw)-1
}
func collabStringPrefix(raw []byte, want string) bool {
	if !collabRawString(raw) {
		return false
	}
	_, ok := collabMatchASCII(raw, 1, want)
	return ok
}

// Compare at most len(want) ASCII codepoints, including escaped letters. Other
// Unicode cannot match a protocol keyword. No full-string decoding is needed.
func collabMatchASCII(raw []byte, index int, want string) (int, bool) {
	for i := 0; i < len(want); i++ {
		if index >= len(raw)-1 {
			return index, false
		}
		c := raw[index]
		index++
		if c == '\\' {
			if index >= len(raw)-1 {
				return index, false
			}
			c = raw[index]
			index++
			switch c {
			case 'u':
				if index+4 > len(raw)-1 {
					return index, false
				}
				code := 0
				for _, digit := range raw[index : index+4] {
					n := int(digit)
					switch {
					case digit >= '0' && digit <= '9':
						n -= '0'
					case digit >= 'a' && digit <= 'f':
						n = n - 'a' + 10
					case digit >= 'A' && digit <= 'F':
						n = n - 'A' + 10
					default:
						return index, false
					}
					code = code*16 + n
				}
				if code > 127 {
					return index, false
				}
				c = byte(code)
				index += 4
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			case 'b':
				c = '\b'
			case 'f':
				c = '\f'
			}
		}
		if c != want[i] {
			return index, false
		}
	}
	return index, true
}

func collabStringContains(raw []byte, want string) bool {
	b := collabIngressBudget{remaining: 1<<63 - 1}
	return b.contains(raw, want)
}

func (b *collabIngressBudget) contains(raw []byte, want string) bool {
	if !collabRawString(raw) {
		// Result.String returns raw JSON for arrays and objects. Scalars cannot
		// contain any catalog keyword, including normalized number strings.
		b.spend(int64(len(raw)))
		return len(raw) > 0 && (raw[0] == '[' || raw[0] == '{') && bytes.Contains(raw, []byte(want))
	}
	if len(want) == 0 {
		return true
	}
	content := raw[1 : len(raw)-1]
	// Literal absence plus no Unicode escapes means ordinary text escapes cannot
	// hide a keyword. This is per-field, even when unrelated tool history exists.
	b.spend(8 * int64(len(content)))
	if !bytes.Contains(content, []byte(want[max(0, len(want)-7):])) && !collabKeywordEscape(content, want) {
		return false
	}
	for i := 0; i < len(content); {
		// Cache the end of this literal run. Both searches advance; a failed
		// candidate never searches the remaining suffix for a backslash again.
		end := len(content)
		if at := b.indexByte(content[i:], '\\'); at >= 0 {
			end = i + at
		}
		b.spend(int64(end - i))
		if bytes.Contains(content[i:end], []byte(want)) {
			return true
		}
		if end == len(content) {
			return false
		}
		// Only this short tail can start a match crossing the next escape.
		i = max(i, end-len(want)+1)
		for i < end {
			at := b.indexByte(content[i:end], want[0])
			if at < 0 {
				i = end
				break
			}
			i += at
			b.spend(6 * int64(len(want)))
			if _, ok := collabMatchASCII(raw, i+1, want); ok {
				return true
			}
			i++
		}
		for i < len(content) && content[i] == '\\' {
			// Only decoded character boundaries can begin a match. In particular,
			// the final hex digit in a Unicode escape is never a literal letter.
			if i+1 < len(content) && content[i+1] == 'u' {
				b.spend(6 * int64(len(want)))
				if _, ok := collabMatchASCII(raw, i+1, want); ok {
					return true
				}
				i += 6
			} else {
				// Ordinary JSON escapes cannot begin an ASCII catalog keyword.
				b.spend(2)
				i += 2
			}
		}
	}
	return false
}

// A Unicode control or HTML escape cannot hide an ASCII keyword letter. Search
// the rare u00 fragment rather than every backslash in escaped developer text.
func collabKeywordEscape(raw []byte, want string) bool {
	for len(raw) > 0 {
		at := bytes.Index(raw, []byte("u00"))
		if at < 0 || at+5 > len(raw) {
			return false
		}
		code := 0
		for _, c := range raw[at+3 : at+5] {
			switch {
			case c >= '0' && c <= '9':
				code = code*16 + int(c-'0')
			case c >= 'a' && c <= 'f':
				code = code*16 + int(c-'a') + 10
			case c >= 'A' && c <= 'F':
				code = code*16 + int(c-'A') + 10
			default:
				code = 256
			}
		}
		if code <= 127 && bytes.IndexByte([]byte(want), byte(code)) >= 0 {
			return true
		}
		raw = raw[at+3:]
	}
	return false
}
