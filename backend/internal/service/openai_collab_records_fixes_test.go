//go:build unit

package service

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func collabMultilineFrame(t *testing.T) string {
	frame := string(collabCallFrame(t, `{"message":"cipher"}`, ""))
	return "data: " + frame[:1] + "\n" + strings.Repeat("data: "+strings.Repeat(" ", 60)+"\n", 12000) + "data: " + frame[1:] + "\n\n"
}
func TestCollabFixLinearFrameAndNoIrrelevantParsing(t *testing.T) {
	s := collabRecordTestService(t)
	payload := collabMultilineFrame(t)
	e := resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1, Sequence: 1, EventType: "response.output_item.done", Executable: true, Payload: payload}
	c := s.NewCollabRecordDelivery(collabRecordTestInput())
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	c.AboutToWrite(context.Background(), e)
	runtime.ReadMemStats(&after)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(16<<20), "SSE assembly must be linear")
	runtime.ReadMemStats(&before)
	c.Written(context.Background(), e)
	runtime.ReadMemStats(&after)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(8<<20), "Written must not parse the frame again")
	e.EventType = "response.output_text.delta"
	allocations := collabMinAllocsPerRun(2, func() { c.AboutToWrite(context.Background(), e) })
	require.Zero(t, allocations, "metadata rules out calls before any parse")
}

func TestCollabFixNoticeEndsAtEveryModelOutput(t *testing.T) {
	s := collabRecordTestService(t)
	notice := map[string]any{"type": "agent_message", "content": []any{map[string]string{"type": "input_text", "text": "Message Type: FINAL_ANSWER\nTask name: task\nSender: child\nPayload:\nGateway notice [event]. unreadable"}}}
	for _, kind := range []string{"web_search_call", "local_shell_call", "computer_call", "image_generation_call", "future_call", "message", ""} {
		t.Run(kind, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"input": []any{notice, map[string]string{"type": kind}}})
			found := s.ParseCollabSeenNotices(body, map[string]resilience.CollabDigest{"child": "thread"})
			require.Len(t, found, 1)
			require.False(t, found[0].Current)
		})
	}
	for _, tail := range []string{`{"type":"message","role":"user","role":"assistant"}`, `{"type":"message","role":"user","content":null}`, `null`} {
		first, _ := json.Marshal(notice)
		body := []byte(`{"input":[` + string(first) + "," + tail + "]}")
		found := s.ParseCollabSeenNotices(body, nil)
		require.Len(t, found, 1)
		require.False(t, found[0].Current)
	}
}

func TestCollabFixNoticeInputAllowlist(t *testing.T) {
	s := collabRecordTestService(t)
	notice := `{"type":"agent_message","content":[{"type":"input_text","text":"Message Type: FINAL_ANSWER\nTask name: task\nSender: child\nPayload:\nGateway notice [event]. unreadable"}]}`
	for _, tail := range []string{
		`{"type":"message","role":"user","content":"input"}`,
		`{"role":"developer","content":[{"type":"input_text","text":"input"}]}`,
		`{"type":"function_call_output","call_id":"call","output":"result"}`,
		`{"type":"custom_tool_call_output","call_id":"call","output":[{"type":"input_text","text":"result"}]}`,
	} {
		found := s.ParseCollabSeenNotices([]byte(`{"input":[`+notice+","+tail+"]}"), nil)
		require.Len(t, found, 1)
		require.True(t, found[0].Current)
	}
	for _, tail := range []string{
		`{"role":"user","content":[{"type":"output_text","text":"ambiguous"}]}`,
		`{"type":"agent_message","content":[null]}`,
		`{"type":"function_call_output","call_id":"call","output":null}`,
	} {
		found := s.ParseCollabSeenNotices([]byte(`{"input":[`+notice+","+tail+"]}"), nil)
		require.Len(t, found, 1)
		require.False(t, found[0].Current)
	}
}

func TestCollabFixAmbiguousHistoryNeverBorrows(t *testing.T) {
	s := collabRecordTestService(t)
	notice := `{"type":"agent_message","content":[{"type":"input_text","text":"Message Type: FINAL_ANSWER\nTask name: task\nSender: child\nPayload:\nGateway notice [event]. unreadable"}]}`
	for _, head := range []string{`null`, `{"type":42}`, `{"type":"message","role":"user","role":"assistant"}`} {
		found := s.ParseCollabSeenNotices([]byte(`{"input":[`+head+","+notice+"]}"), nil)
		require.Len(t, found, 1)
		require.False(t, found[0].Current)
	}
}

func BenchmarkCollabFixMultilineDelivery(b *testing.B) {
	s := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "synthetic-test-secret"}}}
	s.initCollabRecords()
	defer s.CloseCollabRecords()
	payload := "data: {\n" + strings.Repeat("data: "+strings.Repeat(" ", 60)+"\n", 12000) + `data: "type":"response.output_item.done","item":{"type":"function_call","namespace":"collaboration","name":"spawn_agent","call_id":"call","arguments":"{\"message\":\"cipher\"}"}}` + "\n\n"
	e := resilience.CollabDeliveryEvent{Generation: 1, SendSeq: 1, Sequence: 1, EventType: "response.output_item.done", Executable: true, Payload: payload}
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for b.Loop() {
		c := s.NewCollabRecordDelivery(collabRecordTestInput())
		c.AboutToWrite(context.Background(), e)
		c.Written(context.Background(), e)
	}
}
