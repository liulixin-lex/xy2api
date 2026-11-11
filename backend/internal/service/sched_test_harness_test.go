//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
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
		_, _ = c.Writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		return &OpenAIForwardResult{}, nil
	}}
	_, samples, answer, err := schedTestForward(context.Background(), forwarder, account, "evidence", "session", []byte(`{}`), true, 42)
	if err != nil || len(samples) != 1 || samples[0].Outcome != schedquality.OutcomeOK || answer != "hello" {
		t.Fatalf("samples=%v answer=%q err=%v", samples, answer, err)
	}
}

func TestSchedTestRealForwardingF6(t *testing.T) {
	t.Skip("needs integration: WP0-capture")
}
