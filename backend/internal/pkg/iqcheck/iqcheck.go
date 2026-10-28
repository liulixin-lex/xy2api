// Package iqcheck implements parameterized candy checks and sequential rounds.
package iqcheck

import "github.com/liulixin-lex/xy2api/internal/pkg/openai"

const PromptVersion = "candy-v3"
const GraderVersion = "candy-grader-v3"
const OutputInstruction = `请解答用户的题目。输出格式统一为一个简洁 JSON 对象，唯一字段为 answer，值为最终整数答案。不要输出解释、Markdown 或其他字段。`

// Reasoning and completed envelopes may repeat large payloads. Bound the wire
// stream separately from retained assistant text, which stays capped at 64 KiB.
const MaxResponseBytes = 8 * 1024 * 1024
const MaxAnswerBytes = 64 * 1024

type Check struct {
	Expected, Naive, Sent string
	PreferHeader          bool
	Rules                 []openai.DowngradeRule
	declaration           *openai.DeclarationResult
}

type Result struct {
	StateFingerprint string `json:"-"`
	StateRevision    string `json:"-"`
	StateTicketID    string `json:"-"`
	StateModel       string `json:"-"`

	Diagnostic       *Diagnostic       `json:"diagnostic,omitempty"`
	Status           string            `json:"status"`
	Answer           string            `json:"answer"`
	Reason           string            `json:"reason,omitempty"`
	NormalizedAnswer *string           `json:"normalized_answer,omitempty"`
	AnswerFormat     string            `json:"answer_format,omitempty"`
	FormatCompliant  bool              `json:"format_compliant"`
	ReportedModel    string            `json:"reported_model,omitempty"`
	ModelClass       openai.ModelClass `json:"model_class,omitempty"`
}

func Unknown(reason string) Result { return Result{Status: "unknown", Reason: reason} }

func Grade(answer string, c Check) Result {
	if len(answer) > MaxAnswerBytes {
		return Unknown("answer_too_large")
	}
	extracted := ExtractAnswer(answer)
	r := Result{Status: "unknown", Answer: answer, Reason: extracted.Reason, AnswerFormat: extracted.Format, FormatCompliant: extracted.Compliant}
	if extracted.Value != "" {
		r.NormalizedAnswer = &extracted.Value
		r.Status, r.Reason = "degraded", "wrong_answer"
		switch extracted.Value {
		case c.Expected:
			r.Status, r.Reason = "smart", "correct_answer"
		case c.Naive:
			r.Diagnostic = &Diagnostic{NaiveAnswer: true}
		}
	}
	if d := c.declaration; d != nil {
		r.ReportedModel, r.ModelClass = d.ReportedModel, d.Class
		if d.Wrong {
			r.Status, r.Reason = "degraded", "model_mismatch"
		} else if d.Unknown {
			r.Status, r.Reason = "unknown", "model_conflict"
		}
	}
	return r
}

func Payload(chat bool, model, effort string, c Challenge) map[string]any {
	p := map[string]any{"model": model, "store": false, "stream": true}
	if chat {
		p["messages"] = []map[string]string{{"role": "system", "content": OutputInstruction}, {"role": "user", "content": c.Prompt()}}
		if effort != "upstream_default" {
			p["reasoning_effort"] = effort
		}
	} else {
		p["instructions"] = OutputInstruction
		p["input"] = []map[string]any{{"role": "user", "content": []map[string]string{{"type": "input_text", "text": c.Prompt()}}}}
		if effort != "upstream_default" {
			p["reasoning"] = map[string]string{"effort": effort}
		}
	}
	return p
}
