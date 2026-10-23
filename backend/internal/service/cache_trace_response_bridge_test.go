package service

import (
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	traceconfig "github.com/liulixin-lex/xy2api/internal/config"
)

func TestCacheTraceIndexedStreamFoldMatchesOriginal(t *testing.T) {
	frame := func(body string) string { return "data: " + body + "\n\n" }
	progressive := frame(`{"type":"response.output_text.delta","usage":{"input_tokens":2048,"output_tokens":10,"cache_read_input_tokens":512}}`)
	terminal := frame(`{"type":"response.completed","response":{"usage":{"input_tokens":4096,"output_tokens":21,"input_tokens_details":{"cached_tokens":1024}}}}`)
	for _, tc := range []struct{ name, wire string }{
		{"terminal_authority", progressive + terminal},
		{"zero_terminal_retains_progressive", progressive + frame(`{"type":"response.completed","response":{"usage":{"input_tokens":0,"output_tokens":0,"cached_tokens":0}}}`)},
		{"missing_P_terminal_overwrites", progressive + frame(`{"type":"response.completed","response":{"usage":{"output_tokens":5,"cached_tokens":0}}}`)},
		{"multiple_nonterminal", progressive + frame(`{"type":"response.output_text.delta","usage":{"input_tokens":0,"cache_read_input_tokens":0}}`) + terminal},
		{"half_frame", progressive + `data: {"response":{"usage":{"input_tokens":4096`},
		{"failed_terminal", progressive + frame(`{"type":"response.failed","response":{"usage":{"input_tokens":4096,"output_tokens":21,"cache_read_input_tokens":1024},"error":{"message":"private-error"}}}`)},
		{"no_DONE", progressive + terminal},
		{"error_no_usage", progressive + frame(`{"type":"error","error":{"message":"private-error"}}`)},
		{"non_utf8", frame("{\"delta\":\"bad-"+string([]byte{0xff})+"\"}") + terminal},
		{"large_delta", frame(`{"type":"response.output_text.delta","delta":"`+strings.Repeat("x", 2<<20)+`"}`) + terminal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			want := svc.parseSSEUsageFromBody(tc.wire)
			var got OpenAIUsage
			forEachOpenAISSEFrame(tc.wire, func(kind string, data []byte) { svc.parseSSEUsageBytesWithTypeIndexed(data, kind, &got) })
			if !reflect.DeepEqual(got, *want) {
				t.Fatalf("indexed fold %#v != original %#v", got, *want)
			}
		})
	}
}

func TestCacheTraceIndexedRawCCRetainsLaxParserSemantics(t *testing.T) {
	for _, payload := range []string{
		`{"usage":{"prompt_tokens":2048,"prompt_tokens_details":{"cached_tokens":0}}}`,
		`{"usage":{"prompt_tokens":2048},"unfinished":`,
		`{"usage":{"prompt_tokens":2048`,
		`{"usage":null,"usage":{"prompt_tokens":2048}}`,
		`{"usage":{"prompt_tokens":0},"usage":{"prompt_tokens":2048}}`,
		`{"response":{"usage":{"prompt_tokens":2048}}}`,
		`{"usage":{"prompt_tokens":2048},"tool_usage":{"image_gen":{"output_tokens_details":{"image_tokens":7}}}}`,
		"{\"usage\":{\"prompt_tokens\":2048},\"delta\":\"bad-" + string([]byte{0xff}) + "\"}",
	} {
		want := extractCCStreamUsage(payload)
		got, _ := extractCCStreamUsageIndexed(payload)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("raw CC semantics changed for %q: %#v != %#v", payload, got, want)
		}
	}
}

// These are additive tests for the optional indexed production arm. Existing
// two-argument callback tests and result.CacheTrace == nil assertions remain.
func TestCacheTraceIndexedBufferedObserverFailureIsolation(t *testing.T) {
	svc := &OpenAIGatewayService{}
	for _, tc := range cacheBillingCorpus(false) {
		t.Run(tc.name, func(t *testing.T) {
			expected := svc.parseSSEUsageFromBody(tc.wire)
			expectedBill := cacheTraceBillingForUsage(t, *expected)
			frames := 0
			forEachOpenAISSEFrame(tc.wire, func(string, []byte) { frames++ })
			for _, at := range []int{1, max(1, frames/2), frames} {
				for _, failure := range []string{"error", "panic"} {
					calls := 0
					tr := &cacheTrace{}
					got, complete := svc.parseSSEUsageObserving(tc.wire, func([]byte) error {
						calls++
						if calls == at {
							if failure == "panic" {
								panic("private-indexed-observer-panic")
							}
							return errors.New("private-indexed-observer-error")
						}
						return nil
					}, tr)
					if complete || calls != at || !reflect.DeepEqual(got, expected) {
						t.Fatalf("failure isolation changed: complete=%t calls=%d at=%d usage=%#v/%#v", complete, calls, at, got, expected)
					}
					if gotBill := cacheTraceBillingForUsage(t, *got); !reflect.DeepEqual(gotBill, expectedBill) {
						t.Fatal("billing fields/calls changed")
					}
				}
			}
		})
	}
}

func TestCacheTraceIndexedPromptPresenceFollowsBillingFold(t *testing.T) {
	svc := &OpenAIGatewayService{}
	for _, tc := range []struct {
		name, terminal string
		wantP          int
		wantPresence   bool
	}{
		{"terminal_missing_P", `{"type":"response.completed","response":{"usage":{"output_tokens":5,"cached_tokens":0}}}`, 0, false},
		{"zero_terminal_retained", `{"type":"response.completed","response":{"usage":{"input_tokens":0,"output_tokens":0,"cached_tokens":0}}}`, 2048, true},
		{"explicit_zero_authoritative", `{"type":"response.completed","response":{"usage":{"input_tokens":0,"output_tokens":5,"cached_tokens":0}}}`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &cacheTrace{}
			wire := "data: " + `{"type":"response.output_text.delta","usage":{"input_tokens":2048,"output_tokens":1,"cached_tokens":0}}` + "\n\ndata: " + tc.terminal + "\n\n"
			usage, complete := svc.parseSSEUsageObserving(wire, nil, tr)
			if !complete || usage.InputTokens != tc.wantP || tr.data.UsagePromptPresent != tc.wantPresence || !tr.data.UsagePromptObserved {
				t.Fatalf("usage/presence drift: %#v presence=%t observed=%t", usage, tr.data.UsagePromptPresent, tr.data.UsagePromptObserved)
			}
		})
	}
}

func TestCacheTraceBufferedTypedPresenceMatchesFinalUsage(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		wantP        int
		wantPresence bool
	}{
		{"last_root_usage_missing_P", `"response":{},"usage":{"input_tokens":2048,"output_tokens":1},"usage":{"output_tokens":7,"cached_tokens":0}`, 0, false},
		{"last_response_usage_missing_P", `"response":{"usage":{"input_tokens":2048,"output_tokens":1}},"response":{"usage":{"output_tokens":7,"cached_tokens":0}}`, 0, false},
		{"duplicate_response_merges_usage", `"response":{"usage":{"input_tokens":2048}},"response":{"status":"completed"}`, 2048, true},
		{"null_response_resets_then_explicit_zero", `"response":{"usage":{"input_tokens":2048}},"response":null,"response":{"usage":{"input_tokens":0,"output_tokens":7,"cached_tokens":0}}`, 0, true},
		{"null_response_no_typed_override", `"response":{"usage":{"input_tokens":2048}},"response":null,"response":{}`, 2048, true},
		{"root_fills_missing_response_usage", `"response":{},"usage":{"input_tokens":0,"output_tokens":7,"cached_tokens":0}`, 0, true},
		{"response_overrides_root", `"usage":{"input_tokens":2048},"response":{"usage":{"output_tokens":7,"cached_tokens":0}}`, 0, false},
		{"case_insensitive_root", `"response":{},"Usage":{"input_tokens":2048,"cached_tokens":0}`, 2048, true},
		{"case_insensitive_response", `"Response":{"Usage":{"input_tokens":2048,"cached_tokens":0}}`, 2048, true},
		{"escaped_root", `"response":{},"us\u0061ge":{"input_tokens":2048,"cached_tokens":0}`, 2048, true},
		{"inner_duplicate_counts_first", `"response":{"usage":{"input_tokens":0,"input_tokens":2048,"output_tokens":7,"cached_tokens":0}}`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := "data: {\"type\":\"response.completed\"," + tc.fields + "}\n\n"
			var baseline OpenAIUsage
			for _, enabled := range []bool{false, true} {
				configure := traceTestConfig
				if !enabled {
					configure = func(cfg *traceconfig.Config) { traceTestConfig(cfg); cfg.Gateway.CacheTrace.Enabled = false }
				}
				svc, c, _, _ := cacheTraceWireFixture(t, "chat", "apikey", "private-pck", "session_id", configure)
				if enabled {
					resetCacheTraceAttempt(c)
				}
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}
				account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				_, usage, _, err := svc.readOpenAICompatBufferedTerminal(resp, c, account, "gpt-5.4", "cache trace typed fixture", "fixture")
				if err != nil {
					t.Fatal(err)
				}
				if usage.InputTokens != tc.wantP {
					t.Fatalf("got P=%d, want %d", usage.InputTokens, tc.wantP)
				}
				if !enabled {
					baseline = usage
					continue
				}
				if !reflect.DeepEqual(usage, baseline) {
					t.Fatalf("typed billing changed: %#v / %#v", usage, baseline)
				}
				tr := cacheTraceFromGin(c)
				if tr == nil || tr.data.UsagePromptPresent != tc.wantPresence {
					t.Fatalf("final typed presence mismatch; trace=%#v want=%t", tr, tc.wantPresence)
				}
			}
		})
	}
}

func TestCacheTraceIndexedEscapedUsageRetainsOriginalFastGuard(t *testing.T) {
	svc := &OpenAIGatewayService{}
	for _, body := range []string{
		`{"type":"response.output_text.delta","us\u0061ge":{"input_tokens":2048},"prompt_cache_diagnostics":"model_changed"}`,
		`{"type":"response.output_text.delta","Usage":{"input_tokens":2048}}`,
	} {
		var baseline, indexed OpenAIUsage
		svc.parseSSEUsageBytesWithType([]byte(body), "response.output_text.delta", &baseline)
		svc.parseSSEUsageBytesWithTypeIndexed([]byte(body), "response.output_text.delta", &indexed, true)
		if !reflect.DeepEqual(indexed, baseline) {
			t.Fatalf("literal usage fast guard changed: %#v / %#v", indexed, baseline)
		}
	}
}
