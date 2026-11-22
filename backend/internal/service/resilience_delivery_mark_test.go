//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The probe may be anywhere in its reading when heartbeats are folded into
// the count: it must never take a heartbeat for output.
func TestDeliveryHeartbeatMarkIsConsistentForProbes(t *testing.T) {
	w := &deliveryWriter{}
	c := &gin.Context{}
	c.Set(deliveryWriterKey, w)
	w.written.Store(100)
	first := &openAICompactSSEKeepalive{}
	w.sse.Store(first)
	w.images.Store(&openAIImagesJSONKeepalive{bytes: 80})

	// Holding the first source's lock parks the probe after it read the count.
	first.mu.Lock()
	got := make(chan int64, 1)
	go func() { got <- w.semanticBytes() }()
	time.Sleep(50 * time.Millisecond)
	w.sse.Store(nil)
	MarkDeliveryHeartbeats(c)
	first.mu.Unlock()

	require.Equal(t, int64(20), <-got)
	require.Equal(t, int64(20), w.semanticBytes())
}
