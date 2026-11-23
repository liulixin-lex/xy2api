//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

var collabRegressionCtxSink context.Context

func BenchmarkCollabRegressionUsageOff(b *testing.B) {
	for _, old := range []bool{false, true} {
		b.Run(fmt.Sprint("old=", old), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if old {
					collabRegressionCtxSink = collabRegressionOldUsageRecordContext(context.Background(), context.Background())
				} else {
					collabRegressionCtxSink = usageRecordContext(context.Background(), context.Background())
				}
			}
		})
	}
}
func TestCollabRegressionLosslessHeartbeatRestored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		seam := &collabSeam{original: c.Writer, rescue: func(*gin.Context, *collabSeam, *collabWithheld) {}, lossless: true, losslessDuration: time.Second}
		seam.writer = newCollabSeamWriter(c.Writer, seamTestAlways)
		c.Writer = seam.writer
		stop := service.StartPreOutputKeepalive(c, service.PreOutputProtocolResponses, 0, time.Second)
		c.Set(ginKeyPreOutputKeepaliveStop, stop)
		defer stop()
		synctest.Wait()
		before := rec.Body.Len()
		if before == 0 {
			t.Fatal("heartbeat fixture not committed")
		}
		if seam.tryLossless(c) {
			t.Fatal("unexpected delivery")
		}
		service.ResumeCommittedPreOutputKeepalive(c, time.Second)
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if rec.Body.Len() == before {
			t.Error("failed preemption restored a closed heartbeat; no heartbeats resumed")
		}
	})
}
func TestCollabRegressionPanicAfterOutputKeepsChildOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	parent := resilience.NewRequestState()
	parent.EnableGatewayRequest(time.Now(), resilience.GatewayRequestScopeText)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), parent))
	withSeam(c, func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
		child := resilience.NewCollabChildState(parent)
		c.Request = c.Request.WithContext(resilience.WithCollabAttempt(resilience.WithRequestState(c.Request.Context(), child), resilience.CollabAttempt{Optional: true}))
		c.Header("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString(seamTestDelta)
		panic("synthetic post-output panic")
	}, seamTestExistingError(c))
	if resilience.StateFrom(c.Request.Context()) != parent {
		t.Error("post-output panic left deferred cleanup reading child state")
	}
	if !resilience.CollabAttemptFrom(c.Request.Context()).CustomerSettlementAllowed() {
		t.Error("post-output panic left optional-undelivered metadata")
	}
}
func TestCollabRegressionContinuedMultilineIdentity(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	w := newCollabSeamWriter(c.Writer, seamTestAlways)
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.WriteString(seamTestCreated)
	_, _ = w.WriteString(seamTestFailed)
	w.suspend()
	got := string(w.continuePrelude([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\n" + "data: \"response\":{\"id\":\"resp_other\"},\"sequence_number\":0}\n\n")))
	if strings.Contains(got, "resp_other") {
		t.Error("multiline SSE continued using replacement response ID")
	}
}
func BenchmarkCollabRegressionArm(b *testing.B) {
	gin.SetMode(gin.TestMode)
	old := resilience.Current().Clone()
	defer resilience.Publish(old)
	for _, size := range []int{1 << 20, 10 << 20} {
		for _, mode := range []string{"off", "ladder", "lossless_first"} {
			b.Run(fmt.Sprintf("bytes=%d/%s", size, mode), func(b *testing.B) {
				settings := resilience.Defaults()
				settings.Collab.Ladder = mode != "off"
				settings.Collab.LosslessFirst = mode == "lossless_first"
				resilience.Publish(settings)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(resilience.WithRequestState(context.Background(), resilience.NewRequestState()))
				c.Request.Header.Set("User-Agent", "codex_cli_rs/0.166.0")
				original, request := c.Writer, c.Request
				h := &OpenAIGatewayHandler{}
				h.collabRescue = h.rescueCollab
				h.collabRescuable = collabCanRescue
				body := []byte(`{"input":[{"type":"agent_message","id":"amsg_a","content":[{"type":"encrypted_content","encrypted_content":"` + strings.Repeat("x", size) + `"}]}]}`)
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					c.Writer, c.Request = original, request
					s := h.armCollabSeam(c, body, "", service.PlatformOpenAI, false)
					h.armCollabLossless(c, s)
				}
			})
		}
	}
}
func collabRegressionOldUsageRecordContext(parent context.Context, base context.Context) context.Context {
	if base == nil {
		base = context.Background()
	}
	if parent == nil {
		return base
	}
	if clientRequestID, _ := parent.Value(ctxkey.ClientRequestID).(string); strings.TrimSpace(clientRequestID) != "" {
		base = context.WithValue(base, ctxkey.ClientRequestID, strings.TrimSpace(clientRequestID))
	}
	if requestID, _ := parent.Value(ctxkey.RequestID).(string); strings.TrimSpace(requestID) != "" {
		base = context.WithValue(base, ctxkey.RequestID, strings.TrimSpace(requestID))
	}
	if meta := resilience.GatewayRequestMetadataFromContext(parent); meta != nil {
		base = resilience.WithGatewayRequestMetadata(base, meta)
	}
	base = service.WithInflightReservation(base, service.InflightReservationFromContext(parent))
	// xy2api resilience: billing needs the request's delivery state; a client
	// already gone is resolved now.
	if st := resilience.StateFrom(parent); st != nil {
		if parent.Err() != nil {
			st.ResolveDisconnect()
		}
		base = resilience.WithRequestState(base, st)
	}
	return base
}

func TestCollabRegressionWatchResumesAfterAcceptedSendFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var accepted, queued atomic.Bool
		ctx, stop := watchCollabTakeover(context.Background(), 15*time.Second, accepted.Load, func(context.Context) bool { return queued.Load() })
		defer stop()
		accepted.Store(true)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		// The accepted reasoning item was followed by a rejection; a new
		// optional send has a fresh observer and a user is now waiting.
		accepted.Store(false)
		queued.Store(true)
		time.Sleep(70 * time.Second)
		synctest.Wait()
		if ctx.Err() == nil {
			t.Error("next optional send is still live after 72s, despite waiters and the 15s limit")
		}
	})
}

func TestCollabRegressionSavedStreamRetryability(t *testing.T) {
	saved := &collabWithheld{status: 200, header: http.Header{}, ops: []collabSeamOp{{kind: collabSeamWrite, data: []byte(seamTestFailed)}}}
	if !collabSavedRetryable(saved) {
		t.Fatal("buffered stream failure lost client retryability")
	}
	saved.header.Set("x-should-retry", "false")
	if collabSavedRetryable(saved) {
		t.Fatal("explicit retry refusal lost")
	}
}
func TestCollabRegressionSingleJSONDocument(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w := newCollabSeamWriter(c.Writer, seamTestAlways)
	w.WriteHeader(400)
	w.WriteString(seamTestHTTPFail)
	w.suspend()
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.WriteString(seamTestJSON); err != nil {
		t.Fatal(err)
	}
	if n, err := w.WriteString(`{"extra":true}`); n != 0 || !errors.Is(err, http.ErrBodyNotAllowed) {
		t.Fatalf("second document: %d %v", n, err)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatal("invalid delivered JSON")
	}
}

func TestCollabRegressionFailedPreemptionRestoresRequest(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set("X-Synthetic", "original")
	original := c.Request
	c.Header("X-Synthetic-Response", "original")
	c.Error(errors.New("original error"))
	seam := &collabSeam{original: c.Writer, losslessDuration: time.Second}
	seam.writer = newCollabSeamWriter(c.Writer, seamTestAlways)
	c.Writer = seam.writer
	seam.rescue = func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
		c.Request.Header.Set("X-Synthetic", "changed")
		c.Header("X-Synthetic-Response", "changed")
		c.Errors[0].Err = errors.New("changed error")
	}
	if seam.tryLossless(c) {
		t.Fatal("unexpected delivery")
	}
	if c.Request != original || original.Header.Get("X-Synthetic") != "original" || c.Writer.Header().Get("X-Synthetic-Response") != "original" || c.Errors[0].Error() != "original error" {
		t.Fatal("failed attempt leaked request mutations")
	}
}

func BenchmarkCollabContinuationWrite(b *testing.B) {
	for _, active := range []bool{false, true} {
		b.Run(fmt.Sprint(active), func(b *testing.B) {
			c, _ := gin.CreateTestContext(&seamTestDiscard{header: http.Header{"Content-Type": {"text/event-stream"}}})
			w := newCollabSeamWriter(c.Writer, seamTestAlways)
			w.set(collabSeamPassing)
			w.prelude = collabDeliveredPrelude{ID: "resp_previous", Created: true}
			w.takeover.Store(active)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				w.WriteString(seamTestDelta)
			}
		})
	}
}

type collabPartialWriter struct{ gin.ResponseWriter }

func (w collabPartialWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}
func (w collabPartialWriter) WriteString(p string) (int, error) { return w.Write([]byte(p)) }
func TestCollabRegressionContinuationPartialAndOverflow(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	w := newCollabSeamWriter(collabPartialWriter{c.Writer}, seamTestAlways)
	w.Header().Set("Content-Type", "text/event-stream")
	w.set(collabSeamPassing)
	w.takeover.Store(true)
	w.prelude = collabDeliveredPrelude{Created: true, ID: "resp_previous"}
	n, err := w.WriteString(seamTestDelta)
	if n > len(seamTestDelta) || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("partial count=%d err=%v", n, err)
	}
	n, err = w.WriteString("data: " + strings.Repeat("x", collabSeamQuietLimit))
	if n != 0 || !errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("overflow count=%d err=%v", n, err)
	}
}

func TestCollabContinuationFramesDecode(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		for _, width := range []int{1, 4096} {
			t.Run(fmt.Sprintf("newline=%q/width=%d", newline, width), func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				w := newCollabSeamWriter(c.Writer, seamTestAlways)
				w.Header().Set("Content-Type", "text/event-stream")
				w.set(collabSeamPassing)
				w.takeover.Store(true)
				w.prelude = collabDeliveredPrelude{ID: "resp_previous", Created: true, CreatedAt: 10, NextSequence: 4}
				wire := strings.Join([]string{
					"event: response.output_text.delta",
					`data: {"type":"response.output_text.delta","delta":"text\nmore","sequence_number":0}`,
					"",
					"event: response.completed",
					`data: {"type":"response.completed",`,
					`data: "response":{"id":"resp_upstream",`,
					`data: "created_at":20,"status":"completed"},"sequence_number":1}`,
					"", "",
				}, newline)
				for len(wire) > 0 {
					size := min(width, len(wire))
					n, err := w.WriteString(wire[:size])
					if err != nil || n != size {
						t.Fatalf("write %d: %d %v", size, n, err)
					}
					wire = wire[size:]
				}
				events, err := sse.Decode(strings.NewReader(rec.Body.String()))
				if err != nil || len(events) != 2 {
					t.Fatalf("decode: events=%d err=%v", len(events), err)
				}
				for i, event := range events {
					payload, ok := event.Data.(string)
					if !ok || !json.Valid([]byte(payload)) {
						t.Fatalf("event %d is not JSON: %v", i, event.Data)
					}
					if got := gjson.Get(payload, "sequence_number").Int(); got != int64(i+4) {
						t.Fatalf("sequence=%d", got)
					}
					if i == 1 && (event.Event != "response.completed" || gjson.Get(payload, "response.id").String() != "resp_previous" || gjson.Get(payload, "response.created_at").Int() != 10) {
						t.Fatalf("bad completed event: %s", payload)
					}
				}
			})
		}
	}
}
