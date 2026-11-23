package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

const collabDeliveryBufferLimit = 1 << 20

type openAICollabDeliveryFrame struct {
	event   resilience.CollabDeliveryEvent
	payload []byte
}

// This observer is below the seam and keepalives. It never buffers or changes
// delivery itself; pending holds only bounded observation copies. A stream
// frame is confirmed only when the caller's subsequent flush succeeds.
type openAICollabDeliveryWriter struct {
	gin.ResponseWriter
	mu          sync.Mutex
	ctx         context.Context
	hooks       *resilience.CollabDeliveryHooks
	current     resilience.CollabDeliveryEvent
	generations []resilience.CollabDeliveryEvent
	partial     []byte
	pending     []openAICollabDeliveryFrame
	pendingSize int
	sequence    int64
	local       bool
	failed      error
	completed   bool
	oversized   bool
}

func (w *openAICollabDeliveryWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *openAICollabDeliveryWriter) begin(local bool) resilience.CollabDeliveryEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.current.Generation != 0 {
		w.saveCurrent()
	}
	generation := resilience.CollabGeneration(len(w.generations) + 1)
	send := resilience.CollabSendSequence(0)
	if len(w.generations) > 0 {
		send = w.generations[len(w.generations)-1].SendSeq
	}
	if !local {
		send++
	}
	w.current = resilience.CollabDeliveryEvent{Generation: generation, SendSeq: send}
	w.generations = append(w.generations, w.current)
	w.local, w.sequence, w.failed, w.oversized = local, 0, nil, false
	w.partial, w.pending, w.pendingSize = nil, nil, 0
	return w.current
}

func (w *openAICollabDeliveryWriter) saveCurrent() {
	event := w.current
	event.WriteError = w.failed
	if event.WriteError == nil {
		event.WriteError = w.ctx.Err()
	}
	event.Successful = event.WriteError == nil && len(w.pending) == 0
	w.generations[int(event.Generation)-1] = event
}

// CollabDeliveryMark preserves the generation of a withheld terminal while
// optional physical responses use later generations on the same connection.
type CollabDeliveryMark struct {
	writer *openAICollabDeliveryWriter
	event  resilience.CollabDeliveryEvent
	local  bool
	failed error
}

func MarkOpenAICollabDelivery(ctx context.Context) CollabDeliveryMark {
	if request := openAICollabRequestFrom(ctx); request != nil {
		w := request.delivery
		w.mu.Lock()
		defer w.mu.Unlock()
		return CollabDeliveryMark{writer: w, event: w.current, local: w.local, failed: w.failed}
	}
	return CollabDeliveryMark{}
}

func (mark CollabDeliveryMark) Restore() {
	if w := mark.writer; w != nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.current.Generation != 0 {
			w.saveCurrent()
		}
		w.current, w.local, w.failed = mark.event, mark.local, mark.failed
		w.partial, w.pending, w.pendingSize, w.oversized = nil, nil, 0, false
	}
}

func beginOpenAICollabResponse(ctx context.Context) {
	if request := openAICollabRequestFrom(ctx); request != nil {
		event := request.delivery.begin(false)
		request.state.Response = resilience.CollabResponseState{Generation: event.Generation}
		request.state.Sent++
	}
}

// BeginOpenAICollabLocalResponse seals the upstream origin before local output.
// Local frames retain delivery tracking but never authorize executable records.
func BeginOpenAICollabLocalResponse(ctx context.Context) {
	if request := openAICollabRequestFrom(ctx); request != nil {
		event := request.delivery.begin(true)
		request.state.Response = resilience.CollabResponseState{Generation: event.Generation}
	}
}

// FinishOpenAICollabDelivery runs after the seam has released or replaced its
// terminal. Deferring completion until then includes withheld error replay and
// ensures no generation can notify consumers after its Completed callback.
func FinishOpenAICollabDelivery(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	request := openAICollabRequestFrom(c.Request.Context())
	if request == nil || request.delivery == nil {
		return
	}
	w := request.delivery
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.completed {
		return
	}
	w.completed = true
	if w.current.Generation != 0 {
		w.saveCurrent()
	}
	for _, event := range w.generations {
		w.hooks.Completed(w.ctx, event, nil)
	}
	w.partial, w.pending, w.generations = nil, nil, nil
}

func (w *openAICollabDeliveryWriter) Write(p []byte) (int, error) {
	return w.write(p, false)
}

func (w *openAICollabDeliveryWriter) WriteString(value string) (int, error) {
	return w.write([]byte(value), true)
}

func (w *openAICollabDeliveryWriter) write(p []byte, asString bool) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.current.Generation == 0 {
		w.current.Generation, w.local = 1, true
		w.generations = append(w.generations, w.current)
	}
	stream := strings.Contains(w.Header().Get("Content-Type"), "text/event-stream")
	frames := w.frames(p, stream)
	for _, frame := range frames {
		w.hooks.Received(w.ctx, frame.event, frame.payload)
		w.hooks.AboutToWrite(w.ctx, frame.event, frame.payload)
	}
	var n int
	var err error
	if asString {
		n, err = w.ResponseWriter.WriteString(string(p))
	} else {
		n, err = w.ResponseWriter.Write(p)
	}
	if err != nil || n != len(p) {
		w.failed = err
		if w.failed == nil {
			w.failed = io.ErrShortWrite
		}
		return n, err
	}
	if w.failed != nil || w.ctx.Err() != nil {
		return n, err
	}
	for _, frame := range frames {
		if stream {
			if w.pendingSize+len(frame.payload) <= collabDeliveryBufferLimit {
				w.pending = append(w.pending, frame)
				w.pendingSize += len(frame.payload)
			}
		} else {
			w.written(frame)
		}
	}
	return n, err
}

func (w *openAICollabDeliveryWriter) written(frame openAICollabDeliveryFrame) {
	frame.event.Successful = true
	frame.event.BytesWritten = len(frame.payload)
	w.hooks.Written(w.ctx, frame.event, frame.payload)
}

func (w *openAICollabDeliveryWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Preserve the existing flush, including wrapper bookkeeping. Some
	// transports expose failure only by canceling the request context.
	if flush, ok := w.ResponseWriter.(interface{ FlushError() error }); ok {
		if err := flush.FlushError(); err != nil {
			w.failed = err
		}
	} else {
		w.ResponseWriter.Flush()
	}
	if w.failed == nil && w.ctx.Err() == nil {
		for _, frame := range w.pending {
			w.written(frame)
		}
	}
	w.pending, w.pendingSize = nil, 0
}

func (w *openAICollabDeliveryWriter) frames(p []byte, stream bool) []openAICollabDeliveryFrame {
	if w.completed || w.oversized {
		return nil
	}
	if len(w.partial)+len(p) > collabDeliveryBufferLimit {
		w.partial, w.oversized = nil, true
		return nil
	}
	w.partial = append(w.partial, p...)
	var frames []openAICollabDeliveryFrame
	if !stream {
		if gjson.ValidBytes(w.partial) {
			frames = append(frames, w.frame("response.json", bytes.Clone(w.partial)))
			w.partial = nil
		}
		return frames
	}
	for {
		end, sep := sseBlockEnd(w.partial)
		if end < 0 {
			break
		}
		event, payload := sseBlockFields(w.partial[:end])
		if event == "" {
			event = gjson.GetBytes(payload, "type").String()
		}
		frames = append(frames, w.frame(event, payload))
		w.partial = w.partial[end+sep:]
	}
	return frames
}

func (w *openAICollabDeliveryWriter) frame(kind string, payload []byte) openAICollabDeliveryFrame {
	w.sequence++
	event := w.current
	event.Sequence, event.EventType = w.sequence, kind
	switch kind {
	case "response.output_item.done":
		item := gjson.GetBytes(payload, "item")
		event.Executable = !w.local && item.Get("type").String() == "function_call" && item.Get("namespace").String() == codexCollaborationNamespace
	case "response.completed", "response.done", "response.json":
		event.Executable = !w.local
	}
	return openAICollabDeliveryFrame{event: event, payload: payload}
}
