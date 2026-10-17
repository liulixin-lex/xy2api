// Command cdn-sim is a reverse proxy that behaves like a CDN edge for
// gateway resilience tests (staging-validation §2, tasks 0.6):
//
//   - FIRST_BYTE_TIMEOUT: no response headers from the origin within this
//     time ends the request with 524, like a CDN origin timeout;
//   - IDLE_TIMEOUT: once streaming, no bytes from the origin for this long
//     drops the client connection and the origin request (the "cut");
//   - EDGE: the X-XY-Edge marker added to every origin request (a client
//     supplied value is always removed first).
//
// Environment: LISTEN (default :8080), TARGET (origin URL, required),
// FIRST_BYTE_TIMEOUT and IDLE_TIMEOUT (Go durations, default 125s), EDGE
// (default "cf"; "none" forwards without a marker, for direct-path request
// counting). GET /_cuts lists the requests that were cut; GET
// /_requests?since=N lists every proxied request.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"
)

type cut struct {
	At       time.Time `json:"at"`
	Path     string    `json:"path"`
	Kind     string    `json:"kind"` // first_byte or idle
	Elapsed  float64   `json:"elapsed_s"`
	Received int64     `json:"received_bytes"`
}

type cutLog struct {
	mu   sync.Mutex
	cuts []cut
}

func (l *cutLog) add(c cut) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cuts = append(l.cuts, c)
	if len(l.cuts) > 5000 {
		l.cuts = append([]cut(nil), l.cuts[len(l.cuts)-2500:]...)
	}
}

func (l *cutLog) list() []cut {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]cut(nil), l.cuts...)
}

var cuts = &cutLog{}

type proxied struct {
	ID       int64     `json:"id"`
	Path     string    `json:"path"`
	Status   int       `json:"status"`
	Started  time.Time `json:"started"`
	Duration float64   `json:"duration_s"`
}

type requestLog struct {
	mu   sync.Mutex
	next int64
	rows []proxied
}

func (l *requestLog) add(p proxied) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	p.ID = l.next
	l.rows = append(l.rows, p)
	if len(l.rows) > 20000 {
		l.rows = append([]proxied(nil), l.rows[len(l.rows)-10000:]...)
	}
}

func (l *requestLog) since(id int64) []proxied {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []proxied{}
	for _, p := range l.rows {
		if p.ID > id {
			out = append(out, p)
		}
	}
	return out
}

var requests = &requestLog{}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type ctxKey struct{}

type requestInfo struct {
	started  time.Time
	path     string
	received int64
}

// idleBody closes the origin body when no bytes arrive for idle.
type idleBody struct {
	io.ReadCloser
	idle  time.Duration
	timer *time.Timer
	info  *requestInfo
	mu    sync.Mutex
	fired bool
}

func newIdleBody(body io.ReadCloser, idle time.Duration, info *requestInfo, cancel context.CancelFunc) *idleBody {
	b := &idleBody{ReadCloser: body, idle: idle, info: info}
	b.timer = time.AfterFunc(idle, func() {
		b.mu.Lock()
		b.fired = true
		b.mu.Unlock()
		cuts.add(cut{At: time.Now(), Path: info.path, Kind: "idle", Elapsed: time.Since(info.started).Seconds(), Received: info.received})
		cancel()
		_ = body.Close()
	})
	return b
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.info.received += int64(n)
		b.timer.Reset(b.idle)
	}
	b.mu.Lock()
	fired := b.fired
	b.mu.Unlock()
	if fired {
		// Drop the client connection like a CDN that gave up on the origin.
		return n, http.ErrAbortHandler
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	return b.ReadCloser.Close()
}

func envDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		return d
	}
	return def
}

func main() {
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = ":8080"
	}
	target, err := url.Parse(os.Getenv("TARGET"))
	if err != nil || target.Host == "" {
		log.Fatal("TARGET must be the origin URL")
	}
	firstByte := envDuration("FIRST_BYTE_TIMEOUT", 125*time.Second)
	idle := envDuration("IDLE_TIMEOUT", 125*time.Second)
	edge := os.Getenv("EDGE")
	if edge == "" {
		edge = "cf"
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = firstByte
	transport.DisableCompression = true

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	proxy.FlushInterval = -1
	baseDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		baseDirector(r)
		r.Header.Del("X-XY-Edge")
		if edge != "none" {
			r.Header.Set("X-XY-Edge", edge)
		}
		r.Host = target.Host
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		info, _ := resp.Request.Context().Value(ctxKey{}).(*requestInfo)
		cancel, _ := resp.Request.Context().Value(cancelKey{}).(context.CancelFunc)
		if info != nil && cancel != nil {
			resp.Body = newIdleBody(resp.Body, idle, info, cancel)
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		info, _ := r.Context().Value(ctxKey{}).(*requestInfo)
		var netErr interface{ Timeout() bool }
		if errors.As(err, &netErr) && netErr.Timeout() && info != nil {
			cuts.add(cut{At: time.Now(), Path: info.path, Kind: "first_byte", Elapsed: time.Since(info.started).Seconds()})
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(524)
			_, _ = io.WriteString(w, "<html><body>524: A timeout occurred (cdn-sim)</body></html>")
			return
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/_cuts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cuts.list())
	})
	mux.HandleFunc("/_requests", func(w http.ResponseWriter, r *http.Request) {
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(requests.since(since))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		info := &requestInfo{started: time.Now(), path: r.URL.Path}
		ctx = context.WithValue(context.WithValue(ctx, ctxKey{}, info), cancelKey{}, context.CancelFunc(cancel))
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			requests.add(proxied{Path: info.path, Status: sw.status, Started: info.started, Duration: time.Since(info.started).Seconds()})
		}()
		proxy.ServeHTTP(sw, r.WithContext(ctx))
	})
	log.Printf("cdn-sim %s -> %s first_byte=%s idle=%s edge=%s", listen, target, firstByte, idle, edge)
	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

type cancelKey struct{}
