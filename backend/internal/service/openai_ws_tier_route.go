package service

import (
	"context"
	"sync"

	coderws "github.com/coder/websocket"
	openaiwsv2 "github.com/liulixin-lex/xy2api/internal/service/openai_ws_v2"
)

// The passthrough relay keeps a reader outstanding between turns. Publish the
// replacement before closing the retired socket so its interrupted reader
// resumes on the new route without terminating the downstream session.
type openAIWSTierRoute struct {
	mu         sync.Mutex
	conn       openaiwsv2.FrameConn
	generation uint64
	closed     bool
}

func (r *openAIWSTierRoute) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	for {
		r.mu.Lock()
		c, generation, closed := r.conn, r.generation, r.closed
		r.mu.Unlock()
		if closed {
			return 0, nil, errOpenAIWSConnClosed
		}
		t, p, err := c.ReadFrame(ctx)
		r.mu.Lock()
		current := r.generation == generation
		r.mu.Unlock()
		if current || ctx.Err() != nil {
			return t, p, err
		}
	}
}

func (r *openAIWSTierRoute) WriteFrame(ctx context.Context, t coderws.MessageType, p []byte) error {
	r.mu.Lock()
	c, closed := r.conn, r.closed
	r.mu.Unlock()
	if closed {
		return errOpenAIWSConnClosed
	}
	return c.WriteFrame(ctx, t, p)
}

func (r *openAIWSTierRoute) replace(next openaiwsv2.FrameConn) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = next.Close()
		return errOpenAIWSConnClosed
	}
	previous := r.conn
	r.conn = next
	r.generation++
	r.mu.Unlock()
	_ = previous.Close()
	return nil
}

func (r *openAIWSTierRoute) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	c := r.conn
	r.mu.Unlock()
	return c.Close()
}
