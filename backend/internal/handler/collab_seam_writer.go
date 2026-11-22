package handler

import (
	"bytes"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// collabSeamPendingLimit bounds the bytes of an unfinished frame, or of the
// prelude held back during a takeover. Beyond it the response passes through.
const collabSeamPendingLimit = 256 << 10

type collabSeamMode int32

const (
	collabSeamWatching collabSeamMode = iota // nothing but heartbeats and prelude so far
	collabSeamHolding                        // a terminal error is withheld
	collabSeamPassing                        // output started, or the seam has finished
)

type collabSeamOpKind uint8

const (
	collabSeamWrite collabSeamOpKind = iota
	collabSeamWriteString
	collabSeamFlush
	collabSeamHeaderNow
)

type collabSeamOp struct {
	kind collabSeamOpKind
	data []byte
}

// collabWithheld is a terminal error exactly as the existing path wrote it.
type collabWithheld struct {
	ops []collabSeamOp
	// size is what the writes would have added to the body.
	size int
	// committed reports whether headers were on the wire before the error.
	committed bool
	status    int
	// commitHeader is the header map when the error would have committed;
	// header is the map afterwards, as later readers would have seen it.
	commitHeader http.Header
	header       http.Header
}

// collabSeamWriter lets the existing path run unchanged and withholds only
// the terminal error it ends with before any output: a status >= 400, a
// response.failed or error frame, or a failed JSON response. Everything else
// passes through, and what is withheld can be replayed call by call, so a
// request that is not rescued ends byte for byte as it does today.
//
// Writers installed later (the pre-output keepalive) wrap this one and may
// write from their own goroutine, hence the lock. No method calls upwards.
type collabSeamWriter struct {
	gin.ResponseWriter

	mode atomic.Int32
	mu   sync.Mutex

	// pending is an unfinished frame, or the prelude while quiet; checked is
	// how much of it has been classified.
	pending []byte
	checked int
	// quiet is set during a takeover that began before headers were
	// committed: nothing may commit them until there is output.
	quiet bool

	held         *collabWithheld
	armHeader    http.Header
	commitHeader http.Header
}

func newCollabSeamWriter(inner gin.ResponseWriter) *collabSeamWriter {
	return &collabSeamWriter{ResponseWriter: inner, armHeader: inner.Header().Clone()}
}

func (w *collabSeamWriter) current() collabSeamMode { return collabSeamMode(w.mode.Load()) }
func (w *collabSeamWriter) set(mode collabSeamMode) { w.mode.Store(int32(mode)) }

// Unwrap lets http.ResponseController reach the server's writer.
func (w *collabSeamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *collabSeamWriter) WriteHeader(code int) {
	if w.current() == collabSeamPassing {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// Once bytes are withheld the response counts as written, and gin ignores
	// a status change after that.
	if w.current() == collabSeamHolding || len(w.pending) > 0 {
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *collabSeamWriter) Write(data []byte) (int, error) {
	if w.current() == collabSeamPassing {
		return w.ResponseWriter.Write(data)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.write(collabSeamWrite, data)
}

func (w *collabSeamWriter) WriteString(s string) (int, error) {
	if w.current() == collabSeamPassing {
		return w.ResponseWriter.WriteString(s)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.write(collabSeamWriteString, []byte(s))
}

func (w *collabSeamWriter) write(kind collabSeamOpKind, data []byte) (int, error) {
	switch w.current() {
	case collabSeamPassing:
		return w.forward(kind, data)
	case collabSeamHolding:
		w.hold(kind, data)
		return len(data), nil
	}
	committed := w.ResponseWriter.Written()
	if !committed && len(w.pending) == 0 {
		if w.Status() >= http.StatusBadRequest {
			w.hold(kind, data)
			return len(data), nil
		}
		if len(data) == 0 {
			if w.quiet {
				return 0, nil
			}
			return w.forward(kind, data)
		}
	}
	if len(w.pending) > 0 || collabSeamEventStream(w.Header()) {
		return w.writeFrames(kind, data, committed)
	}
	if !committed && collabSeamFailedJSON(data) {
		w.hold(kind, data)
		return len(data), nil
	}
	// The mode changes only after the write: passing writers skip the lock.
	n, err := w.forward(kind, data)
	w.set(collabSeamPassing)
	return n, err
}

func (w *collabSeamWriter) writeFrames(kind collabSeamOpKind, data []byte, committed bool) (int, error) {
	earlier := len(w.pending)
	buf := data
	if earlier > 0 {
		buf = append(w.pending, data...)
	}
	pos := w.checked
	output := false
	for pos < len(buf) {
		end := collabSeamFrameEnd(buf[pos:], earlier-pos)
		if end == 0 {
			break
		}
		frame := collabSeamClassify(buf[pos : pos+end])
		if frame == collabSeamFailedFrame {
			// Withhold the whole write, so a prelude that came with the error
			// does not commit the response on its own.
			w.pending, w.checked = nil, 0
			w.hold(kind, buf)
			return len(data), nil
		}
		pos += end
		if frame == collabSeamOutputFrame {
			output = true
			break
		}
	}

	out, pass := pos, false
	switch {
	case output || len(buf)-pos > collabSeamPendingLimit || len(buf) > collabSeamPendingLimit && w.quiet && !committed:
		out, pass = len(buf), true
	case w.quiet && !committed:
		out = 0
	}

	var err error
	var written int
	if out > 0 {
		written, err = w.forward(kind, buf[:out])
	}
	if pass || err != nil {
		// Again only after the write.
		defer w.set(collabSeamPassing)
	}
	if err != nil {
		w.pending, w.checked = nil, 0
		return min(len(data), max(0, written-earlier)), err
	}
	if out == len(buf) {
		w.pending, w.checked = nil, 0
		return len(data), nil
	}
	if !committed && out == 0 && earlier == 0 {
		w.commitHeader = w.Header().Clone()
	}
	// The caller may reuse data, so the remainder is always copied.
	if earlier > 0 && out == 0 {
		w.pending = buf
	} else {
		w.pending = bytes.Clone(buf[out:])
	}
	w.checked = pos - out
	return len(data), nil
}

// forward writes through, committing with the headers of the moment the
// response first counted as written.
func (w *collabSeamWriter) forward(kind collabSeamOpKind, data []byte) (n int, err error) {
	w.committing(w.commitHeader, func() {
		switch kind {
		case collabSeamWriteString:
			n, err = w.ResponseWriter.WriteString(string(data))
		default:
			n, err = w.ResponseWriter.Write(data)
		}
	})
	return n, err
}

// committing runs a call that may commit the response as if it happened when
// the headers were as in at.
func (w *collabSeamWriter) committing(at http.Header, call func()) {
	if at == nil || w.ResponseWriter.Written() {
		call()
		return
	}
	later := w.Header().Clone()
	collabSeamSetHeader(w.Header(), at)
	call()
	w.commitHeader = nil
	// Header has to be fetched again: after the commit the server hands out a
	// map whose changes no longer reach the wire.
	collabSeamSetHeader(w.Header(), later)
}

func (w *collabSeamWriter) hold(kind collabSeamOpKind, data []byte) {
	if w.held == nil {
		held := &collabWithheld{committed: w.ResponseWriter.Written(), status: w.Status()}
		if !held.committed {
			held.commitHeader = w.commitHeader
			if held.commitHeader == nil {
				held.commitHeader = w.Header().Clone()
			}
		}
		w.held, w.commitHeader = held, nil
		w.set(collabSeamHolding)
	}
	w.held.ops = append(w.held.ops, collabSeamOp{kind: kind, data: bytes.Clone(data)})
	w.held.size += len(data)
}

func (w *collabSeamWriter) Flush() {
	if w.current() == collabSeamPassing {
		w.ResponseWriter.Flush()
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commitOnly(collabSeamFlush)
}

func (w *collabSeamWriter) WriteHeaderNow() {
	if w.current() == collabSeamPassing {
		w.ResponseWriter.WriteHeaderNow()
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commitOnly(collabSeamHeaderNow)
}

func (w *collabSeamWriter) commitOnly(kind collabSeamOpKind) {
	if w.current() == collabSeamHolding {
		w.hold(kind, nil)
		return
	}
	if w.current() == collabSeamWatching && !w.ResponseWriter.Written() {
		if len(w.pending) == 0 && w.Status() >= http.StatusBadRequest {
			w.hold(kind, nil)
			return
		}
		if w.quiet || len(w.pending) > 0 {
			return
		}
	}
	if kind == collabSeamFlush {
		w.ResponseWriter.Flush()
		return
	}
	w.ResponseWriter.WriteHeaderNow()
}

// Written and Size answer as if the withheld bytes had been written: the
// existing path decides on failover and on error forms from both.
func (w *collabSeamWriter) Written() bool {
	if w.current() == collabSeamPassing {
		return w.ResponseWriter.Written()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.held != nil || len(w.pending) > 0 || w.ResponseWriter.Written()
}

func (w *collabSeamWriter) Size() int {
	if w.current() == collabSeamPassing {
		return w.ResponseWriter.Size()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	size := w.ResponseWriter.Size()
	if w.held == nil && len(w.pending) == 0 {
		return size
	}
	size = max(0, size) + len(w.pending)
	if w.held != nil {
		size += w.held.size
	}
	return size
}

// withholding reports whether the existing path ended in a withheld error.
func (w *collabSeamWriter) withholding() bool { return w.current() == collabSeamHolding }

// suspend takes the withheld error out and prepares the writer for a
// takeover. If headers were not committed, the response looks untouched and
// stays uncommitted until the takeover produces output.
func (w *collabSeamWriter) suspend() *collabWithheld {
	w.mu.Lock()
	defer w.mu.Unlock()
	held := w.held
	if held == nil {
		return nil
	}
	held.header = w.Header().Clone()
	w.held, w.pending, w.checked = nil, nil, 0
	w.set(collabSeamWatching)
	if !held.committed {
		w.resetUncommitted()
		w.quiet = true
	}
	return held
}

// discard drops what a failed takeover attempt left, before the next one.
func (w *collabSeamWriter) discard() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.current() == collabSeamPassing {
		return
	}
	w.held, w.pending, w.checked = nil, nil, 0
	w.set(collabSeamWatching)
	if !w.ResponseWriter.Written() {
		w.resetUncommitted()
	}
}

func (w *collabSeamWriter) resetUncommitted() {
	w.commitHeader = nil
	collabSeamSetHeader(w.Header(), w.armHeader)
	w.ResponseWriter.WriteHeader(http.StatusOK)
}

// resume puts the saved error back in place of whatever the takeover left.
// It reports false when the takeover's output already reached the client;
// the saved error must not follow that.
func (w *collabSeamWriter) resume(saved *collabWithheld) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if saved == nil || w.current() == collabSeamPassing {
		return false
	}
	w.quiet, w.pending, w.checked, w.commitHeader = false, nil, 0, nil
	if !saved.committed && !w.ResponseWriter.Written() {
		w.ResponseWriter.WriteHeader(saved.status)
	}
	collabSeamSetHeader(w.Header(), saved.header)
	w.held = saved
	w.set(collabSeamHolding)
	return true
}

// release ends the seam: whatever is withheld goes out as the existing path
// wrote it, and every later write passes straight through.
func (w *collabSeamWriter) release() {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer w.set(collabSeamPassing)
	w.quiet = false
	if len(w.pending) > 0 {
		_, _ = w.forward(collabSeamWrite, w.pending)
		w.pending, w.checked = nil, 0
	}
	held := w.held
	w.held = nil
	if held == nil {
		return
	}
	for i, op := range held.ops {
		call := func() {
			switch op.kind {
			case collabSeamWrite:
				_, _ = w.ResponseWriter.Write(op.data)
			case collabSeamWriteString:
				_, _ = w.ResponseWriter.WriteString(string(op.data))
			case collabSeamFlush:
				w.ResponseWriter.Flush()
			case collabSeamHeaderNow:
				w.ResponseWriter.WriteHeaderNow()
			}
		}
		if i == 0 {
			w.committing(held.commitHeader, call)
			continue
		}
		call()
	}
}

func collabSeamSetHeader(dst, src http.Header) {
	clear(dst)
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
}

func collabSeamEventStream(header http.Header) bool {
	return strings.Contains(strings.ToLower(header.Get("Content-Type")), "text/event-stream")
}

func collabSeamFailedJSON(data []byte) bool {
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	status := gjson.GetBytes(data, "status")
	return status.Type == gjson.String && status.String() == "failed"
}

// collabSeamFrameEnd returns the length of the first complete frame of buf,
// 0 if there is none. scanned is how much of buf was already searched.
func collabSeamFrameEnd(buf []byte, scanned int) int {
	if bytes.HasPrefix(buf, []byte("\n")) {
		return 1
	}
	if bytes.HasPrefix(buf, []byte("\r\n")) {
		return 2
	}
	// A separator can straddle the previous write by up to two bytes.
	for from := max(0, scanned-2); from < len(buf); {
		newline := bytes.IndexByte(buf[from:], '\n')
		if newline < 0 {
			return 0
		}
		next := from + newline + 1
		if next < len(buf) && buf[next] == '\n' {
			return next + 1
		}
		if next+1 < len(buf) && buf[next] == '\r' && buf[next+1] == '\n' {
			return next + 2
		}
		from = next
	}
	return 0
}

type collabSeamFrame uint8

const (
	collabSeamPreludeFrame collabSeamFrame = iota
	collabSeamFailedFrame
	collabSeamOutputFrame
)

// collabSeamClassify is deliberately narrow: whatever is not a heartbeat,
// prelude or terminal error counts as output and ends the watch.
func collabSeamClassify(frame []byte) collabSeamFrame {
	var event string
	var data []byte
	hasData := false
	for line := range bytes.Lines(frame) {
		line = bytes.TrimRight(line, "\r\n")
		if value, ok := bytes.CutPrefix(line, []byte("event:")); ok {
			event = string(bytes.TrimSpace(value))
		} else if value, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			if hasData {
				data = append(append(bytes.Clone(data), '\n'), bytes.TrimSpace(value)...)
			} else {
				data, hasData = bytes.TrimSpace(value), true
			}
		}
	}
	if event == "" && !hasData || string(data) == "[DONE]" {
		return collabSeamPreludeFrame
	}
	typ := gjson.GetBytes(data, "type").String()
	if event == "response.failed" || event == "error" || typ == "response.failed" || typ == "error" {
		return collabSeamFailedFrame
	}
	if typ == "" {
		typ = event
	}
	switch typ {
	case "ping", "keepalive", "response.created", "response.in_progress", "response.queued",
		"response.metadata", "codex.response.metadata", "codex.rate_limits":
		return collabSeamPreludeFrame
	}
	return collabSeamOutputFrame
}
