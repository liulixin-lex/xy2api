package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
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
	return &relayKeepaliveFilter{r: bufio.NewReaderSize(src, 4096), live: live}
}

// relayLiveness records when a relay keep-alive frame was last dropped from
// an upstream stream: the relay is alive although it sent nothing. Stall
// detectors take the later of it and their last read, so a relay that keeps
// a quiet stream open only with these frames is not cut as stalled, as in
// 0.3.0 where the frames passed through; the frames are still no output.
// Each dropped frame is counted per account.
type relayLiveness struct {
	at        atomic.Int64
	groupID   int64
	accountID int64
	platform  string
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
	l.at.Store(time.Now().UnixNano())
	resilience.Count(resilience.MetricRelayKeepalive, l.groupID, l.accountID, l.platform)
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
	err       error
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
	if f.holding {
		if isRelayKeepaliveDataLine(trimmed) {
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
	if isRelayKeepaliveDataLine(trimmed) {
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
