package handler

import (
	"bytes"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

const (
	// collabSeamPendingLimit bounds an unfinished frame of the existing path.
	// Beyond it the response passes through and cannot be taken over.
	collabSeamPendingLimit = 256 << 10
	// collabSeamQuietLimit bounds what a takeover may write before output.
	// Created and in-progress events echo instructions and tools, so it is
	// generous. Beyond it the attempt is abandoned, never delivered.
	collabSeamQuietLimit = 4 << 20
	// Beyond these a withheld error goes out at once and is not taken over.
	collabSeamHeldLimit = 1 << 20
	collabSeamHeldOps   = 128
)

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

// collabSeamWriter lets the existing path run unchanged and withholds only a
// terminal error before any output, and only while rescuable is set because
// a takeover could act on it: a status >= 400, a response.failed or error frame,
// or a failed JSON response. Everything else goes out when it is written, and
// what is withheld can be replayed call by call, so a request that is not
// rescued ends byte for byte as it does today.
//
// Writers installed later (the pre-output keepalive) wrap this one and may
// write from their own goroutine, hence the lock. No method calls upwards.
type collabSeamWriter struct {
	gin.ResponseWriter

	mode atomic.Int32
	mu   sync.Mutex

	// pending is an unfinished frame, or while quiet everything written so
	// far. checked is how much of it has been classified and sent how much
	// went out already: what cannot be taken over is watched, never delayed.
	pending []byte
	checked int
	sent    int
	// document follows the JSON response of a takeover.
	document collabJSONScan
	// rescuable is only read here: no callback ever runs under the writer locks.
	rescuable *atomic.Bool
	// quiet is set during a takeover: until it has semantic output nothing of
	// it reaches the client, neither headers, prelude, heartbeats nor errors,
	// whether or not the existing path had committed the headers.
	quiet bool

	held         *collabWithheld
	armHeader    http.Header
	commitHeader http.Header
}

func newCollabSeamWriter(inner gin.ResponseWriter, rescuable *atomic.Bool) *collabSeamWriter {
	return &collabSeamWriter{ResponseWriter: inner, armHeader: inner.Header().Clone(), rescuable: rescuable}
}

// intercepts reports whether a terminal error written now is withheld.
func (w *collabSeamWriter) intercepts() bool {
	return w.quiet || w.rescuable != nil && w.rescuable.Load()
}

func (w *collabSeamWriter) unsent() int { return len(w.pending) - w.sent }

func (w *collabSeamWriter) forget() {
	w.pending, w.checked, w.sent = nil, 0, 0
	w.document = collabJSONScan{}
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
	if w.current() == collabSeamHolding || w.unsent() > 0 {
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
		if w.yield() {
			return w.forward(kind, data)
		}
		w.hold(kind, data)
		return len(data), nil
	}
	committed := w.ResponseWriter.Written()
	if !committed && len(w.pending) == 0 {
		if w.Status() >= http.StatusBadRequest {
			if w.intercepts() {
				w.hold(kind, data)
				return len(data), nil
			}
			return w.pass(kind, data)
		}
		if len(data) == 0 {
			if w.quiet {
				return 0, nil
			}
			return w.forward(kind, data)
		}
	}
	stream := collabSeamEventStream(w.Header())
	if w.quiet && !stream {
		return w.writeJSON(kind, data, committed)
	}
	if len(w.pending) > 0 || stream {
		return w.writeFrames(kind, data, committed)
	}
	if !committed && collabSeamFailedJSON(data) && w.intercepts() {
		w.hold(kind, data)
		return len(data), nil
	}
	return w.pass(kind, data)
}

// yield lets a withheld error go once it is not rescuable any more.
func (w *collabSeamWriter) yield() bool {
	if w.intercepts() {
		return false
	}
	w.replay()
	w.set(collabSeamPassing)
	return true
}

// pass writes through and ends the watch. A takeover of which no byte went
// out delivered nothing: the saved error is still owed.
func (w *collabSeamWriter) pass(kind collabSeamOpKind, data []byte) (int, error) {
	n, err := w.forward(kind, data)
	if w.quiet && err != nil && n <= 0 {
		w.hold(kind, nil)
		return n, err
	}
	// The mode changes only after the write: passing writers skip the lock.
	w.set(collabSeamPassing)
	return n, err
}

// writeJSON collects a takeover's JSON response. It may come in any number of
// writes and replaces the saved error only when the write that closes the
// document leaves it valid and with output. Later writes pass through.
func (w *collabSeamWriter) writeJSON(kind collabSeamOpKind, data []byte, committed bool) (int, error) {
	earlier := len(w.pending)
	buf := data
	if earlier > 0 {
		buf = append(w.pending, data...)
	} else if !committed {
		w.commitHeader = w.Header().Clone()
	}
	w.document.feed(data)
	switch {
	case w.document.closed:
		w.forget()
		if !collabSeamJSONOutput(buf) {
			w.hold(kind, buf)
			break
		}
		n, err := w.pass(kind, buf)
		if err != nil {
			return min(len(data), max(0, n-earlier)), err
		}
	case w.document.notObject, len(buf) > collabSeamQuietLimit:
		w.forget()
		w.hold(kind, buf)
	case earlier > 0:
		w.pending = buf
	default:
		w.pending = bytes.Clone(buf)
	}
	return len(data), nil
}

// collabJSONScan finds where an object that arrives in parts closes, looking
// at every byte once. It does not validate: that happens once, at the end.
type collabJSONScan struct {
	depth             int
	quoted, escaped   bool
	closed, notObject bool
}

func (j *collabJSONScan) feed(data []byte) {
	for _, b := range data {
		switch {
		case j.closed || j.notObject:
			return
		case j.escaped:
			j.escaped = false
		case j.quoted:
			switch b {
			case '\\':
				j.escaped = true
			case '"':
				j.quoted = false
			}
		case b == ' ' || b == '\t' || b == '\r' || b == '\n':
		case j.depth == 0:
			if b == '{' {
				j.depth = 1
			} else {
				j.notObject = true
			}
		case b == '"':
			j.quoted = true
		case b == '{' || b == '[':
			j.depth++
		case b == '}' || b == ']':
			j.depth--
			j.closed = j.depth == 0
		}
	}
}

func (w *collabSeamWriter) writeFrames(kind collabSeamOpKind, data []byte, committed bool) (int, error) {
	earlier, sent := len(w.pending), w.sent
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
		frame, event, payload := collabSeamClassify(buf[pos : pos+end])
		if frame == collabSeamFailedFrame {
			if sent == 0 && w.intercepts() {
				// Withhold the whole write, so a prelude that came with the
				// error does not commit the response on its own.
				w.forget()
				w.hold(kind, buf)
				return len(data), nil
			}
			// What cannot be taken over ends the watch like output.
			output = true
			break
		}
		pos += end
		if frame != collabSeamOutputFrame {
			continue
		}
		// Anything ends the watch on the existing path. A takeover counts as
		// delivered only with semantic output, by the gateway's own definition:
		// an empty item or text part must not replace the saved error.
		if !w.quiet || resilience.ResponsesEventOutputDisposition(event, payload).SemanticOutput {
			output = true
			break
		}
	}

	// out is how far buf goes out now, keep where what stays pending starts.
	out, keep, last := pos, pos, false
	switch {
	case output:
		out, keep, last = len(buf), len(buf), true
	case w.quiet:
		if len(buf) > collabSeamQuietLimit {
			w.forget()
			w.hold(kind, nil)
			return len(data), nil
		}
		out, keep = 0, 0
	case len(buf)-pos > collabSeamPendingLimit:
		out, keep, last = len(buf), len(buf), true
	case pos < len(buf) && (pos < sent || !w.intercepts()):
		// An unfinished frame waits only if it could end in a takeover.
		out = len(buf)
	}

	var err error
	var written int
	if out > sent {
		if last {
			written, err = w.pass(kind, buf[sent:out])
		} else if written, err = w.forward(kind, buf[sent:out]); err != nil {
			w.set(collabSeamPassing)
		}
	} else if last {
		w.set(collabSeamPassing)
	}
	if err != nil {
		w.forget()
		return min(len(data), max(0, written-(earlier-sent))), err
	}
	if keep == len(buf) {
		w.forget()
		return len(data), nil
	}
	if !committed && out == 0 && earlier == 0 {
		w.commitHeader = w.Header().Clone()
	}
	// The caller may reuse data, so the remainder is always copied.
	if earlier > 0 && keep == 0 {
		w.pending = buf
	} else {
		w.pending = bytes.Clone(buf[keep:])
	}
	w.checked, w.sent = pos-keep, max(0, out-keep)
	return len(data), nil
}

// forward writes through, committing with the headers of the moment the
// response first counted as written.
func (w *collabSeamWriter) forward(kind collabSeamOpKind, data []byte) (n int, err error) {
	if w.quiet {
		// A takeover's parts are joined here, so a length it copied may be wrong.
		w.Header().Del("Content-Length")
		w.commitHeader.Del("Content-Length")
	}
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
	w.held.size += len(data)
	if w.quiet {
		// A failed attempt's error is never replayed, only its size is kept.
		return
	}
	w.held.ops = append(w.held.ops, collabSeamOp{kind: kind, data: bytes.Clone(data)})
	if w.held.size > collabSeamHeldLimit || len(w.held.ops) > collabSeamHeldOps {
		w.replay()
		w.set(collabSeamPassing)
	}
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
	if w.current() == collabSeamHolding && !w.yield() {
		w.hold(kind, nil)
		return
	}
	if w.current() == collabSeamWatching && w.unsent() > 0 && !w.intercepts() {
		// Held back as rescuable, which it no longer is.
		if _, err := w.forward(collabSeamWrite, w.pending[w.sent:]); err != nil {
			w.set(collabSeamPassing)
		}
		w.sent = len(w.pending)
	}
	if w.current() == collabSeamWatching && !w.ResponseWriter.Written() {
		if len(w.pending) == 0 && w.Status() >= http.StatusBadRequest && w.intercepts() {
			w.hold(kind, nil)
			return
		}
		if w.unsent() > 0 {
			return
		}
	}
	if w.quiet && w.current() == collabSeamWatching {
		return
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
	return w.held != nil || w.unsent() > 0 || w.ResponseWriter.Written()
}

func (w *collabSeamWriter) Size() int {
	if w.current() == collabSeamPassing {
		return w.ResponseWriter.Size()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	size := w.ResponseWriter.Size()
	if w.held == nil && w.unsent() == 0 {
		return size
	}
	size = max(0, size) + w.unsent()
	if w.held != nil {
		size += w.held.size
	}
	return size
}

// withholding reports whether the existing path ended in a withheld error.
func (w *collabSeamWriter) withholding() bool { return w.current() == collabSeamHolding }

// suspend takes the withheld error out and prepares the writer for a
// takeover: nothing of it goes out until it has semantic output, and if
// headers were not committed the response looks untouched until then.
func (w *collabSeamWriter) suspend() *collabWithheld {
	w.mu.Lock()
	defer w.mu.Unlock()
	held := w.held
	if held == nil {
		return nil
	}
	held.header = w.Header().Clone()
	w.held = nil
	w.forget()
	w.set(collabSeamWatching)
	w.quiet = true
	if !held.committed {
		w.resetUncommitted()
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
	w.held = nil
	w.forget()
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
	w.quiet, w.commitHeader = false, nil
	w.forget()
	switch {
	case saved.committed:
	case w.ResponseWriter.Written():
		// An attempt committed the response and then could not write. The saved
		// status and headers cannot come back, so its body does not follow them.
		// That is still no delivery.
		saved = &collabWithheld{committed: true, header: saved.header}
	default:
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
	if w.unsent() > 0 {
		_, _ = w.forward(collabSeamWrite, w.pending[w.sent:])
	}
	w.forget()
	w.replay()
}

func (w *collabSeamWriter) replay() {
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

// collabSeamJSONOutput reports whether a takeover's JSON response has output
// worth replacing the saved error with.
func collabSeamJSONOutput(data []byte) bool {
	if !gjson.ValidBytes(data) {
		return false
	}
	response := gjson.ParseBytes(data)
	status, output := response.Get("status"), response.Get("output")
	if !output.IsArray() || status.Exists() && status.String() != "completed" && status.String() != "incomplete" {
		return false
	}
	payload := append(append([]byte(`{"type":"response.completed","response":`), data...), '}')
	return resilience.ResponsesEventOutputDisposition("response.completed", payload).SemanticOutput
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
func collabSeamClassify(frame []byte) (kind collabSeamFrame, event string, data []byte) {
	var joined bytes.Buffer // only for an event with several data lines
	lines := 0
	for line := range bytes.Lines(frame) {
		line = bytes.TrimRight(line, "\r\n")
		if value, ok := bytes.CutPrefix(line, []byte("event:")); ok {
			event = string(bytes.TrimSpace(value))
		} else if value, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			value = bytes.TrimSpace(value)
			lines++
			if lines == 1 {
				data = value
				continue
			}
			if lines == 2 {
				_, _ = joined.Write(data)
			}
			_ = joined.WriteByte('\n')
			_, _ = joined.Write(value)
		}
	}
	if lines > 1 {
		data = joined.Bytes()
	}
	if event == "" && lines == 0 || string(data) == "[DONE]" {
		return collabSeamPreludeFrame, event, data
	}
	typ := gjson.GetBytes(data, "type").String()
	if event == "response.failed" || event == "error" || typ == "response.failed" || typ == "error" {
		return collabSeamFailedFrame, event, data
	}
	if typ == "" {
		typ = event
	}
	switch typ {
	case "ping", "keepalive", "response.created", "response.in_progress", "response.queued",
		"response.metadata", "codex.response.metadata", "codex.rate_limits":
		return collabSeamPreludeFrame, event, data
	}
	return collabSeamOutputFrame, event, data
}
