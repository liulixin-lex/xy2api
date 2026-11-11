package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type schedTestWriter struct {
	header http.Header
	body   bytes.Buffer
}

func (w *schedTestWriter) Header() http.Header { return w.header }
func (w *schedTestWriter) WriteHeader(int)     {}
func (w *schedTestWriter) Write(b []byte) (int, error) {
	if left := 1024*1024 - w.body.Len(); left > 0 {
		_, _ = w.body.Write(b[:min(left, len(b))])
	}
	return len(b), nil
}
func (w *schedTestWriter) Flush() {}

func schedTestForward(ctx context.Context, f SchedTestForwarder, account *Account, model string, body []byte, warm bool, prev int) (*OpenAIForwardResult, []schedquality.AttemptSample, string, error) {
	var mu sync.Mutex
	var samples []schedquality.AttemptSample
	state := resilience.NewRequestState()
	state.SetSchedSelection(resilience.SchedSelection{AccountID: account.ID, Model: model, StickyWarm: warm})
	state.SetSchedTest(&SchedTestAttempt{Warm: warm, PrevPromptTokens: prev, Sink: func(sample schedquality.AttemptSample) {
		mu.Lock()
		defer mu.Unlock()
		samples = append(samples, sample)
	}})
	ctx = resilience.WithRequestState(ctx, state)
	writer := &schedTestWriter{header: make(http.Header)}
	c, _ := gin.CreateTestContext(writer)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", io.NopCloser(bytes.NewReader(body))).WithContext(ctx)
	req.RemoteAddr = ""
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", DefaultOpenAICodexUserAgent)
	req.Header.Set("originator", openai.CodexDefaultOriginator)
	c.Request = req
	InstallDeliveryTracking(c, time.Now())
	result, err := f.Forward(ctx, c, account, body)
	FinishSchedAttemptAtHandlerExit(ctx)
	mu.Lock()
	captured := append([]schedquality.AttemptSample(nil), samples...)
	mu.Unlock()
	return result, captured, schedTestResponseText(writer.body.String()), err
}

func schedTestResponseText(body string) string {
	var answer strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) == nil && event.Type == "response.output_text.delta" {
			answer.WriteString(event.Delta)
		}
	}
	return answer.String()
}
