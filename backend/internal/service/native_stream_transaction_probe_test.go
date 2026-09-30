package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

// This fixture compiles unchanged on 0.2.2 and on the candidate. The optional
// exported snapshot exists only on the candidate; the same event and zero-byte
// downstream write expose whether identity commits before I/O in each version.
func TestNativeStreamTransactionProbe(t *testing.T) {
	ctx := NewControlledRequestContext(context.Background(), "responses")
	r := controlledRequest(ctx)
	if field := reflect.ValueOf(r).Elem().FieldByName("NativeDelivery"); field.IsValid() && field.CanSet() {
		field.SetBool(true)
	}
	r.Ledger = scheduling.NewAttemptLedger(scheduling.RetryPolicy{MaxAttempts: 3, MaxPerAccount: 1, MaxPerTier: 3, CrossTier: true}, scheduling.LatencyProfile{}, time.Now(), time.Time{})
	if err := r.Ledger.BeginAttempt(1, 0, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	sink := &nativeProbeFailedWriter{ResponseWriter: c.Writer}
	sink.beforeWrite = func() { sink.committedAtWrite = r.Ledger.Snapshot().Committed }
	w := &schedulingResponseWriter{ResponseWriter: sink, request: r}
	w.Header().Set("Content-Type", "text/event-stream")
	n, err := w.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fixture\"}}\n\n"))
	committed := r.Ledger.Snapshot().Committed
	retry := r.Ledger.CanAttempt(2, 0, time.Now(), true)
	t.Logf("created_write bytes=%d error=%v committed_before_write=%t committed_after_error=%t semantic_seen=%t retry_blocked=%t", n, err, sink.committedAtWrite, committed, !r.semanticAt.IsZero(), errors.Is(retry, scheduling.ErrCommitted))
	if n != 0 || err == nil || !sink.committedAtWrite || !committed || !r.semanticAt.IsZero() || !errors.Is(retry, scheduling.ErrCommitted) {
		t.Fatal("identity must irreversibly commit before a failed first write without fabricating semantic content")
	}
}

type nativeProbeFailedWriter struct {
	gin.ResponseWriter
	beforeWrite      func()
	committedAtWrite bool
}

func (w *nativeProbeFailedWriter) Write([]byte) (int, error) {
	w.beforeWrite()
	return 0, errors.New("fixture downstream disconnected")
}
