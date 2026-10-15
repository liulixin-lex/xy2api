package service

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// relayKeepaliveMarker identifies the keep-alive frames some relays inject
// into Responses streams, usually before response.created:
//
//	event: response.output_text.delta
//	data: {"type":"response.output_text.delta","item_id":"SSE-Keep-Alive","delta":"","SSE-Keep-Alive":true}
//
// They are not part of the protocol: strict SDKs (openai-python) reject a
// delta before response.created, and the gateway would count the frame as
// first output and give up pre-output failover and the first-output limit.
var relayKeepaliveMarker = []byte("SSE-Keep-Alive")

var relayKeepaliveDeltaEvent = []byte("response.output_text.delta")

// newOpenAIRelayKeepaliveFilter drops relay keep-alive frames from an
// upstream SSE body (design §7.8). Everything else passes through byte for
// byte; it is a no-op when resilience is switched off.
func newOpenAIRelayKeepaliveFilter(src io.Reader) io.Reader {
	if src == nil || !resilience.Current().Enabled {
		return src
	}
	return &relayKeepaliveFilter{r: bufio.NewReaderSize(src, 4096)}
}

type relayKeepaliveFilter struct {
	r *bufio.Reader
	// out holds bytes ready for the caller.
	out []byte
	// held is an "event: response.output_text.delta" line waiting for its
	// data line.
	held []byte
	// midLine is set while a line longer than the buffer passes through.
	midLine bool
	// skipBlank drops the blank line that ends a dropped frame.
	skipBlank bool
	err       error
}

func (f *relayKeepaliveFilter) Read(p []byte) (int, error) {
	for len(f.out) == 0 {
		if f.err != nil {
			if len(f.held) > 0 {
				f.out, f.held = f.held, nil
				break
			}
			return 0, f.err
		}
		f.next()
	}
	n := copy(p, f.out)
	f.out = f.out[n:]
	return n, nil
}

// next reads one line (or one chunk of an over-long line) and decides
// whether it is emitted, held or dropped.
func (f *relayKeepaliveFilter) next() {
	line, err := f.r.ReadSlice('\n')
	if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
		f.err = err
	}
	if len(line) == 0 {
		return
	}
	if f.midLine || errors.Is(err, bufio.ErrBufferFull) {
		// Keep-alive frames are short: an over-long line is never one.
		f.midLine = errors.Is(err, bufio.ErrBufferFull)
		f.skipBlank = false
		f.emit(line)
		return
	}
	trimmed := bytes.TrimRight(line, "\r\n")
	if f.skipBlank {
		f.skipBlank = false
		if len(trimmed) == 0 {
			return
		}
	}
	if f.held != nil {
		if isRelayKeepaliveDataLine(trimmed) {
			f.held = nil
			f.skipBlank = true
			return
		}
		f.emit(line)
		return
	}
	if isRelayKeepaliveEventLine(trimmed) {
		f.held = append([]byte(nil), line...)
		return
	}
	if isRelayKeepaliveDataLine(trimmed) {
		f.skipBlank = true
		return
	}
	f.emit(line)
}

// emit queues line after any held event line. line may alias the reader's
// buffer, so it is copied.
func (f *relayKeepaliveFilter) emit(line []byte) {
	out := make([]byte, 0, len(f.held)+len(line))
	out = append(out, f.held...)
	out = append(out, line...)
	f.held = nil
	f.out = out
}

func isRelayKeepaliveEventLine(line []byte) bool {
	rest, ok := bytes.CutPrefix(line, []byte("event:"))
	return ok && bytes.Equal(bytes.TrimSpace(rest), relayKeepaliveDeltaEvent)
}

func isRelayKeepaliveDataLine(line []byte) bool {
	if !bytes.Contains(line, relayKeepaliveMarker) {
		return false
	}
	payload, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return false
	}
	payload = bytes.TrimSpace(payload)
	if !gjson.ValidBytes(payload) {
		return false
	}
	doc := gjson.ParseBytes(payload)
	return doc.Get("type").String() == string(relayKeepaliveDeltaEvent) &&
		doc.Get("delta").String() == "" &&
		(doc.Get("item_id").String() == string(relayKeepaliveMarker) || doc.Get(string(relayKeepaliveMarker)).Bool())
}
