package iqcheck

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIQCompletedItemsRecovery(t *testing.T) {
	done := `data: {"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"21"}]}}` + "\n\n"
	terminal := `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}` + "\n\n"
	for _, tc := range []struct{ name, body, status, reason string }{
		{"empty terminal", done + terminal, "smart", "correct_answer"},
		{"idempotent done", done + done + terminal, "smart", "correct_answer"},
		{"wrong answer", strings.Replace(done, `"text":"21"`, `"text":"29"`, 1) + terminal, "degraded", "wrong_answer"},
		{"missing completion", done, "unknown", "incomplete_response"},
		{"delta only", `data: {"type":"response.output_text.delta","delta":"21"}` + "\n\n" + terminal, "unknown", "missing_final_message"},
		{"conflicting done", done + strings.Replace(done, `"text":"21"`, `"text":"29"`, 1) + terminal, "unknown", "conflicting_final_response"},
		{"conflicting index", done + strings.Replace(done, `"output_index":1`, `"output_index":2`, 1) + terminal, "unknown", "conflicting_output_identity"},
		{"commentary", strings.Replace(done, `"final_answer"`, `"commentary"`, 1) + terminal, "unknown", "missing_final_message"},
		{"empty final", strings.Replace(done, `"text":"21"`, `"text":""`, 1) + terminal, "unknown", "empty_final_text"},
		{"incomplete item", strings.Replace(done, `"status":"completed"`, `"status":"in_progress"`, 1) + terminal, "unknown", "invalid_completed_message"},
		{"missing item status", strings.Replace(done, `"status":"completed",`, "", 1) + terminal, "unknown", "invalid_completed_message"},
		{"user item", strings.Replace(done, `"role":"assistant"`, `"role":"user"`, 1) + terminal, "unknown", "invalid_completed_message"},
		{"failed", done + strings.Replace(terminal, `"status":"completed"`, `"status":"failed"`, 1), "unknown", "incomplete_response"},
		{"refusal", strings.Replace(done, `"output_text"`, `"refusal"`, 1) + terminal, "unknown", "refusal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}, false, "http", fixtureCheck)
			require.Equal(t, tc.status, r.Status)
			require.Equal(t, tc.reason, r.Reason)
			if tc.name == "commentary" {
				require.Equal(t, "commentary", r.Diagnostic.IgnoredTypes)
			}
			if r.Status == "smart" {
				require.Equal(t, "completed_items", r.Diagnostic.AnswerSource)
				require.Equal(t, 1, r.Diagnostic.DoneMessages)
			}
		})
	}
}

func TestIQRecoveryRetryPolicy(t *testing.T) {
	for _, reason := range []string{"timeout", "request_failed", "response_read_failed", "http_502", "http_503", "http_504", "missing_final_message", "empty_final_text", "incomplete_response", "zero_byte_response"} {
		require.True(t, Retryable(Unknown(reason)), reason)
	}
	for _, reason := range []string{"quota_exhausted", "http_403", "http_429", "rate_limited", "wrong_answer", "refusal"} {
		require.False(t, Retryable(Unknown(reason)), reason)
	}
	require.False(t, Retryable(Grade("29", fixtureCheck)))
	r := Unknown("timeout")
	r.Diagnostic = &Diagnostic{Transport: "plugin"}
	require.False(t, Retryable(r))
}

func TestIQCompletedItemIdentityAndPartialTerminal(t *testing.T) {
	item := `{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"21"}]}`
	done := `data: {"type":"response.output_item.done","output_index":1,"item":` + item + "}\n\n"
	for _, tc := range []struct{ name, output, status string }{
		{"same envelope", item, "smart"},
		{"missing item with reasoning", `{"id":"reason_1","type":"reasoning"}`, "smart"},
		{"conflicting role", strings.Replace(item, `"assistant"`, `"user"`, 1), "unknown"},
		{"conflicting phase", strings.Replace(item, `"final_answer"`, `"commentary"`, 1), "unknown"},
		{"conflicting type", strings.Replace(item, `"message"`, `"reasoning"`, 1), "unknown"},
		{"conflicting value", strings.Replace(item, `"21"`, `"29"`, 1), "unknown"},
		{"independent final", strings.Replace(item, `"msg_1"`, `"msg_2"`, 1), "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := done + `data: {"type":"response.completed","response":{"status":"completed","output":[` + tc.output + "]}}\n\n"
			r := parseInput(strings.NewReader(body), true, false, fixtureCheck)
			require.Equal(t, tc.status, r.Status, r.Reason)
		})
	}
	for _, body := range []string{strings.Replace(done, `"output_index":1`, `"output_index":1,"OUTPUT_INDEX":2`, 1), strings.Replace(done, `"id":"msg_1",`, "", 1)} {
		r := parseInput(strings.NewReader(body+`data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n"), true, false, fixtureCheck)
		require.Equal(t, "unknown", r.Status)
	}
}

// relayPlaceholderStream mirrors a captured relay stream: whitespace-only
// assistant messages without phase precede the upstream reasoning and final
// answer, both while streaming and again in response.completed.
func relayPlaceholderStream(placeholders int, answer string) string {
	var b strings.Builder
	var output []string
	emit := func(kind, fields string) {
		_, _ = fmt.Fprintf(&b, "event: %s\ndata: {\"type\":%q,%s}\n\n", kind, kind, fields)
	}
	done := func(index int, item string) {
		emit("response.output_item.done", fmt.Sprintf(`"output_index":%d,"item":%s`, index, item))
		output = append(output, item)
	}
	emit("response.created", `"response":{"id":"resp_ft_1","status":"in_progress","output":[]}`)
	for i := 0; i < placeholders; i++ {
		emit("response.output_text.delta", fmt.Sprintf(`"output_index":%d,"delta":" "`, i))
		done(i, fmt.Sprintf(`{"id":"msg_ft_%d","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","annotations":[],"text":" "}]}`, i))
	}
	done(placeholders, `{"id":"rs_1","type":"reasoning","summary":[]}`)
	emit("response.metadata", `"metadata":{"openai_chatgpt_moderation_metadata":{}}`)
	done(placeholders+1, `{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","annotations":[],"text":`+strconv.Quote(answer)+`}]}`)
	emit("response.completed", `"response":{"id":"resp_ft_1","status":"completed","model":"gpt-6-astra","output":[`+strings.Join(output, ",")+`]}`)
	return b.String()
}

func TestIQRelayBlankPlaceholderMessages(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		placeholders, done, output int
	}{
		{"one placeholder", 1, 2, 3},
		{"two placeholders", 2, 3, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := relayPlaceholderStream(tc.placeholders, `{"answer":21}`)
			r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, false, "http", fixtureCheck)
			require.Equal(t, "smart", r.Status, r.Reason)
			require.Equal(t, `{"answer":21}`, r.Answer)
			require.Equal(t, tc.done, r.Diagnostic.DoneMessages)
			require.Equal(t, tc.output, r.Diagnostic.TerminalItems)
			require.Equal(t, "reasoning/blank", r.Diagnostic.IgnoredTypes)
		})
	}
	// Only whitespace is exempt; a phase-less message with text still competes.
	textual := strings.ReplaceAll(relayPlaceholderStream(1, `{"answer":21}`), `"text":" "`, `"text":"29"`)
	require.Equal(t, "conflicting_final_response", parseInput(strings.NewReader(textual), true, false, fixtureCheck).Reason)
	require.Equal(t, "empty_final_text", parseInput(strings.NewReader(relayPlaceholderStream(1, " ")), true, false, fixtureCheck).Reason)
}
