package service

import (
	"context"
	"io"
	"sync"
)

// Native bodies own the transport cancellation used by early downstream exits.
// Close first aborts the HTTP request and joins any Read, then closes the body.
// Calling net/http Body.Close while another goroutine is still reading can race
// its post-close drain and leave the second EOF acknowledgement waiting on an
// idle persistent connection. Cancellation interrupts the actual socket read;
// it is not a timeout around an abandoned goroutine.
type nativeStreamResponseBody struct {
	io.ReadCloser
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closing   bool
	reads     sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

func (b *nativeStreamResponseBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		if err := b.ctx.Err(); err != nil {
			return 0, err
		}
		return 0, io.ErrClosedPipe
	}
	b.reads.Add(1)
	b.mu.Unlock()
	defer b.reads.Done()
	return b.ReadCloser.Read(p)
}

func (b *nativeStreamResponseBody) Close() error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closing = true
		b.mu.Unlock()
		b.cancel()
		b.reads.Wait()
		b.closeErr = b.ReadCloser.Close()
	})
	return b.closeErr
}
