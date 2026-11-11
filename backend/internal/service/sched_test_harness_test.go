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
		if !reflect.DeepEqual(wantHeaders, c.Request.Header) || len(c.Keys) != 0 {
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

func TestSchedTestRealForwardingF6(t *testing.T) {
	for _, accountKind := range []string{"oauth", "api_key", "api_key_mapping_a_to_b"} {
		t.Run(accountKind, func(t *testing.T) {
			// Integration setup must feed the SAME inbound Responses body and
			// Codex client headers to the real handler and schedTestForward,
			// capturing both at HTTPUpstream.Do with a fake streaming upstream.
			// Only session/conversation identifiers and prompt_cache_key VALUES
			// may differ; their presence, namespace and stability still match.
			checks := []string{
				"same method, URL scheme/host/path/query, Request.Host, proxy URL and account ID",
				"same ordinary HTTPUpstream.Do transport; no account-test DoWithTLS path",
				"compare the full header map including Authorization from ACCOUNT credentials",
				"User-Agent, originator, version, Content-Type, Accept, OpenAI-Beta",
				"ChatGPT account/organization/project headers, account header overrides",
				"x-codex beta/routing/turn-state headers; session_id and conversation_id presence",
				"no user identity, client API key, Cookie, Forwarded, X-Forwarded-For or X-Real-IP",
				"compare the full decoded body after normalizing only session/cache identifier values",
				"model equals the real outbound model for the evidence key; mapping a -> b emits b",
				"stream=true, store=false, reasoning effort and summary=auto, output cap",
				"OAuth include=reasoning.encrypted_content and removal of unsupported fields match handler",
				"instructions, tools/tool_choice, full input order/content/call IDs and prior answer match",
				"prompt_cache_key namespace matches; stable across a tier, distinct across tests/tiers",
				"same first-output timeout configuration and stream parsing, including incomplete success usage",
				"test samples only reach SchedTestAttempt.Sink with frozen account/model/warm/previous tokens",
				"zero test samples in attempt ledger, quality engine Observe and real session memory",
				"zero usage/billing writes, schedule reports, recent-session successes or sticky response bindings",
				"zero test cache-diagnostic settlements; upstream errors retain test source in ops events",
				"upstream 429/transient/model/site state handling and OAuth usage snapshots remain active",
			}
			for _, check := range checks {
				t.Log(check)
			}
			t.Skip("needs integration: WP0-capture")
		})
	}
}
