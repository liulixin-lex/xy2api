//go:build unit

package service

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// probeSSE 把事件拼成 SSE 文本：每个元素是一个完整事件（可含 event: 行），事件间空行分隔。
func probeSSE(events ...string) string {
	return strings.Join(events, "\n\n") + "\n\n"
}

func TestReadProbeStream_Chat(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stream    string
		answered  bool
		reasoning bool
		text      string
		streamErr string
	}{
		{
			name: "heartbeats and role-only delta before the answer",
			stream: probeSSE(": keepalive", ":", "event: ping\ndata: {}",
				`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
				`data: {"choices":[{"delta":{"content":"4"}}]}`,
				`data: {"choices":[{"delta":{"content":"0"}}]}`,
				`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`data: {"choices":[],"usage":{"completion_tokens":2}}`,
				"data: [DONE]"),
			answered: true, text: "40",
		},
		{
			name: "reasoning before the answer",
			stream: probeSSE(`data: {"choices":[{"delta":{"reasoning_content":"23+17=40"}}]}`,
				`data: {"choices":[{"delta":{"reasoning":"so 40"}}]}`,
				`data: {"choices":[{"delta":{"content":"40"},"finish_reason":"stop"}]}`, "data: [DONE]"),
			answered: true, reasoning: true, text: "40",
		},
		{
			name: "reasoning only, no text",
			stream: probeSSE(`data: {"choices":[{"delta":{"reasoning_content":"the sum is 40"}}]}`,
				`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`, "data: [DONE]"),
			reasoning: true,
		},
		{
			name: "think tags in the text",
			stream: probeSSE(`data: {"choices":[{"delta":{"content":"<think>"}}]}`,
				`data: {"choices":[{"delta":{"content":"23+17 is 40"}}]}`,
				`data: {"choices":[{"delta":{"content":"</think>"}}]}`,
				`data: {"choices":[{"delta":{"content":"\n41"}}]}`, "data: [DONE]"),
			text: "41",
		},
		{
			name: "error followed by DONE is no success",
			stream: probeSSE(`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
				`data: {"error":{"message":"upstream overloaded","type":"server_error"}}`, "data: [DONE]"),
			streamErr: "upstream overloaded",
		},
		{
			name:      "error event",
			stream:    probeSSE("event: error\ndata: {\"error\":{\"message\":\"boom\"}}"),
			streamErr: "boom",
		},
		{
			name:   "wrong answer",
			stream: probeSSE(`data: {"choices":[{"delta":{"content":"41"}}]}`, "data: [DONE]"),
			text:   "41",
		},
		{name: "empty stream", stream: probeSSE("data: [DONE]")},
		{name: "no events at all", stream: ""},
		{
			name:     "CRLF line endings and no space after colon",
			stream:   "data:{\"choices\":[{\"delta\":{\"content\":\"40\"}}]}\r\n\r\ndata:[DONE]\r\n\r\n",
			answered: true, text: "40",
		},
		{
			name:     "JSON split across data lines",
			stream:   "data: {\"choices\":[{\"delta\":\ndata: {\"content\":\"40 \"}}]}\n\n",
			answered: true, text: "40",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := readProbeStream(strings.NewReader(tc.stream), probeProtocolChat, "40")
			require.NoError(t, got.ReadErr)
			require.Equal(t, tc.answered, got.Answered)
			require.Equal(t, tc.reasoning, got.Reasoning)
			require.Equal(t, tc.text, got.Text)
			if tc.streamErr == "" {
				require.Empty(t, got.StreamErr)
				if !tc.answered {
					require.True(t, got.Ended)
				}
			} else {
				require.Contains(t, got.StreamErr, tc.streamErr)
				require.False(t, got.Answered)
			}
		})
	}
}

func TestReadProbeStream_Responses(t *testing.T) {
	ev := func(typ, data string) string { return "event: " + typ + "\ndata: " + data }
	for _, tc := range []struct {
		name      string
		stream    string
		answered  bool
		reasoning bool
		streamErr string
		stop      string
	}{
		{
			name: "reasoning summary then text",
			stream: probeSSE(
				ev("response.created", `{"type":"response.created","response":{"status":"in_progress"}}`),
				ev("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"adding 40"}`),
				ev("response.output_text.delta", `{"type":"response.output_text.delta","delta":"40"}`),
				ev("response.output_text.done", `{"type":"response.output_text.done","text":"40"}`)),
			answered: true, reasoning: true,
		},
		{
			name: "only the completed event carries the text",
			stream: probeSSE(ev("response.completed", `{"type":"response.completed","response":{"status":"completed",`+
				`"output":[{"type":"reasoning"},{"type":"message","content":[{"type":"output_text","text":"40"}]}]}}`)),
			answered: true, reasoning: true,
		},
		{
			name: "response.failed",
			stream: probeSSE(ev("response.failed", `{"type":"response.failed","response":{"status":"failed",`+
				`"error":{"code":"server_error","message":"The model failed"}}}`)),
			streamErr: "The model failed",
		},
		{
			name:      "error event",
			stream:    probeSSE(ev("error", `{"type":"error","code":"rate_limit_exceeded","message":"Rate limit reached"}`)),
			streamErr: "Rate limit reached",
		},
		{
			name: "incomplete with reasoning only",
			stream: probeSSE(
				ev("response.reasoning_text.delta", `{"type":"response.reasoning_text.delta","delta":"hmm"}`),
				ev("response.incomplete", `{"type":"response.incomplete","response":{"status":"incomplete",`+
					`"incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"reasoning"}]}}`)),
			reasoning: true, stop: "max_output_tokens",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := readProbeStream(strings.NewReader(tc.stream), probeProtocolResponses, "40")
			require.NoError(t, got.ReadErr)
			require.Equal(t, tc.answered, got.Answered, "text=%q", got.Text)
			require.Equal(t, tc.reasoning, got.Reasoning)
			require.Equal(t, tc.stop, got.Stop)
			if tc.streamErr != "" {
				require.Contains(t, got.StreamErr, tc.streamErr)
			} else {
				require.Empty(t, got.StreamErr)
			}
		})
	}
}

func TestReadProbeStream_Anthropic(t *testing.T) {
	ev := func(typ, data string) string { return "event: " + typ + "\ndata: " + data }
	normal := probeSSE(
		ev("message_start", `{"type":"message_start","message":{"id":"m","content":[]}}`),
		ev("ping", `{"type": "ping"}`),
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"23+17=40"}}`),
		ev("content_block_stop", `{"type":"content_block_stop","index":0}`),
		ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"40"}}`),
		ev("content_block_stop", `{"type":"content_block_stop","index":1}`),
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`),
		ev("message_stop", `{"type":"message_stop"}`))
	got := readProbeStream(strings.NewReader(normal), probeProtocolAnthropic, "40")
	require.True(t, got.Answered)
	require.True(t, got.Reasoning)

	thinkingOnly := probeSSE(
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"40"}}`),
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`),
		ev("message_stop", `{"type":"message_stop"}`))
	got = readProbeStream(strings.NewReader(thinkingOnly), probeProtocolAnthropic, "40")
	require.False(t, got.Answered)
	require.True(t, got.Reasoning)
	require.True(t, got.Ended)
	require.Empty(t, got.Text)
	require.Equal(t, "max_tokens", got.Stop)

	failed := probeSSE(ev("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	got = readProbeStream(strings.NewReader(failed), probeProtocolAnthropic, "40")
	require.False(t, got.Answered)
	require.Contains(t, got.StreamErr, "Overloaded")
}

func TestReadProbeStream_Gemini(t *testing.T) {
	normal := probeSSE(
		`data: {"candidates":[{"content":{"parts":[{"text":"40 maybe","thought":true}],"role":"model"}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"4"}],"role":"model"}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"0"}],"role":"model"},"finishReason":"STOP"}]}`)
	got := readProbeStream(strings.NewReader(normal), probeProtocolGemini, "40")
	require.True(t, got.Answered)
	require.True(t, got.Reasoning)
	require.Equal(t, "40", got.Text)

	failed := probeSSE(`data: {"error":{"code":503,"message":"The model is overloaded.","status":"UNAVAILABLE"}}`)
	got = readProbeStream(strings.NewReader(failed), probeProtocolGemini, "40")
	require.False(t, got.Answered)
	require.Contains(t, got.StreamErr, "The model is overloaded.")

	blocked := probeSSE(`data: {"promptFeedback":{"blockReason":"SAFETY"}}`)
	got = readProbeStream(strings.NewReader(blocked), probeProtocolGemini, "40")
	require.Contains(t, got.StreamErr, "SAFETY")

	thoughtOnly := probeSSE(`data: {"candidates":[{"content":{"parts":[{"text":"40","thought":true}]},"finishReason":"MAX_TOKENS"}]}`)
	got = readProbeStream(strings.NewReader(thoughtOnly), probeProtocolGemini, "40")
	require.False(t, got.Answered)
	require.True(t, got.Reasoning)
	require.Equal(t, "MAX_TOKENS", got.Stop)
}

// TestReadProbeStream_StopsAtAnswer 读到答案即返回，不等流结束。
func TestReadProbeStream_StopsAtAnswer(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	go func() {
		_, _ = io.WriteString(pw, probeSSE(`data: {"choices":[{"delta":{"content":"40"}}]}`,
			`data: {"choices":[{"delta":{"content":" because"}}]}`))
		// 之后永远不再写，也不关闭：读取方必须已经返回。
	}()
	done := make(chan probeReading, 1)
	go func() { done <- readProbeStream(pr, probeProtocolChat, "40") }()
	select {
	case got := <-done:
		require.True(t, got.Answered)
		require.False(t, got.Ended)
	case <-time.After(2 * time.Second):
		t.Fatal("reader kept waiting after the answer arrived")
	}
}

func TestReadProbeStream_ReplaceModeNeedsTextAtEnd(t *testing.T) {
	got := readProbeStream(strings.NewReader(probeSSE(`data: {"choices":[{"delta":{"content":"ok"}}]}`, "data: [DONE]")), probeProtocolChat, "")
	require.True(t, got.Answered)
	got = readProbeStream(strings.NewReader(probeSSE("data: [DONE]")), probeProtocolChat, "")
	require.False(t, got.Answered)
	require.True(t, got.Ended)
}

func TestReadProbeStream_ReadErrorIsReported(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = io.WriteString(pw, probeSSE(`data: {"choices":[{"delta":{"content":"4"}}]}`))
		_ = pw.CloseWithError(io.ErrUnexpectedEOF)
	}()
	got := readProbeStream(pr, probeProtocolChat, "40")
	require.ErrorIs(t, got.ReadErr, io.ErrUnexpectedEOF)
	require.False(t, got.Answered)
	require.False(t, got.Ended)
}

func TestReadProbeJSON(t *testing.T) {
	for _, tc := range []struct {
		name      string
		protocol  probeProtocol
		body      string
		answered  bool
		reasoning bool
		streamErr string
		text      string
	}{
		{"chat", probeProtocolChat, `{"choices":[{"message":{"content":"40","reasoning_content":"x"},"finish_reason":"stop"}]}`, true, true, "", "40"},
		{"chat think tags", probeProtocolChat, `{"choices":[{"message":{"content":"<think>40</think>41"}}]}`, false, false, "", "41"},
		{"chat error on 200", probeProtocolChat, `{"error":{"message":"bad things"}}`, false, false, "bad things", ""},
		{"responses leading reasoning", probeProtocolResponses, `{"output":[{"type":"reasoning"},{"type":"message","content":[{"type":"output_text","text":"40"}]}]}`, true, true, "", "40"},
		{"responses failed", probeProtocolResponses, `{"status":"failed","error":{"message":"nope"}}`, false, false, "nope", ""},
		{"anthropic thinking excluded", probeProtocolAnthropic, `{"content":[{"type":"thinking","thinking":"40"},{"type":"text","text":"41"}]}`, false, true, "", "41"},
		{"anthropic error", probeProtocolAnthropic, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, false, false, "Overloaded", ""},
		{"gemini thought excluded", probeProtocolGemini, `{"candidates":[{"content":{"parts":[{"text":"41","thought":true},{"text":"40"}]}}]}`, true, true, "", "40"},
		{"gemini array stream", probeProtocolGemini, `[{"candidates":[{"content":{"parts":[{"text":"4"}]}}]},{"candidates":[{"content":{"parts":[{"text":"0"}]}}]}]`, true, false, "", "40"},
		{"gemini error", probeProtocolGemini, `{"error":{"code":429,"message":"quota"}}`, false, false, "quota", ""},
		{"not JSON", probeProtocolChat, `<html>oops</html>`, false, false, "oops", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := readProbeJSON([]byte(tc.body), tc.protocol, "40")
			require.Equal(t, tc.answered, got.Answered)
			require.Equal(t, tc.reasoning, got.Reasoning)
			require.Equal(t, tc.text, got.Text)
			require.True(t, got.Ended)
			if tc.streamErr != "" {
				require.Contains(t, got.StreamErr, tc.streamErr)
			} else {
				require.Empty(t, got.StreamErr)
			}
		})
	}
}

// MiniMax-style streams repeat the whole message in the last chunk; it must not
// be appended to the deltas already received ("12" + "12" would read as 1212).
func TestReadProbeStream_ChatFinalMessageDoesNotRepeatDeltas(t *testing.T) {
	stream := probeSSE(
		`data: {"choices":[{"delta":{"content":"1"}}]}`,
		`data: {"choices":[{"delta":{"content":"2"}}]}`,
		`data: {"choices":[{"message":{"role":"assistant","content":"12"},"finish_reason":"stop"}]}`,
		"data: [DONE]")
	got := readProbeStream(strings.NewReader(stream), probeProtocolChat, "12")
	require.True(t, got.Answered, got.Text)
	require.Equal(t, "12", got.Text)

	// A stream whose only content arrives as message still counts.
	onlyMessage := probeSSE(
		`data: {"choices":[{"message":{"role":"assistant","content":"12"},"finish_reason":"stop"}]}`,
		"data: [DONE]")
	got = readProbeStream(strings.NewReader(onlyMessage), probeProtocolChat, "12")
	require.True(t, got.Answered, got.Text)
}
