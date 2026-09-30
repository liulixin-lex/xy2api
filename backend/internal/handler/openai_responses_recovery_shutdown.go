package handler

import "context"

// ShutdownNativeResponses must run before usage workers and upstream transport
// teardown. Admission and WaitGroup.Add share the same lock, so every execution
// admitted before shutdown is cancelled and included in the bounded wait.
func (h *OpenAIGatewayHandler) ShutdownNativeResponses(ctx context.Context) error {
	h.nativeResponseLifecycleMu.Lock()
	done := h.nativeResponseShutdownDone
	if done == nil {
		h.nativeResponseClosing = true
		done = make(chan struct{})
		h.nativeResponseShutdownDone = done
		manager := h.NativeResponseManager()
		_ = manager.Close()
		go func() { h.nativeResponseWorkers.Wait(); close(done) }()
	}
	h.nativeResponseLifecycleMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
