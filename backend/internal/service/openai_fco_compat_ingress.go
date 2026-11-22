package service

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// WithOpenAIFCOCompatIngressSnapshot binds ctx to the current fco_compat
// settings and to rawBody. rawBody must be the body exactly as the client sent
// it, before lenient JSON normalization or any decode and re-encode step, so
// the duplicate member flag still sees every member. Entry points call it
// before content moderation; the audit and every outbound attempt of the
// request then read the same snapshot.
func WithOpenAIFCOCompatIngressSnapshot(ctx context.Context, rawBody []byte) context.Context {
	return WithOpenAIFCOCompatSnapshot(ctx, NewOpenAIFCOCompatSnapshot(resilience.Current(), rawBody))
}

// A Responses WebSocket keeps one gin context for the whole connection, while
// every response.create frame is a turn with its own snapshot. The ingress
// forwarder records the snapshot of the turn it has just read under this key,
// so the handler's per-turn audit reads the snapshot that turn is sent with.
const openAIFCOCompatWSTurnKey = "openai_fco_compat.ws_turn_snapshot"

type openAIFCOCompatWSTurn struct {
	turn     int
	snapshot OpenAIFCOCompatSnapshot
}

func setOpenAIFCOCompatWSTurnSnapshot(c *gin.Context, turn int, snapshot OpenAIFCOCompatSnapshot) {
	if c == nil {
		return
	}
	c.Set(openAIFCOCompatWSTurnKey, openAIFCOCompatWSTurn{turn: turn, snapshot: snapshot})
}

// OpenAIFCOCompatWSTurnSnapshot returns the snapshot of the WebSocket turn the
// ingress forwarder read last, and that turn's number as the forwarder counts
// it, which is the number its hooks receive.
func OpenAIFCOCompatWSTurnSnapshot(c *gin.Context) (OpenAIFCOCompatSnapshot, int, bool) {
	if c == nil {
		return OpenAIFCOCompatSnapshot{}, 0, false
	}
	value, exists := c.Get(openAIFCOCompatWSTurnKey)
	if !exists {
		return OpenAIFCOCompatSnapshot{}, 0, false
	}
	entry, ok := value.(openAIFCOCompatWSTurn)
	if !ok {
		return OpenAIFCOCompatSnapshot{}, 0, false
	}
	return entry.snapshot, entry.turn, true
}
