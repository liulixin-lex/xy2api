//go:build unit

package service

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func TestSchedTestHarness(t *testing.T) {
	account := &Account{ID: 37}
	forwarder := &fakeSchedTestForwarder{Fn: func(ctx context.Context, c *gin.Context, a *Account, body []byte) (*OpenAIForwardResult, error) {
		state := resilience.StateFrom(ctx)
		selection, ok := state.SchedSelection()
		if !ok || selection.AccountID != 37 || selection.Model != "evidence" || !selection.StickyWarm {
			t.Error("missing frozen selection")
		}
		attempt, ok := state.SchedTest().(*SchedTestAttempt)
		if !ok || !attempt.Warm || attempt.PrevPromptTokens != 42 {
			t.Fatal("missing attempt")
		}
		attempt.Sink(schedquality.AttemptSample{Outcome: schedquality.OutcomeOK})
		if a != account || c.Request.RemoteAddr != "" || c.Request.Header.Get("Authorization") != "" || c.Request.Header.Get("User-Agent") != DefaultOpenAICodexUserAgent {
			t.Error("incorrect internal request")
		}
		wantHeaders := http.Header{
			"Content-Type": {"application/json"}, "Accept": {"text/event-stream"},
			"User-Agent": {DefaultOpenAICodexUserAgent}, "Originator": {openai.CodexDefaultOriginator},
		}
		if !reflect.DeepEqual(wantHeaders, c.Request.Header) {
			t.Error("internal request contains unexpected client identity")
		}
		if c.Request.Context() != ctx || c.Request.Method != http.MethodPost || c.Request.URL.Path != "/v1/responses" {
			t.Error("incorrect forwarding context")
		}
		_, _ = c.Writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		return &OpenAIForwardResult{}, nil
	}}
	_, samples, answer, err := schedTestForward(context.Background(), forwarder, account, "evidence", []byte(`{}`), true, 42)
	if err != nil || len(samples) != 1 || samples[0].Outcome != schedquality.OutcomeOK || answer != "hello" {
		t.Fatalf("samples=%v answer=%q err=%v", samples, answer, err)
	}
}

// SchedTestForwardForTest lets the external test compare the harness with the real handler.
var SchedTestForwardForTest = schedTestForward
