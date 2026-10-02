package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
)

const nativeStreamFailureDrainTimeout = 3 * time.Second

var errNativeSSEUpstreamIdle = fmt.Errorf("upstream SSE idle timeout: %w", context.DeadlineExceeded)

// Track actual upstream bytes, including fragments of an event. A complete-frame
// scanner cannot distinguish a large event still arriving from an idle body.
type nativeSSEReadProgressBody struct {
	io.ReadCloser
	started  time.Time
	lastRead atomic.Int64
}

func newNativeSSEReadProgressBody(body io.ReadCloser) *nativeSSEReadProgressBody {
	return &nativeSSEReadProgressBody{ReadCloser: body, started: time.Now()}
}

func (b *nativeSSEReadProgressBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.lastRead.Store(int64(time.Since(b.started)))
	}
	return n, err
}

func (b *nativeSSEReadProgressBody) LastReadAt() time.Time {
	return b.started.Add(time.Duration(b.lastRead.Load()))
}

// Only protocol data commits a generation. SSE comments remain local liveness.
func nativeSSEEventData(frame []byte) []byte {
	var data []byte
	for _, line := range bytes.Split(frame, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		value := bytes.TrimPrefix(line, []byte("data:"))
		value = bytes.TrimPrefix(value, []byte(" "))
		if data == nil {
			data = value
		} else {
			joined := make([]byte, 0, len(data)+len(value)+1)
			joined = append(joined, data...)
			joined = append(joined, '\n')
			data = append(joined, value...)
		}
	}
	return bytes.TrimSpace(data)
}

type nativeSSEScanner interface {
	Scan() bool
	Text() string
	Err() error
}
type nativeSSEScanEvent struct {
	line   string
	err    error
	readAt time.Time
}

// The caller remains the sole downstream writer; only reading runs separately.
// Its one-token queue bounds read-ahead and cancellation closes the actual body.
type nativeSSEIdleScanner struct {
	ctx          context.Context
	body         io.ReadCloser
	events       <-chan nativeSSEScanEvent
	done         chan struct{}
	stopped      chan struct{}
	ticker       *time.Ticker
	interval     time.Duration
	upstreamIdle time.Duration
	idleTimer    *time.Timer
	heartbeat    func() error
	line         string
	readAt       time.Time
	err          error
	failureTimer *time.Timer
	failureDone  <-chan time.Time
}

func newNativeSSEIdleScanner(ctx context.Context, body io.ReadCloser, scanner nativeSSEScanner, interval time.Duration, heartbeat func() error, upstreamIdle ...time.Duration) *nativeSSEIdleScanner {
	events := make(chan nativeSSEScanEvent, 1)
	s := &nativeSSEIdleScanner{ctx: ctx, body: body, events: events, done: make(chan struct{}), stopped: make(chan struct{}), heartbeat: heartbeat, interval: interval}
	if len(upstreamIdle) > 0 {
		s.upstreamIdle = upstreamIdle[0]
	}
	if interval > 0 {
		s.ticker = time.NewTicker(interval)
	}
	go func() {
		defer close(events)
		defer close(s.stopped)
		for scanner.Scan() {
			select {
			case events <- nativeSSEScanEvent{line: scanner.Text(), readAt: nativeSSEReadAt(scanner)}:
			case <-s.done:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case events <- nativeSSEScanEvent{err: err}:
			case <-s.done:
			}
		}
	}()
	return s
}
func (s *nativeSSEIdleScanner) Scan() bool {
	var tick <-chan time.Time
	if s.ticker != nil {
		tick = s.ticker.C
	}
	var idle <-chan time.Time
	waitStarted := time.Now()
	if s.upstreamIdle > 0 {
		if s.idleTimer == nil {
			s.idleTimer = time.NewTimer(s.upstreamIdle)
		} else {
			s.idleTimer.Reset(s.upstreamIdle)
		}
		idle = s.idleTimer.C
	}
	for {
		select {
		case <-s.failureDone:
			s.err = io.ErrUnexpectedEOF
			_ = s.body.Close()
			return false
		case <-s.ctx.Done():
			s.err = s.ctx.Err()
			_ = s.body.Close()
			return false
		case <-idle:
			// A queued read wins a timer race; downstream writing time is excluded
			// because the deadline is armed only while Scan waits for upstream.
			select {
			case event, ok := <-s.events:
				return s.accept(event, ok)
			default:
			}
			lastProgress := waitStarted
			if progress, ok := s.body.(interface{ LastReadAt() time.Time }); ok && progress.LastReadAt().After(lastProgress) {
				lastProgress = progress.LastReadAt()
			}
			if remaining := s.upstreamIdle - time.Since(lastProgress); remaining > 0 {
				s.idleTimer.Reset(remaining)
				continue
			}
			s.err = errNativeSSEUpstreamIdle
			_ = s.body.Close()
			return false
		case <-tick:
			if err := s.heartbeat(); err != nil {
				s.err = err
				_ = s.body.Close()
				return false
			}
		case event, ok := <-s.events:
			return s.accept(event, ok)
		}
	}
}

func (s *nativeSSEIdleScanner) accept(event nativeSSEScanEvent, ok bool) bool {
	if s.ctx.Err() != nil {
		s.err = s.ctx.Err()
		return false
	}
	if !ok {
		return false
	}
	if data, ok := extractOpenAISSEDataLine(event.line); ok && gjson.Get(data, "type").String() == "error" && s.failureTimer == nil {
		s.failureTimer = time.NewTimer(nativeStreamFailureDrainTimeout)
		s.failureDone = s.failureTimer.C
	}
	s.line, s.err, s.readAt = event.line, event.err, event.readAt
	return event.err == nil
}

// ResetHeartbeat is called only by the sole downstream writer after delivery.
// Measure idle time from the last write instead of the scanner's construction.
func (s *nativeSSEIdleScanner) ResetHeartbeat() {
	if s.ticker != nil {
		s.ticker.Reset(s.interval)
	}
}
func (s *nativeSSEIdleScanner) Text() string { return s.line }
func (s *nativeSSEIdleScanner) Err() error   { return s.err }
func (s *nativeSSEIdleScanner) Close() {
	close(s.done)
	if s.failureTimer != nil {
		s.failureTimer.Stop()
	}
	if s.ticker != nil {
		s.ticker.Stop()
	}
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	_ = s.body.Close()
	<-s.stopped
}

func (s *nativeSSEIdleScanner) ReadAt() time.Time { return s.readAt }

// Classify only a complete SSE event. Network fragmentation and multiline data
// must never expose an identity or trigger retry before the event is complete.
type nativeSSEEventScanner struct {
	source        nativeSSEScanner
	pending       []string
	terminal      bool
	line          string
	err           error
	readAt        time.Time
	maxEventBytes int64
}

// Preserve the existing per-line payload limit and allow bounded SSE metadata.
// The recovery journal's smaller 16-MiB event cap is intentionally independent.
func newNativeSSEEventScanner(source nativeSSEScanner, maxLineBytes ...int) *nativeSSEEventScanner {
	limit := defaultMaxLineSize
	if len(maxLineBytes) > 0 && maxLineBytes[0] > 0 {
		limit = maxLineBytes[0]
	}
	return &nativeSSEEventScanner{source: source, maxEventBytes: int64(limit) + 64*1024}
}
func (s *nativeSSEEventScanner) Scan() bool {
	if len(s.pending) > 0 {
		s.line = s.pending[0]
		s.pending = s.pending[1:]
		return true
	}
	if s.err != nil || s.terminal {
		return false
	}
	var lines []string
	complete := false
	var eventBytes int64
	for s.source.Scan() {
		line := s.source.Text()
		eventBytes += int64(len(line)) + 1
		if eventBytes > s.maxEventBytes {
			s.err = bufio.ErrTooLong
			return false
		}
		lines = append(lines, line)
		if line == "" {
			complete = true
			break
		}
	}
	if err := s.source.Err(); err != nil {
		s.err = err
		return false
	}
	if len(lines) == 0 {
		return false
	}
	if !complete {
		s.err = io.ErrUnexpectedEOF
		return false
	}
	// Timestamp before JSON validation, classification, conversion or commit.
	s.readAt = time.Now()
	var data []string
	firstData := -1
	for index, line := range lines {
		if value, ok := extractOpenAISSEDataLine(line); ok {
			if firstData < 0 {
				firstData = index
			}
			data = append(data, value)
		}
	}
	if len(data) > 0 {
		joined := strings.Join(data, "\n")
		eventType := ""
		for _, line := range lines {
			if value, ok := extractOpenAISSEEventLine(line); ok {
				eventType = value
			}
		}
		kind := strings.TrimSpace(gjson.Get(joined, "type").String())
		if kind == "" {
			kind = strings.TrimSpace(eventType)
		}
		if joined == "[DONE]" || (kind != "error" && openAIStreamEventIsTerminalWithType(joined, kind)) {
			s.terminal = true
		}
		if joined != "[DONE]" && !nativeStreamEventJSONStringValid(joined) {
			s.err = errNativeStreamEventJSON
			return false
		}
		if len(data) > 1 {
			var compact bytes.Buffer
			if err := json.Compact(&compact, []byte(joined)); err != nil {
				s.err = err
				return false
			}
			output := make([]string, 0, len(lines)-len(data)+1)
			for index, line := range lines {
				if _, ok := extractOpenAISSEDataLine(line); ok {
					if index == firstData {
						output = append(output, "data: "+compact.String())
					}
					continue
				}
				output = append(output, line)
			}
			lines = output
		}
	}
	s.pending = lines[1:]
	s.line = lines[0]
	return true
}
func (s *nativeSSEEventScanner) Text() string      { return s.line }
func (s *nativeSSEEventScanner) Err() error        { return s.err }
func (s *nativeSSEEventScanner) ReadAt() time.Time { return s.readAt }
func nativeSSEReadAt(scanner nativeSSEScanner) time.Time {
	if source, ok := scanner.(interface{ ReadAt() time.Time }); ok {
		return source.ReadAt()
	}
	return time.Now()
}
