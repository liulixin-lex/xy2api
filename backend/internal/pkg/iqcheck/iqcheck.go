// Package iqcheck implements the single candy-question check, not a model grader.
package iqcheck

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/domain"
)

const Model = domain.DefaultIQModel
const Effort = domain.DefaultIQEffort
const PromptVersion = "candy-v2"
const GraderVersion = "candy-grader-v2"
const OutputInstruction = `请解答用户的题目。输出格式统一为一个简洁 JSON 对象，唯一字段为 answer，值为最终整数答案。不要输出解释、Markdown 或其他字段。`
const MaxResponseBytes = 256 * 1024
const MaxAnswerBytes = 64 * 1024
const Prompt = `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）
苹果味 桃子味 西瓜味
圆形 7 9 8
五角星形 7 6 4
只输出一个 JSON 对象，仅含整数 answer，不输出解释或 Markdown。`

type Result struct {
	Status           string  `json:"status"`
	Answer           string  `json:"answer"`
	Reason           string  `json:"reason,omitempty"`
	NormalizedAnswer *string `json:"normalized_answer,omitempty"`
	AnswerFormat     string  `json:"answer_format,omitempty"`
	FormatCompliant  bool    `json:"format_compliant"`
	ReportedModel    string  `json:"reported_model,omitempty"`
}

func Unknown(reason string) Result { return Result{Status: "unknown", Reason: reason} }

func Grade(answer string) Result {
	if len(answer) > MaxAnswerBytes {
		return Unknown("response_too_large")
	}
	extracted := ExtractAnswer(answer)
	r := Result{Status: "unknown", Answer: answer, Reason: extracted.Reason, AnswerFormat: extracted.Format, FormatCompliant: extracted.Compliant}
	if extracted.Value != "" {
		r.NormalizedAnswer = &extracted.Value
		r.Status, r.Reason = "degraded", "wrong_answer"
		if extracted.Value == "21" {
			r.Status, r.Reason = "smart", "correct_answer"
		}
	}
	return r
}

func Payload(chat bool, profiles ...domain.IQProfile) map[string]any {
	profile := (domain.IQProfile{}).Defaults()
	if len(profiles) != 0 {
		profile = profiles[0].Defaults()
	}
	p := map[string]any{"model": profile.Model, "store": false, "stream": true}
	if chat {
		p["messages"] = []map[string]string{{"role": "system", "content": OutputInstruction}, {"role": "user", "content": Prompt}}
		if profile.ReasoningEffort != "upstream_default" {
			p["reasoning_effort"] = profile.ReasoningEffort
		}
	} else {
		p["instructions"] = OutputInstruction
		p["input"] = []map[string]any{{"role": "user", "content": []map[string]string{{"type": "input_text", "text": Prompt}}}}
		if profile.ReasoningEffort != "upstream_default" {
			p["reasoning"] = map[string]string{"effort": profile.ReasoningEffort}
		}
	}
	if profile.OutputMode == "strict" {
		schema := map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]string{"type": "integer"}}, "required": []string{"answer"}, "additionalProperties": false}
		format := map[string]any{"name": "candy_answer", "strict": true, "schema": schema}
		if chat {
			p["response_format"] = map[string]any{"type": "json_schema", "json_schema": format}
		} else {
			format["type"] = "json_schema"
			p["text"] = map[string]any{"format": format}
		}
	}
	return p
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type message struct {
	Phase   string    `json:"phase"`
	Role    string    `json:"role"`
	Type    string    `json:"type"`
	Status  string    `json:"status"`
	Content []content `json:"content"`
}
type response struct {
	Type     string          `json:"type"`
	Model    string          `json:"model"`
	Status   string          `json:"status"`
	Output   []message       `json:"output"`
	Response *response       `json:"response"`
	Error    json.RawMessage `json:"error"`
	Choices  []struct {
		Index        int     `json:"index"`
		FinishReason *string `json:"finish_reason"`
		Delta        struct {
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		} `json:"delta"`
		Message struct {
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		} `json:"message"`
	} `json:"choices"`
}

func responseAnswer(r response) (string, error) {
	if r.Status != "completed" {
		return "", errors.New("incomplete_response")
	}
	var answer strings.Builder
	for _, m := range r.Output {
		if m.Type != "message" || m.Role != "assistant" || (m.Phase != "" && m.Phase != "final_answer") {
			continue
		}
		if m.Status != "" && m.Status != "completed" {
			return "", errors.New("incomplete_response")
		}
		for _, c := range m.Content {
			if c.Type == "refusal" {
				return "", errors.New("refusal")
			}
			if c.Type == "output_text" {
				_, _ = answer.WriteString(c.Text)
			}
		}
	}
	return answer.String(), nil
}

// Parse requires a successful terminal event; deltas alone never count as an answer.
func Parse(body io.Reader, sse, chat bool) Result {
	raw, err := io.ReadAll(io.LimitReader(body, MaxResponseBytes+1))
	if err != nil {
		return Unknown("response_read_failed")
	}
	if len(raw) > MaxResponseBytes {
		return Unknown("response_too_large")
	}
	var answer strings.Builder
	completed := false
	reportedModel := ""
	consume := func(data []byte) error {
		var r response
		if _, err := uniqueJSON(data); err != nil {
			return errors.New("invalid_response")
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return errors.New("invalid_response")
		}
		if r.Model != "" {
			reportedModel = r.Model
		}
		if len(r.Error) > 0 && string(r.Error) != "null" {
			return errors.New("upstream_error")
		}
		if chat {
			for _, c := range r.Choices {
				if c.Delta.Refusal != "" || c.Message.Refusal != "" {
					return errors.New("refusal")
				}
				if c.Index != 0 {
					continue
				}
				if completed && (c.Delta.Content != "" || c.Message.Content != "") {
					return errors.New("invalid_response")
				}
				if sse {
					_, _ = answer.WriteString(c.Delta.Content)
				} else {
					_, _ = answer.WriteString(c.Message.Content)
				}
				if c.FinishReason != nil {
					if *c.FinishReason != "stop" {
						return errors.New("incomplete_response")
					}
					completed = true
				}
			}
		} else {
			if r.Type == "response.failed" || r.Type == "response.incomplete" || r.Type == "error" {
				return errors.New("incomplete_response")
			}
			if !sse || r.Type == "response.completed" || r.Type == "response.done" {
				if r.Response != nil {
					r = *r.Response
				}
				if r.Model != "" {
					reportedModel = r.Model
				}
				text, err := responseAnswer(r)
				if err != nil {
					return err
				}
				if completed && text != answer.String() {
					return errors.New("conflicting_final_response")
				}
				answer.Reset()
				_, _ = answer.WriteString(text)
				completed = true
			}
		}
		return nil
	}
	if !sse {
		if err := consume(raw); err != nil {
			return Unknown(err.Error())
		}
	} else {
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 4096), MaxResponseBytes+1)
		var data []string
		flush := func() error {
			if len(data) == 0 {
				return nil
			}
			joined := strings.Join(data, "\n")
			data = nil
			if joined == "[DONE]" {
				return nil
			}
			return consume([]byte(joined))
		}
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if err := flush(); err != nil {
					return Unknown(err.Error())
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if scanner.Err() != nil {
			return Unknown("invalid_response")
		}
		if err := flush(); err != nil {
			return Unknown(err.Error())
		}
	}
	if !completed {
		return Unknown("incomplete_response")
	}
	if len(reportedModel) > 256 {
		return Unknown("response_too_large")
	}
	result := Grade(answer.String())
	result.ReportedModel = reportedModel
	return result
}
