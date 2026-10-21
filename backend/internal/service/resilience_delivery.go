package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Client delivery tracking (design §8).

const deliveryWriterKey = "xy_resilience_delivery_writer"

// EdgeUpstreamStatusHeader carries the status a CDN-bound 502/504 had before
// it was answered as 503.
const EdgeUpstreamStatusHeader = "X-XY-Upstream-Status"

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

	// state of the request, for the edge marker (WriteHeader).
	state *resilience.RequestState

	// stream tells the opening events of an SSE response from its content.
	stream sseDeliveryState
}

// WriteHeader answers 503 instead of 502/504 to a request that came through
// a CDN edge. Cloudflare replaces an origin 502 or 504 with its own plain
// "error code: 502" page, which drops the JSON error, Retry-After and the
// request id (staging CF-3); it passes a 503 through unchanged. Direct
// clients keep the original status. X-XY-Upstream-Status tells which it was.
func (w *deliveryWriter) WriteHeader(code int) {
	if (code == http.StatusBadGateway || code == http.StatusGatewayTimeout) && !w.Written() &&
		w.state != nil && w.state.Edge() != "" && resilience.Current().ErrorsOn() {
		w.Header().Set(EdgeUpstreamStatusHeader, strconv.Itoa(code))
		code = http.StatusServiceUnavailable
	}
	w.ResponseWriter.WriteHeader(code)
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
	if n > 0 {
		w.observeStream(data[:n])
		w.noteFirstOutput(len(bytes.TrimSpace(data[:n])) > 0)
	}
	return n, err
}

func (w *deliveryWriter) WriteString(s string) (int, error) {
	n, err := w.ResponseWriter.WriteString(s)
	w.written.Add(int64(n))
	if n > 0 && w.stream.observing() {
		w.observeStream([]byte(s[:n]))
	}
	if n > 0 {
		w.noteFirstOutput(strings.TrimSpace(s[:n]) != "")
	}
	return n, err
}

func (w *deliveryWriter) noteFirstOutput(nonWhitespace bool) {
	// Do not invoke the delivery probe here: this write can be underneath
	// a keepalive mutex, and its probe can reenter that mutex.
	// JSON keepalive writes whitespace. For SSE, a whitespace-only write
	// can complete a content block, so use the assembled block's state.
	if (w.stream.kind.Load() != sseKindEventStream && nonWhitespace) || w.stream.content.Load() {
		w.state.NoteFirstOutput(time.Now())
	}
}

// deliveredBytes is the delivery probe: the semantic bytes, except that an
// SSE response delivered nothing until a content event was written (review
// R03). Converting and native streams write opening events (message_start,
// a Chat role chunk, response.created) and pings as soon as the upstream
// answers; a client that leaves before any content got nothing.
func (w *deliveryWriter) deliveredBytes() int64 {
	n := w.semanticBytes()
	if w.stream.kind.Load() != sseKindEventStream {
		return n
	}
	if !w.stream.content.Load() {
		return 0
	}
	if n > 0 {
		return n
	}
	return 1
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
	w := &deliveryWriter{ResponseWriter: c.Writer, state: st}
	c.Writer = w
	c.Set(deliveryWriterKey, w)
	st.InitDelivery(c.Request.Context(), started, w.deliveredBytes)
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

// NoteDeliveryHeartbeat registers bytes a handler wrote as heartbeats, such
// as the pings of a slot or user message queue wait, so they do not count as
// delivered output. It reports false when the request has no delivery
// tracking.
func NoteDeliveryHeartbeat(c *gin.Context, bytes int) bool {
	w := deliveryWriterFrom(c)
	if w == nil {
		return false
	}
	if bytes > 0 {
		w.heartbeat.Add(int64(bytes))
	}
	return true
}

// DeliveryOnlyHeartbeats reports whether everything written to the client
// so far is heartbeats. tracked is false when the request has no delivery
// tracking.
func DeliveryOnlyHeartbeats(c *gin.Context) (only, tracked bool) {
	w := deliveryWriterFrom(c)
	if w == nil {
		return false, false
	}
	return w.written.Load() > 0 && w.semanticBytes() == 0, true
}

// ResponsesStreamUnopened reports whether a Responses terminal event the
// gateway writes now needs response.created before it: resilience is on,
// the request is tracked and nothing but heartbeats (if anything) reached
// the client. Switched off, the terminal event goes out alone as in 0.3.0.
func ResponsesStreamUnopened(c *gin.Context) bool {
	if !resilience.Current().Enabled {
		return false
	}
	w := deliveryWriterFrom(c)
	return w != nil && w.semanticBytes() == 0
}

// resilienceUpstreamContext applies the disconnect rules to a context that
// was detached from the client.
func resilienceUpstreamContext(client, detached context.Context) context.Context {
	return resilience.UpstreamContextOnDisconnect(client, detached)
}
