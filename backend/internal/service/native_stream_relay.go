package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

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
	ctx       context.Context
	body      io.ReadCloser
	events    <-chan nativeSSEScanEvent
	done      chan struct{}
	stopped   chan struct{}
	ticker    *time.Ticker
	heartbeat func() error
	line      string
	readAt    time.Time
	err       error
}

func newNativeSSEIdleScanner(ctx context.Context, body io.ReadCloser, scanner nativeSSEScanner, interval time.Duration, heartbeat func() error) *nativeSSEIdleScanner {
	events := make(chan nativeSSEScanEvent, 1)
	s := &nativeSSEIdleScanner{ctx: ctx, body: body, events: events, done: make(chan struct{}), stopped: make(chan struct{}), heartbeat: heartbeat}
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
	for {
		select {
		case <-s.ctx.Done():
			s.err = s.ctx.Err()
			_ = s.body.Close()
			return false
		case <-tick:
			if err := s.heartbeat(); err != nil {
				s.err = fmt.Errorf("client_detached: %w: %v", context.Canceled, err)
				_ = s.body.Close()
				return false
			}
		case event, ok := <-s.events:
			if s.ctx.Err() != nil {
				s.err = s.ctx.Err()
				return false
			}
			if !ok {
				return false
			}
			s.line, s.err, s.readAt = event.line, event.err, event.readAt
			return event.err == nil
		}
	}
}
func (s *nativeSSEIdleScanner) Text() string { return s.line }
func (s *nativeSSEIdleScanner) Err() error   { return s.err }
func (s *nativeSSEIdleScanner) Close() {
	close(s.done)
	if s.ticker != nil {
		s.ticker.Stop()
	}
	_ = s.body.Close()
	<-s.stopped
}

func (s *nativeSSEIdleScanner) ReadAt() time.Time { return s.readAt }

// Classify only a complete SSE event. Network fragmentation and multiline data
// must never expose an identity or trigger retry before the event is complete.
type nativeSSEEventScanner struct {
	source   nativeSSEScanner
	pending  []string
	terminal bool
	line     string
	err      error
	readAt   time.Time
}

func newNativeSSEEventScanner(source nativeSSEScanner) *nativeSSEEventScanner {
	return &nativeSSEEventScanner{source: source}
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
	for s.source.Scan() {
		line := s.source.Text()
		lines = append(lines, line)
		if line == "" {
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
		if joined == "[DONE]" || openAIStreamEventIsTerminalWithType(joined, effectiveOpenAISSEEventType([]byte(joined), eventType)) {
			s.terminal = true
		}
		if joined != "[DONE]" && !json.Valid([]byte(joined)) {
			s.err = errors.New("invalid complete upstream SSE event JSON")
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
