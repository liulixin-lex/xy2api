package service

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Client delivery tracking (design §8).

const deliveryWriterKey = "xy_resilience_delivery_writer"

// deliveryWriter counts every byte written to the client. It sits directly
// on the server's writer, below any keepalive wrapper, so the count is safe
// to read from the disconnect callback.
type deliveryWriter struct {
	gin.ResponseWriter
	written atomic.Int64

	// Heartbeat sources registered by the request goroutine, so the probe
	// never touches the (pooled) gin context.
	heartbeat atomic.Int64
	sse       atomic.Pointer[openAICompactSSEKeepalive]
	images    atomic.Pointer[openAIImagesJSONKeepalive]
}

func deliveryWriterFrom(c *gin.Context) *deliveryWriter {
	if c == nil {
		return nil
	}
	v, ok := c.Get(deliveryWriterKey)
	if !ok {
		return nil
	}
	w, _ := v.(*deliveryWriter)
	return w
}

// semanticBytes returns the bytes written to the client minus heartbeats
// and keepalive padding: the delivered semantic output.
func (w *deliveryWriter) semanticBytes() int64 {
	total := w.written.Load()
	if total <= 0 {
		return 0
	}
	heartbeat := w.heartbeat.Load()
	if k := w.sse.Load(); k != nil {
		k.mu.Lock()
		heartbeat += int64(k.bytes)
		k.mu.Unlock()
	}
	if k := w.images.Load(); k != nil {
		k.mu.Lock()
		heartbeat += int64(k.bytes)
		k.mu.Unlock()
	}
	if total <= heartbeat {
		return 0
	}
	return total - heartbeat
}

func (w *deliveryWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.written.Add(int64(n))
	return n, err
}

func (w *deliveryWriter) WriteString(s string) (int, error) {
	n, err := w.ResponseWriter.WriteString(s)
	w.written.Add(int64(n))
	return n, err
}

func (w *deliveryWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.ResponseWriter == nil {
		return nil, nil, errors.New("response writer released")
	}
	return w.ResponseWriter.Hijack()
}

func (w *deliveryWriter) Flush() { w.ResponseWriter.Flush() }

// Unwrap lets http.ResponseController reach the server's writer.
func (w *deliveryWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *deliveryWriter) CloseNotify() <-chan bool { return w.ResponseWriter.CloseNotify() }

func (w *deliveryWriter) Pusher() http.Pusher { return w.ResponseWriter.Pusher() }

// InstallDeliveryTracking wraps the response writer with the delivery
// counter and gives the request state its probe. It must run before any
// other writer wrapper of the request.
func InstallDeliveryTracking(c *gin.Context, started time.Time) {
	if c == nil || c.Writer == nil || c.Request == nil {
		return
	}
	st := resilience.StateFrom(c.Request.Context())
	if st == nil {
		return
	}
	w := &deliveryWriter{ResponseWriter: c.Writer}
	c.Writer = w
	c.Set(deliveryWriterKey, w)
	st.InitDelivery(c.Request.Context(), started, w.semanticBytes)
}

// noteDeliveryHeartbeat registers heartbeat sources of the request.
func noteDeliveryHeartbeat(c *gin.Context, sse *openAICompactSSEKeepalive, images *openAIImagesJSONKeepalive, bytes int) {
	w := deliveryWriterFrom(c)
	if w == nil {
		return
	}
	if sse != nil {
		w.sse.Store(sse)
	}
	if images != nil {
		w.images.Store(images)
	}
	if bytes > 0 {
		w.heartbeat.Add(int64(bytes))
	}
}

// resilienceUpstreamContext applies the disconnect rules to a context that
// was detached from the client.
func resilienceUpstreamContext(client, detached context.Context) context.Context {
	return resilience.UpstreamContextOnDisconnect(client, detached)
}
