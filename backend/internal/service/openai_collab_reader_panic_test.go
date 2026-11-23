//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type collabReaderPanic struct{}

func (collabReaderPanic) Read([]byte) (int, error) { panic("synthetic reader failure") }
func (collabReaderPanic) Close() error             { return nil }

type collabReaderLogSink chan *logger.LogEvent

func (s collabReaderLogSink) WriteLogEvent(e *logger.LogEvent) {
	if e.Message == "gateway.sse_reader_panic" {
		s <- e
	}
}

func TestCollabReaderPanicIsLoggedAndCounted(t *testing.T) {
	logger.InitBootstrap()
	logs := make(collabReaderLogSink, 1)
	logger.SetSink(logs)
	t.Cleanup(func() { logger.SetSink(nil) })
	before := resilience.CollabSafetySnapshot().ConsumerPanics
	c := newOpenAIRejectedFieldTestContext([]byte(`{"input":[]}`))
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: collabReaderPanic{}}
	svc := newOpenAIRejectedFieldTestService(nil)
	svc.cfg.Gateway.StreamDataIntervalTimeout = 1
	_, err := svc.handleStreamingResponse(context.Background(), response, c, &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, time.Now(), "synthetic", "synthetic")
	require.Error(t, err, "reader failure must not report a delivered success")
	select {
	case event := <-logs:
		require.Equal(t, "error", event.Level)
		require.Equal(t, "openai.responses.sse_reader", event.Fields["component"])
		require.Equal(t, "synthetic reader failure", event.Fields["panic"])
	case <-time.After(time.Second):
		t.Fatal("missing reader panic log")
	}
	require.Equal(t, before+1, resilience.CollabSafetySnapshot().ConsumerPanics)
}
