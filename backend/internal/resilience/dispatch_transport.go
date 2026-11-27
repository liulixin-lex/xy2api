package resilience

import (
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"
)

// ObserveDispatchTransport belongs immediately above the physical transport,
// below wrappers that may call RoundTrip more than once. It does not modify the
// cached transport or any retry/redirect policy. Each RoundTrip owns its trace;
// a request-wide counted flag would lose retries.
func ObserveDispatchTransport(base http.RoundTripper, path string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &dispatchTransport{base: base, path: path}
}

type dispatchTransport struct {
	base http.RoundTripper
	path string
}

func (t *dispatchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	state := StateFrom(req.Context())
	if state == nil {
		return t.base.RoundTrip(req)
	}
	var mu sync.Mutex
	connections, writes, counted := 0, 0, 0
	// net/http validates headers, method and cancellation before GetConn.
	// Its trace is the actual send boundary; invalid requests never count.
	// Other RoundTrippers expose their physical entry directly (including
	// fake transports and transports without net/http trace support).
	if _, traced := t.base.(*http.Transport); !traced {
		state.NoteDispatch(time.Now(), t.path)
		counted = 1
	}
	observe := func(connection bool) {
		mu.Lock()
		defer mu.Unlock()
		if connection {
			connections++
		} else {
			writes++
		}
		// GetConn sees retries even when the next dial fails. WroteHeaders
		// covers transports that retry writes without acquiring another
		// connection. Both signals describe the same send, not two sends.
		for next := max(connections, writes); counted < next; counted++ {
			state.NoteDispatch(time.Now(), t.path)
		}
	}
	ctx := httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		GetConn:      func(string) { observe(true) },
		WroteHeaders: func() { observe(false) },
	})
	return t.base.RoundTrip(req.WithContext(ctx))
}
