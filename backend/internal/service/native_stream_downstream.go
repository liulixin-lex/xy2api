package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const nativeStreamWriteTimeout = 30 * time.Second

// Only a scheduler gate can attach this marker. A real network error can carry
// the same context sentinel, so error identity alone cannot classify the source.
type controlledStreamWriteRejection struct{ cause error }

func (e *controlledStreamWriteRejection) Error() string { return e.cause.Error() }
func (e *controlledStreamWriteRejection) Unwrap() error { return e.cause }

func rejectControlledStreamWrite(err error) error {
	return &controlledStreamWriteRejection{cause: err}
}

// WriteNativeStreamFrame gives one network Write+Flush a bounded lifetime. The
// caller remains the sole writer; cancellation only expires its socket deadline.
// Journal/memory writers may not expose network deadlines and remain synchronous.
func WriteNativeStreamFrame(ctx context.Context, writer http.ResponseWriter, frame []byte) (int, error) {
	return writeNativeStreamFrame(ctx, writer, frame, nativeStreamWriteTimeout)
}

func writeNativeStreamFrame(ctx context.Context, writer http.ResponseWriter, frame []byte, timeout time.Duration) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	controller := http.NewResponseController(writer)
	deadline := time.Now().Add(timeout)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	deadlineErr := controller.SetWriteDeadline(deadline)
	if deadlineErr != nil {
		if !errors.Is(deadlineErr, http.ErrNotSupported) || (!NativeStreamFlushDeferred(ctx) && !nativeStreamMemoryWriter(writer)) {
			return 0, nativeStreamDownstreamError(ctx, deadlineErr)
		}
	}
	if deadlineErr == nil {
		interrupted := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { defer close(interrupted); _ = controller.SetWriteDeadline(time.Now()) })
		defer func() {
			if !stop() {
				<-interrupted
			}
			_ = controller.SetWriteDeadline(time.Time{})
		}()
	}
	n, err := writer.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = flushNativeStreamWriter(writer)
	}
	if err != nil {
		return n, nativeStreamDownstreamError(ctx, err)
	}
	return n, nil
}

// Gin's Flush discards the underlying FlushError. Walk its transparent wrappers
// after staging their headers, so a failed network flush is never reported as a
// delivered event or counted in successful downstream latency measurements.
func flushNativeStreamWriter(writer http.ResponseWriter) error {
	if staged, ok := writer.(interface{ WriteHeaderNow() }); ok {
		staged.WriteHeaderNow()
	}
	for {
		if flusher, ok := writer.(interface{ FlushError() error }); ok {
			return flusher.FlushError()
		}
		if wrapper, ok := writer.(interface{ Unwrap() http.ResponseWriter }); ok {
			writer = wrapper.Unwrap()
			continue
		}
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
			return nil
		}
		return http.ErrNotSupported
	}
}

func nativeStreamDownstreamError(ctx context.Context, err error) error {
	var rejection *controlledStreamWriteRejection
	if errors.As(err, &rejection) {
		return err
	}
	reason := ControlledClientDetached
	explicitReason := controlledContextCancelReason(ctx)
	var networkError net.Error
	if ctx.Err() == nil && errors.As(err, &networkError) && networkError.Timeout() {
		reason = ControlledSlowConsumer
	}
	if explicitReason != "" {
		reason = explicitReason
	}
	_ = CancelControlledRequest(ctx, reason)
	return fmt.Errorf("%s: %w: %v", reason, context.Canceled, err)
}

// Only a synchronous in-memory sink may opt out of network deadlines. Opaque
// network writers fail before Write instead of leaving an uninterruptible call.
func nativeStreamMemoryWriter(writer http.ResponseWriter) bool {
	for depth := 0; depth < 32 && writer != nil; depth++ {
		if memory, ok := writer.(interface{ NativeStreamMemoryWriter() bool }); ok && memory.NativeStreamMemoryWriter() {
			return true
		}
		wrapper, ok := writer.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		writer = wrapper.Unwrap()
	}
	return false
}
