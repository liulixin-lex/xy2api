package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
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
// upstream SSE body (design §7.8) and notes each one on live (may be nil).
// Everything else passes through byte for byte; it is a no-op when
// resilience is switched off.
func newOpenAIRelayKeepaliveFilter(src io.Reader, live *relayLiveness) io.Reader {
	if src == nil || !resilience.Current().Enabled {
		return src
	}
	return &relayKeepaliveFilter{r: bufio.NewReaderSize(src, 4096), live: live, nativePrelude: resilience.Current().NativePreludeWhitelistOn()}
}

// relayLiveness records when a relay keep-alive frame was last dropped from
// an upstream stream: the relay is alive although it sent nothing. Stall
// detectors take the later of it and their last read, so a relay that keeps
// a quiet stream open only with these frames is not cut as stalled, as in
// 0.3.0 where the frames passed through; the frames are still no output.
// Each dropped frame is counted per account. The first one is kept for the
// first-token time of the request log (firstTokenMs).
type relayLiveness struct {
	at        atomic.Int64
	firstDrop atomic.Int64
	groupID   int64
	accountID int64
	platform  string
}

// preCreatedDelta counts real text sent before created. Never log the text: it
// must pass through as content, preserving the upstream order and replay boundary.
func (l *relayLiveness) preCreatedDelta(byteCount int) {
	if l == nil {
		return
	}
	resilience.Count(resilience.MetricPreCreatedDelta, l.groupID, l.accountID, l.platform)
	slog.Info("resilience.pre_created_delta", "group_id", l.groupID, "account_id", l.accountID, "bytes", byteCount)
}

// newRelayLiveness returns the liveness of one upstream attempt.
func newRelayLiveness(ctx context.Context, account *Account) *relayLiveness {
	l := &relayLiveness{groupID: resilience.StateFrom(ctx).GroupID()}
	if account != nil {
		l.accountID, l.platform = account.ID, account.Platform
	}
	return l
}

// relayLivenessFor is newRelayLiveness for handlers that hold the gin context.
func relayLivenessFor(c *gin.Context, account *Account) *relayLiveness {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	return newRelayLiveness(ctx, account)
}

func (l *relayLiveness) frameDropped() {
	if l == nil {
		return
	}
	now := time.Now().UnixNano()
	l.at.Store(now)
	l.firstDrop.CompareAndSwap(0, now)
	resilience.Count(resilience.MetricRelayKeepalive, l.groupID, l.accountID, l.platform)
}

// firstTokenMs is the first-token latency the request log records. In
// semantic mode the first dropped frame counts, from the start of the
// attempt, as 0.3.0 counted the frame it passed through; recorded is the
// first real event. Visible mode, and every timing decision of the stream,
// see real events only.
func (l *relayLiveness) firstTokenMs(recorded *int, start time.Time, mode string) *int {
	if l == nil || mode != OpenAITTFTModeSemantic {
		return recorded
	}
	at := l.firstDrop.Load()
	if at == 0 {
		return recorded
	}
	ms := max(int(time.Unix(0, at).Sub(start).Milliseconds()), 0)
	if recorded != nil && *recorded <= ms {
		return recorded
	}
	return &ms
}

// lastActivity is the later of lastRead and the last dropped frame.
func (l *relayLiveness) lastActivity(lastRead time.Time) time.Time {
	if l == nil {
		return lastRead
	}
	if at := l.at.Load(); at > 0 && time.Unix(0, at).After(lastRead) {
		return time.Unix(0, at)
	}
	return lastRead
}

type relayKeepaliveFilter struct {
	r    *bufio.Reader
	live *relayLiveness
	// out holds bytes ready for the caller.
	out []byte
	// held is an "event: response.output_text.delta" line waiting for its
	// data line while holding is set.
	held    []byte
	holding bool
	// buf backs out when a held line is emitted; reused once out is drained.
	buf []byte
	// midLine is set while a line longer than the buffer passes through.
	midLine bool
	// skipBlank drops the blank line that ends a dropped frame.
	skipBlank bool
	// createdSeen closes the unmarked empty-delta window at created or the
	// first real delta; later whitespace is part of the answer.
	createdSeen   bool
	nativePrelude bool
	err           error
}

func (f *relayKeepaliveFilter) Read(p []byte) (int, error) {
	for len(f.out) == 0 {
		if f.err != nil {
			if f.holding {
				f.out, f.holding = f.held, false
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
		// Keep-alive frames are short: an over-long line is never one. Its
		// contents may include created; disable the new rule conservatively
		// rather than dropping an ordinary empty delta after a large created.
		f.createdSeen = true
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
	if bytes.HasPrefix(trimmed, []byte("event:")) && bytes.Equal(bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("event:"))), []byte("response.created")) {
		f.createdSeen = true
	}
	if f.nativePrelude && !f.createdSeen && isResponsesCreatedDataLine(trimmed) {
		f.createdSeen = true
	}
	if f.nativePrelude && !f.createdSeen {
		if deltaBytes, ok := nonEmptyResponsesDeltaBytes(trimmed); ok {
			f.createdSeen = true
			f.live.preCreatedDelta(deltaBytes)
		}
	}
	if f.holding {
		if isRelayKeepaliveDataLine(trimmed) || (f.nativePrelude && !f.createdSeen && isEmptyResponsesDeltaLine(trimmed)) {
			f.holding = false
			f.skipBlank = true
			f.live.frameDropped()
			return
		}
		f.emit(line)
		return
	}
	if isRelayKeepaliveEventLine(trimmed) {
		f.held = append(f.held[:0], line...)
		f.holding = true
		return
	}
	if isRelayKeepaliveDataLine(trimmed) || (f.nativePrelude && !f.createdSeen && isEmptyResponsesDeltaLine(trimmed)) {
		f.skipBlank = true
		f.live.frameDropped()
		return
	}
	f.emit(line)
}

// emit queues line after any held event line. line aliases the reader's
// buffer, which stays valid until the next read: Read hands out all of out
// before next reads again, so line and buf can be handed out as they are.
func (f *relayKeepaliveFilter) emit(line []byte) {
	if !f.holding {
		f.out = line
		return
	}
	f.buf = append(append(f.buf[:0], f.held...), line...)
	f.holding = false
	f.out = f.buf
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

func isResponsesCreatedDataLine(line []byte) bool {
	payload, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
	if !ok || !gjson.ValidBytes(bytes.TrimSpace(payload)) {
		return false
	}
	return gjson.GetBytes(bytes.TrimSpace(payload), "type").String() == "response.created"
}

// isEmptyResponsesDeltaLine deliberately ignores relay markers. It only
// describes the protocol delta shape; the caller decides whether it is still
// before response.created.
func isEmptyResponsesDeltaLine(line []byte) bool {
	delta, ok := responsesDeltaValue(line)
	return ok && strings.TrimSpace(delta) == ""
}

func nonEmptyResponsesDeltaBytes(line []byte) (int, bool) {
	delta, ok := responsesDeltaValue(line)
	if !ok || strings.TrimSpace(delta) == "" {
		return 0, false
	}
	return len([]byte(delta)), true
}

func responsesDeltaValue(line []byte) (string, bool) {
	payload, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
	if !ok || !gjson.ValidBytes(bytes.TrimSpace(payload)) {
		return "", false
	}
	doc := gjson.ParseBytes(bytes.TrimSpace(payload))
	if doc.Get("type").String() != string(relayKeepaliveDeltaEvent) {
		return "", false
	}
	delta := doc.Get("delta")
	if !delta.Exists() || delta.Type != gjson.String {
		return "", false
	}
	return delta.String(), true
}
