package iqcheck

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGrade(t *testing.T) {
	for _, tc := range []struct{ answer, status string }{
		{"21", "smart"}, {" \n21\n", "smart"},
		{`{"answer":21}`, "smart"}, {`{"answer":"21"}`, "smart"},
		{"```json\n{\"answer\":21}\n```", "smart"},
		{`{"answer":29,"explanation":"答案曾考虑21"}`, "degraded"},
		{"21个糖果，因为可以按形状选择。", "smart"},
		{"21。解释如下。", "smart"},
		{"答案是21。因为可以选择9个圆形和12个五角星形。", "smart"},
		{"最少需要取出 **21** 个糖果。下面解释：9+12=21。", "smart"},
		{"因此，需要取出21个糖果才能保证成功。", "smart"},
		{"The answer is **21**. Here is the reasoning.", "smart"},
		{"经过分析，最终结果为 $\\boxed{21}$。", "smart"},
		{"答案不是21", "degraded"}, {"答案是21或29", "degraded"}, {"答案是21.5", "degraded"}, {"答案可能是21", "degraded"}, {"29", "degraded"}, {"曾考虑21个，但最终答案是29。", "degraded"},
		{"答案是21。更正：答案是29。", "degraded"},
		{"21只是中间计算值，无法确定答案。", "degraded"},
		{"", "unknown"}, {"  ", "unknown"},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			if got := Grade(tc.answer); got.Status != tc.status {
				t.Fatalf("%q: got %s, want %s", tc.answer, got.Status, tc.status)
			}
		})
	}
}

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		sse, chat  bool
		status     string
	}{
		{"json", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"答案是21。"}]}]}`, false, false, "smart"},
		{"reasoning excluded", `{"status":"completed","output":[{"type":"reasoning","content":[{"type":"output_text","text":"21"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"29"}]}]}`, false, false, "degraded"},
		{"incomplete", `{"status":"incomplete","output":[]}`, false, false, "unknown"},
		{"delta only", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"21\"}\n\n", true, false, "unknown"},
		{"chat", `{"choices":[{"index":0,"message":{"content":"21"},"finish_reason":"stop"}]}`, false, true, "smart"},
		{"chat truncated", `{"choices":[{"index":0,"message":{"content":"21"},"finish_reason":"length"}]}`, false, true, "unknown"},
		{"chat stream", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"21\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", true, true, "smart"},
		{"invalid", `{`, false, false, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(strings.NewReader(tc.body), tc.sse, tc.chat); got.Status != tc.status {
				t.Fatalf("%+v", got)
			}
		})
	}
	if got := Parse(strings.NewReader(strings.Repeat("x", MaxResponseBytes+1)), false, false); got.Reason != "response_too_large" {
		t.Fatal(got)
	}
}

func TestPayloadFresh(t *testing.T) {
	for _, chat := range []bool{false, true} {
		p := Payload(chat)
		if p["model"] != Model || p["store"] != false {
			t.Fatal(p)
		}
		for _, key := range []string{"conversation", "previous_response_id", "session_id"} {
			if _, ok := p[key]; ok {
				t.Fatal(key)
			}
		}
		b, _ := json.Marshal(p)
		if strings.Count(string(b), "只需要输出最终数字结果") != 1 {
			t.Fatal(string(b))
		}
	}
}

func TestCompletedSSE(t *testing.T) {
	terminal := `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"29"}]},{"type":"message","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"21"}]}]}}`
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"21\"}\n\ndata: " + terminal + "\n\ndata: " + terminal + "\n\n"
	got := Parse(strings.NewReader(body), true, false)
	if got.Status != "smart" || got.Answer != "21" {
		t.Fatal(got)
	}
	got = Parse(strings.NewReader(body+"data: {\"type\":\"error\",\"error\":{\"message\":\"interrupted\"}}\n\n"), true, false)
	if got.Status != "unknown" {
		t.Fatal(got)
	}
	got = Parse(strings.NewReader("data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n"), true, false)
	if got.Status != "unknown" {
		t.Fatal(got)
	}
}
