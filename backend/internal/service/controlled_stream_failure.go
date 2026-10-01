package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/tidwall/gjson"
)

var errControlledStreamFailureFrame = errors.New("invalid local stream failure frame")

// WriteControlledStreamFailure sends one locally constructed failure after an
// upstream attempt stops. It is deliberately separate from upstream delivery:
// an expired attempt must never authorize another provider frame or identity.
// The live request still controls cancellation and the bounded network write.
// A duplicate returns zero bytes; only a successful nonzero write is delivery.
func WriteControlledStreamFailure(ctx context.Context, writer http.ResponseWriter, frame []byte) (int, error) {
	if ctx == nil || writer == nil || !validControlledStreamFailureFrame(frame) {
		return 0, errControlledStreamFailureFrame
	}
	if w, ok := writer.(*openAICompactKeepaliveWriter); ok {
		// This owned wrapper only coordinates a heartbeat. Suspend it before
		// entering the scheduler's dedicated failure path; retain every
		// logging/network wrapper below the scheduler without generic unwrapping.
		w.suspend()
		return WriteControlledStreamFailure(ctx, w.ResponseWriter, frame)
	}
	r := controlledRequest(ctx)
	if w, ok := writer.(*schedulingResponseWriter); ok {
		if w.request != r {
			return 0, scheduling.ErrAttemptIdentity
		}
		w.writeMu.Lock()
		defer w.writeMu.Unlock()
		// Keep logging, metering and network deadline wrappers below the
		// scheduler. Only the obsolete attempt's commit gate is bypassed.
		return writeControlledStreamFailure(ctx, w.ResponseWriter, frame, r)
	}
	return writeControlledStreamFailure(ctx, writer, frame, r)
}

func writeControlledStreamFailure(ctx context.Context, writer http.ResponseWriter, frame []byte, r *ControlledRequest) (int, error) {
	if r == nil {
		return WriteNativeStreamFrame(ctx, writer, frame)
	}
	writeCtx, cancel, err := beginControlledStreamFailure(ctx, r)
	if err != nil || writeCtx == nil {
		return 0, err
	}
	n, err := WriteNativeStreamFrame(writeCtx, writer, frame)
	cancel()
	r.mu.Lock()
	r.localFailureCancel = nil
	r.localFailureDelivered = err == nil && n == len(frame)
	if n > 0 {
		r.httpCommitted = true
	}
	r.mu.Unlock()
	return n, err
}

func beginControlledStreamFailure(ctx context.Context, r *ControlledRequest) (context.Context, context.CancelFunc, error) {
	r.mu.Lock()
	d := r.currentDispatch
	r.mu.Unlock()
	if d != nil {
		d.mu.Lock()
		defer d.mu.Unlock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentDispatch != d {
		return nil, nil, scheduling.ErrAttemptIdentity
	}
	if r.localFailureStarted {
		return nil, nil, nil
	}
	if r.cancelReason.excludesProviderHealth() || (d != nil && (d.adminCancelled.Load() || d.cancellationReason.excludesProviderHealth())) {
		return nil, nil, context.Canceled
	}
	parent := r.clientContext
	if parent == nil {
		parent = ctx
	}
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	// A canceled dispatch is usable only for its own confirmed timeout.
	if err := ctx.Err(); err != nil && (d == nil || ctx.Value(controlledDispatchContextKey{}) != d || !d.timeout) {
		return nil, nil, err
	}
	writeCtx, cancel := context.WithCancel(parent)
	writeCtx = context.WithValue(WithNativeStreamPolicy(writeCtx, NativeStreamPolicyFromContext(ctx)), controlledSchedulingContextKey{}, r)
	if NativeStreamFlushDeferred(ctx) {
		writeCtx = WithNativeStreamDeferredFlush(writeCtx)
	}
	r.localFailureStarted = true
	r.localFailureCancel = cancel
	if r.Ledger != nil {
		// Seal the logical request without claiming provider identity/content.
		r.Ledger.MarkAttemptCommit()
	}
	return writeCtx, cancel, nil
}

// ControlledStreamFailureWritten reports successful Write and Flush, never an
// attempted or partial terminal. Handlers use it to avoid a second error event.
func ControlledStreamFailureWritten(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	r := controlledRequest(ctx)
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.localFailureDelivered
}

func validControlledStreamFailureFrame(frame []byte) bool {
	if len(frame) == 0 || len(frame) > 64<<10 {
		return false
	}
	frame = bytes.ReplaceAll(frame, []byte("\r\n"), []byte("\n"))
	if !bytes.HasSuffix(frame, []byte("\n\n")) {
		return false
	}
	body := bytes.TrimSuffix(frame, []byte("\n\n"))
	if bytes.Contains(body, []byte("\n\n")) {
		return false
	}
	var data []byte
	var event string
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			if event != "" {
				return false
			}
			event = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
		case bytes.HasPrefix(line, []byte("data:")):
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, bytes.TrimPrefix(line, []byte("data:"))...)
		default:
			return false
		}
	}
	if !nativeStreamEventJSONValid(data) {
		return false
	}
	v := gjson.ParseBytes(data)
	switch v.Get("type").String() {
	case "error":
		return (event == "" || event == "error") && (v.Get("message").Type == gjson.String || v.Get("error.message").Type == gjson.String)
	case "response.failed":
		return event == "response.failed" && v.Get("response.status").String() == "failed" && v.Get("response.error").IsObject()
	case "":
		return (event == "" || event == "error") && v.Get("error").IsObject() && v.Get("error.message").Type == gjson.String
	default:
		return false
	}
}
