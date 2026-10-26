package service

import (
	"encoding/binary"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cespare/xxhash/v2"
	"github.com/tidwall/gjson"
)

const (
	sessionAnchorDomainPrefix = "a2|"
	sessionAnchorBillingLine  = "x-anthropic-billing-header:"
	sessionReminderOpen       = "<system-reminder>"
	sessionReminderClose      = "</system-reminder>"
)

func (s *GatewayService) stableSessionAnchorHash(parsed *ParsedRequest) string {
	if parsed == nil {
		return ""
	}

	h := xxhash.New()
	writeAnchorField(h, []byte(sessionAnchorDomainPrefix))
	session := parsed.SessionContext
	if session == nil {
		writeAnchorField(h, nil)
		writeAnchorField(h, nil)
		writeAnchorInt(h, 0)
	} else {
		writeAnchorField(h, []byte(session.ClientIP))
		writeAnchorField(h, []byte(NormalizeSessionUserAgent(session.UserAgent)))
		writeAnchorInt(h, session.APIKeyID)
	}
	writeAnchorInt(h, derefGroupID(parsed.GroupID))
	systemLength := writeStableSystemAnchorText(h, parsed.SystemRaw())
	// Text NUL is escaped as 00 ff, leaving 00 00 as an unambiguous field separator.
	_, _ = h.Write([]byte{0, 0})
	userLength := writeStableFirstUserAnchorText(h, parsed)
	if systemLength == 0 && userLength == 0 {
		return ""
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

func writeAnchorField(dst *xxhash.Digest, value []byte) {
	writeAnchorLength(dst, uint64(len(value)))
	if len(value) > 0 {
		_, _ = dst.Write(value)
	}
}

func writeAnchorLength(dst *xxhash.Digest, length uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], length)
	_, _ = dst.Write(encoded[:])
}

func writeAnchorInt(dst *xxhash.Digest, value int64) {
	var buffer [20]byte
	writeAnchorField(dst, strconv.AppendInt(buffer[:0], value, 10))
}

func writeStableSystemAnchorText(dst *xxhash.Digest, raw []byte) uint64 {
	writer := stableAnchorTextWriter{dst: dst}
	system := parseRawJSONView(raw)
	switch system.Type {
	case gjson.String:
		writer.writeSystemBlock(system.String())
	case gjson.JSON:
		if system.IsArray() {
			system.ForEach(func(_, part gjson.Result) bool {
				text := part.Get("text")
				if text.Type == gjson.String {
					writer.writeSystemBlock(text.String())
				}
				return true
			})
		}
	}
	return writer.finish()
}

func writeStableFirstUserAnchorText(dst *xxhash.Digest, parsed *ParsedRequest) uint64 {
	if parsed == nil {
		return 0
	}
	if length := writeStableFirstUserFromMessages(dst, parsed.MessagesRaw()); length > 0 {
		return length
	}
	if len(parsed.InputRaw()) == 0 {
		return 0
	}
	var input strings.Builder
	appendResponsesSessionAnchorFromRaw(&input, parsed.InputRaw())
	writer := stableAnchorTextWriter{dst: dst}
	writer.writeWithoutReminders(input.String())
	return writer.finish()
}

func writeStableFirstUserFromMessages(dst *xxhash.Digest, raw []byte) uint64 {
	messages := parseRawJSONView(raw)
	if !messages.IsArray() {
		return 0
	}
	var selectedLength uint64
	messages.ForEach(func(_, message gjson.Result) bool {
		parts := message.Get("parts")
		role := message.Get("role").String()
		isUserShape := role == "user" || (role == "" && parts.IsArray())
		if !isUserShape {
			return true
		}

		content := message.Get("content")
		isParts := false
		if !content.Exists() {
			content = parts
			isParts = true
		}
		length := writeStableUserContent(dst, content, isParts)
		if length == 0 {
			return true
		}
		selectedLength = length
		return false
	})
	return selectedLength
}

func writeStableUserContent(dst *xxhash.Digest, content gjson.Result, isParts bool) uint64 {
	writer := stableAnchorTextWriter{dst: dst}
	switch content.Type {
	case gjson.String:
		writer.writeWithoutReminders(content.String())
	case gjson.JSON:
		if content.IsArray() {
			content.ForEach(func(_, part gjson.Result) bool {
				partType := part.Get("type").String()
				if partType == "text" || (isParts && partType == "") {
					text := part.Get("text")
					if text.Type == gjson.String {
						writer.writeBlockWithoutReminders(text.String())
					}
				}
				return true
			})
		}
	}
	return writer.finish()
}

func stripSessionBillingHeader(text string) string {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if !strings.HasPrefix(trimmed, sessionAnchorBillingLine) {
		return text
	}
	end := strings.IndexAny(trimmed, "\r\n")
	if end < 0 {
		return ""
	}
	rest := trimmed[end+1:]
	if trimmed[end] == '\r' {
		rest = strings.TrimPrefix(rest, "\n")
	}
	return rest
}

type stableAnchorTextWriter struct {
	dst          *xxhash.Digest
	scratch      [1024]byte
	used         int
	length       uint64
	hasText      bool
	pendingSpace bool
}

func (w *stableAnchorTextWriter) writeSystemBlock(text string) {
	if w.hasText {
		w.pendingSpace = true
	}
	w.writeWithoutReminders(stripSessionBillingHeader(text))
}

func (w *stableAnchorTextWriter) writeBlockWithoutReminders(text string) {
	if w.hasText {
		w.pendingSpace = true
	}
	w.writeWithoutReminders(text)
}

func (w *stableAnchorTextWriter) writeWithoutReminders(text string) {
	start := 0
	for start < len(text) {
		openOffset := strings.Index(text[start:], sessionReminderOpen)
		if openOffset < 0 {
			w.writeText(text[start:])
			return
		}
		openAt := start + openOffset
		closeOffset := strings.Index(text[openAt+len(sessionReminderOpen):], sessionReminderClose)
		if closeOffset < 0 {
			w.writeText(text[start:])
			return
		}
		closeAt := openAt + len(sessionReminderOpen) + closeOffset
		w.writeText(text[start:openAt])
		start = closeAt + len(sessionReminderClose)
	}
}

func (w *stableAnchorTextWriter) writeText(text string) {
	for i := 0; i < len(text); {
		if text[i] < utf8.RuneSelf {
			value := text[i]
			i++
			if isASCIIAnchorSpace(value) {
				if w.hasText {
					w.pendingSpace = true
				}
				continue
			}
			w.emitPendingSpace()
			if value == 0 {
				w.emitByte(0)
				w.emitByte(0xff)
			} else {
				w.emitByte(value)
			}
			w.length++
			w.hasText = true
			continue
		}

		r, size := utf8.DecodeRuneInString(text[i:])
		if unicode.IsSpace(r) {
			if w.hasText {
				w.pendingSpace = true
			}
			i += size
			continue
		}
		w.emitPendingSpace()
		w.emitTextBytes(text[i : i+size])
		w.length += uint64(size)
		w.hasText = true
		i += size
	}
}

func isASCIIAnchorSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r' || value == '\v' || value == '\f'
}

func (w *stableAnchorTextWriter) emitPendingSpace() {
	if w.pendingSpace && w.hasText {
		w.emitByte(' ')
		w.length++
	}
	w.pendingSpace = false
}

func (w *stableAnchorTextWriter) emitByte(value byte) {
	if w.dst == nil {
		return
	}
	if w.used == len(w.scratch) {
		_, _ = w.dst.Write(w.scratch[:w.used])
		w.used = 0
	}
	w.scratch[w.used] = value
	w.used++
}

func (w *stableAnchorTextWriter) emitTextBytes(value string) {
	if w.dst == nil {
		return
	}
	for len(value) > 0 {
		if w.used == len(w.scratch) {
			_, _ = w.dst.Write(w.scratch[:w.used])
			w.used = 0
		}
		n := copy(w.scratch[w.used:], value)
		w.used += n
		value = value[n:]
	}
}

func (w *stableAnchorTextWriter) finish() uint64 {
	if w.dst != nil && w.used > 0 {
		_, _ = w.dst.Write(w.scratch[:w.used])
	}
	return w.length
}
