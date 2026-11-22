//go:build unit

package handler

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const (
	seamTestCreated  = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_a\"}}\n\n"
	seamTestProgress = "data: {\"type\":\"response.in_progress\"}\r\n\r\n"
	seamTestPing     = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
	seamTestComment  = ":\n\n"
	seamTestFailed   = "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n"
	seamTestError    = "event: error\ndata: {\"error\":{\"message\":\"no\"}}\n\n"
	seamTestDelta    = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"
	seamTestJSON     = `{"status":"completed","output":[]}`
	seamTestJSONFail = `{"status":"failed","output":[]}`
	seamTestHTTPFail = `{"error":{"message":"refused"}}`
)

type seamTestServer struct {
	server *httptest.Server
	run    func(*gin.Context)
}

func newSeamTestServer(t *testing.T) *seamTestServer {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := &seamTestServer{}
	engine := gin.New()
	engine.POST("/", func(c *gin.Context) { s.run(c) })
	s.server = httptest.NewServer(engine)
	t.Cleanup(s.server.Close)
	return s
}

type seamTestWire struct {
	status int
	header http.Header
	body   string
}

func (s *seamTestServer) do(t *testing.T, run func(*gin.Context)) seamTestWire {
	t.Helper()
	s.run = run
	resp, err := s.server.Client().Post(s.server.URL, "application/json", http.NoBody)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	header := resp.Header.Clone()
	// Framing follows flush timing; it is not part of the application response.
	for _, key := range []string{"Date", "Content-Length"} {
		header.Del(key)
	}
	return seamTestWire{status: resp.StatusCode, header: header, body: string(body)}
}

// withSeam installs the seam the way the Responses handler does.
func withSeam(c *gin.Context, rescue collabRescue, existingPath func()) {
	seam := &collabSeam{original: c.Writer, rescue: rescue}
	seam.writer = newCollabSeamWriter(c.Writer)
	c.Writer = seam.writer
	defer seam.restore(c)
	defer seam.finish(c)
	existingPath()
}

type seamTestOp struct {
	kind  string
	key   string
	value string
	code  int
}

func (op seamTestOp) apply(c *gin.Context) {
	switch op.kind {
	case "header":
		c.Writer.Header().Set(op.key, op.value)
	case "status":
		c.Writer.WriteHeader(op.code)
	case "flush":
		c.Writer.Flush()
	case "now":
		c.Writer.WriteHeaderNow()
	case "string":
		_, _ = c.Writer.WriteString(op.value)
	case "write":
		// Callers may reuse the buffer once Write has returned.
		buf := []byte(op.value)
		_, _ = c.Writer.Write(buf)
		for i := range buf {
			buf[i] = '#'
		}
	}
}

func seamTestSequence(r *rand.Rand) []seamTestOp {
	var ops []seamTestOp
	stream := r.Intn(4) != 0
	if stream {
		ops = append(ops, seamTestOp{kind: "header", key: "Content-Type", value: "text/event-stream"})
	} else {
		ops = append(ops, seamTestOp{kind: "header", key: "Content-Type", value: "application/json"})
	}
	if r.Intn(4) == 0 {
		ops = append(ops, seamTestOp{kind: "status", code: []int{200, 400, 409, 429, 502, 503}[r.Intn(6)]})
	}
	for range r.Intn(6) + 1 {
		switch r.Intn(12) {
		case 0:
			ops = append(ops, seamTestOp{kind: "flush"})
		case 1:
			ops = append(ops, seamTestOp{kind: "now"})
		case 2:
			ops = append(ops, seamTestOp{kind: "status", code: []int{200, 400, 502}[r.Intn(3)]})
		case 3:
			ops = append(ops, seamTestOp{kind: "header", key: []string{"Retry-After", "X-Request-Id"}[r.Intn(2)], value: fmt.Sprint(r.Intn(9))})
		case 4:
			ops = append(ops, seamTestOp{kind: "write"})
		default:
			var payload string
			if stream {
				frames := []string{seamTestCreated, seamTestProgress, seamTestPing, seamTestComment, seamTestFailed, seamTestError, seamTestDelta, "data: [DONE]\n\n", "event: partial"}
				for range r.Intn(3) + 1 {
					payload += frames[r.Intn(len(frames))]
				}
			} else {
				payload = []string{seamTestJSON, seamTestJSONFail, seamTestHTTPFail, " "}[r.Intn(4)]
			}
			for payload != "" {
				n := len(payload)
				if r.Intn(2) == 0 {
					n = r.Intn(len(payload)) + 1
				}
				ops = append(ops, seamTestOp{kind: []string{"write", "string"}[r.Intn(2)], value: payload[:n]})
				if r.Intn(5) == 0 {
					ops = append(ops, seamTestOp{kind: "flush"})
				}
				payload = payload[n:]
			}
		}
	}
	return ops
}

// Without a takeover the seam must be invisible, both on the wire and to the
// code above it, whatever the order of writes, flushes and header changes.
func TestCollabSeamIsTransparentWithoutTakeover(t *testing.T) {
	server := newSeamTestServer(t)
	r := rand.New(rand.NewSource(1))
	for i := range 6000 {
		ops := seamTestSequence(r)
		observe := func(seam bool) (seamTestWire, []string) {
			var seen []string
			wire := server.do(t, func(c *gin.Context) {
				path := func() {
					for _, op := range ops {
						op.apply(c)
						seen = append(seen, fmt.Sprint(op.kind, c.Writer.Written(), c.Writer.Size(), c.Writer.Status()))
					}
				}
				if seam {
					withSeam(c, nil, path)
				} else {
					path()
				}
				var header []string
				for key, values := range c.Writer.Header() {
					header = append(header, key+"="+strings.Join(values, ","))
				}
				sort.Strings(header)
				seen = append(seen, fmt.Sprint("after", c.Writer.Written(), c.Writer.Size(), c.Writer.Status(), header))
			})
			return wire, seen
		}
		wantWire, wantSeen := observe(false)
		gotWire, gotSeen := observe(true)
		require.Equal(t, wantSeen, gotSeen, "sequence %d: %+v", i, ops)
		require.Equal(t, wantWire, gotWire, "sequence %d: %+v", i, ops)
	}
}

func seamTestExistingError(c *gin.Context) func() {
	return func() {
		c.Header("Retry-After", "7")
		c.Set("existing_error", true)
		c.Data(http.StatusBadGateway, "application/json", []byte(seamTestHTTPFail))
	}
}

func TestCollabSeamTakeoverReplacesAnUncommittedError(t *testing.T) {
	server := newSeamTestServer(t)
	t.Run("stream", func(t *testing.T) {
		wire := server.do(t, func(c *gin.Context) {
			withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
				c.Header("Content-Type", "text/event-stream")
				_, _ = c.Writer.WriteString(seamTestCreated)
				c.Writer.Flush()
				require.False(t, seamTestCommitted(c), "prelude must not commit")
				_, _ = c.Writer.WriteString(seamTestDelta)
			}, seamTestExistingError(c))
		})
		require.Equal(t, http.StatusOK, wire.status)
		require.Equal(t, seamTestCreated+seamTestDelta, wire.body)
		require.Empty(t, wire.header.Get("Retry-After"))
		require.Contains(t, wire.header.Get("Content-Type"), "text/event-stream")
	})
	t.Run("json", func(t *testing.T) {
		wire := server.do(t, func(c *gin.Context) {
			withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
				c.Data(http.StatusOK, "application/json", []byte(seamTestJSON))
			}, seamTestExistingError(c))
		})
		require.Equal(t, http.StatusOK, wire.status)
		require.Equal(t, seamTestJSON, wire.body)
		require.Empty(t, wire.header.Get("Retry-After"))
	})
	t.Run("second attempt after a discarded one", func(t *testing.T) {
		wire := server.do(t, func(c *gin.Context) {
			withSeam(c, func(c *gin.Context, seam *collabSeam, _ *collabWithheld) {
				c.Header("X-Attempt", "first")
				c.Data(http.StatusConflict, "application/json", []byte(seamTestHTTPFail))
				seam.writer.discard()
				c.Data(http.StatusOK, "application/json", []byte(seamTestJSON))
			}, seamTestExistingError(c))
		})
		require.Equal(t, http.StatusOK, wire.status)
		require.Equal(t, seamTestJSON, wire.body)
		require.Empty(t, wire.header.Get("X-Attempt"))
	})
}

func TestCollabSeamFailedTakeoverKeepsTheExistingError(t *testing.T) {
	server := newSeamTestServer(t)
	existing := map[string]func(*gin.Context) func(){
		"uncommitted json": seamTestExistingError,
		"committed stream": func(c *gin.Context) func() {
			return func() {
				c.Set("existing_error", true)
				c.Header("Content-Type", "text/event-stream")
				_, _ = c.Writer.WriteString(seamTestPing)
				c.Writer.Flush()
				_, _ = c.Writer.WriteString(seamTestFailed)
				c.Writer.Flush()
			}
		},
		"uncommitted stream": func(c *gin.Context) func() {
			return func() {
				c.Set("existing_error", true)
				c.Header("Content-Type", "text/event-stream")
				_, _ = c.Writer.WriteString(seamTestCreated + seamTestFailed)
			}
		},
		"failed json": func(c *gin.Context) func() {
			return func() {
				c.Set("existing_error", true)
				c.Data(http.StatusOK, "application/json", []byte(seamTestJSONFail))
			}
		},
	}
	takeovers := map[string]collabRescue{
		"does nothing": func(*gin.Context, *collabSeam, *collabWithheld) {},
		"other error": func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
			c.Header("X-Attempt", "failed")
			c.Data(http.StatusTooManyRequests, "application/json", []byte(`{"error":{"message":"busy"}}`))
		},
		"prelude then error": func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
			if seamTestCommitted(c) {
				return // only an uncommitted response can hide a prelude
			}
			c.Header("Content-Type", "text/event-stream")
			_, _ = c.Writer.WriteString(seamTestCreated)
			c.Writer.Flush()
			_, _ = c.Writer.WriteString(seamTestFailed)
		},
		"prelude then silence": func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
			if seamTestCommitted(c) {
				return
			}
			c.Header("Content-Type", "text/event-stream")
			c.Writer.WriteHeaderNow()
			_, _ = c.Writer.WriteString(seamTestCreated)
		},
		"changes keys and request": func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
			c.Set("takeover", true)
			c.Set("existing_error", false)
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), seamTestKey{}, true))
		},
		"panics": func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
			c.Header("X-Attempt", "panic")
			panic("takeover failed")
		},
	}
	for errorName, path := range existing {
		want := server.do(t, func(c *gin.Context) { path(c)() })
		for takeoverName, rescue := range takeovers {
			t.Run(errorName+"/"+takeoverName, func(t *testing.T) {
				got := server.do(t, func(c *gin.Context) {
					withSeam(c, rescue, path(c))
					require.True(t, c.GetBool("existing_error"))
					_, changed := c.Get("takeover")
					require.False(t, changed)
					require.Nil(t, c.Request.Context().Value(seamTestKey{}))
				})
				require.Equal(t, want, got)
			})
		}
	}
}

type seamTestKey struct{}

// seamTestCommitted reports whether headers are really on the wire.
func seamTestCommitted(c *gin.Context) bool {
	w, ok := c.Writer.(*collabSeamWriter)
	return !ok || w.ResponseWriter.Written()
}

func TestCollabSeamNeverFollowsDeliveredOutput(t *testing.T) {
	server := newSeamTestServer(t)
	wire := server.do(t, func(c *gin.Context) {
		withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
			c.Header("Content-Type", "text/event-stream")
			_, _ = c.Writer.WriteString(seamTestDelta)
			_, _ = c.Writer.WriteString(seamTestFailed)
		}, seamTestExistingError(c))
	})
	require.Equal(t, seamTestDelta+seamTestFailed, wire.body)
	require.NotContains(t, wire.body, "refused")
}

func TestCollabSeamSkipsTakeover(t *testing.T) {
	server := newSeamTestServer(t)
	for name, path := range map[string]func(*gin.Context){
		"success":            func(c *gin.Context) { c.Data(http.StatusOK, "application/json", []byte(seamTestJSON)) },
		"error after output": func(c *gin.Context) { seamTestStream(c, seamTestDelta, seamTestFailed) },
		"client canceled": func(c *gin.Context) {
			ctx, cancel := context.WithCancel(c.Request.Context())
			cancel()
			c.Request = c.Request.WithContext(ctx)
			c.Data(http.StatusBadGateway, "application/json", []byte(seamTestHTTPFail))
		},
	} {
		t.Run(name, func(t *testing.T) {
			var called bool
			server.do(t, func(c *gin.Context) {
				withSeam(c, func(*gin.Context, *collabSeam, *collabWithheld) { called = true }, func() { path(c) })
			})
			require.False(t, called)
		})
	}
}

func seamTestStream(c *gin.Context, frames ...string) {
	c.Header("Content-Type", "text/event-stream")
	for _, frame := range frames {
		_, _ = c.Writer.WriteString(frame)
		c.Writer.Flush()
	}
}

func TestCollabSeamPanicReleasesTheWithheldError(t *testing.T) {
	server := newSeamTestServer(t)
	var recovered any
	wire := server.do(t, func(c *gin.Context) {
		defer func() { recovered = recover() }()
		withSeam(c, nil, func() {
			c.Data(http.StatusBadGateway, "application/json", []byte(seamTestHTTPFail))
			panic("existing path")
		})
	})
	require.Equal(t, "existing path", recovered)
	require.Equal(t, http.StatusBadGateway, wire.status)
	require.Equal(t, seamTestHTTPFail, wire.body)
}

// A heartbeat goroutine may write while the request goroutine starts output.
func TestCollabSeamConcurrentHeartbeat(t *testing.T) {
	server := newSeamTestServer(t)
	const beats = 64
	wire := server.do(t, func(c *gin.Context) {
		withSeam(c, nil, func() {
			c.Header("Content-Type", "text/event-stream")
			w := c.Writer
			var wg sync.WaitGroup
			var serial sync.Mutex // stands for the keepalive's own lock
			wg.Go(func() {
				for range beats {
					serial.Lock()
					_, _ = w.WriteString(seamTestPing)
					serial.Unlock()
				}
			})
			serial.Lock()
			_, _ = w.WriteString(seamTestDelta)
			serial.Unlock()
			wg.Wait()
		})
	})
	require.Equal(t, beats, strings.Count(wire.body, seamTestPing))
	require.Equal(t, 1, strings.Count(wire.body, seamTestDelta))
}

func BenchmarkCollabSeamStream(b *testing.B) {
	gin.SetMode(gin.TestMode)
	delta := []byte(seamTestDelta)
	for _, seam := range []bool{false, true} {
		b.Run(fmt.Sprintf("seam=%v", seam), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				c, _ := gin.CreateTestContext(&seamTestDiscard{header: http.Header{"Content-Type": {"text/event-stream"}}})
				w := gin.ResponseWriter(c.Writer)
				if seam {
					w = newCollabSeamWriter(c.Writer)
				}
				for range 2000 {
					_, _ = w.Write(delta)
				}
			}
		})
	}
}

type seamTestDiscard struct{ header http.Header }

func (d *seamTestDiscard) Header() http.Header         { return d.header }
func (d *seamTestDiscard) Write(p []byte) (int, error) { return len(p), nil }
func (d *seamTestDiscard) WriteHeader(int)             {}
func (d *seamTestDiscard) Flush()                      {}
