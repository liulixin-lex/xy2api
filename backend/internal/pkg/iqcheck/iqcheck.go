// Package iqcheck implements the single candy-question check, not a model grader.
package iqcheck

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

const Model = "gpt-6-astra"
const Effort = "low"
const PromptVersion = "candy-v1"
const OutputInstruction = `请解答用户的题目。输出格式统一为一个简洁 JSON 对象，唯一字段为 answer，值为最终整数答案。不要输出解释、Markdown 或其他字段。`
const MaxResponseBytes = 256 * 1024
const MaxAnswerBytes = 64 * 1024
const Prompt = `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）
苹果味 桃子味 西瓜味
圆形 7 9 8
五角星形 7 6 4
只需要输出最终数字结果`

type Result struct {
	Status string `json:"status"`
	Answer string `json:"answer"`
	Reason string `json:"reason,omitempty"`
}

func Unknown(reason string) Result { return Result{Status: "unknown", Reason: reason} }

var answerMarkup = strings.NewReplacer("**", "", "__", "", "`", "", "$", "", "\\(", "", "\\)", "", "\\[", "", "\\]", "")
var conclusionPattern = regexp.MustCompile(`(?i)(?:最终答案|正确答案|答案|结论|最少(?:需要)?(?:取出|摸出|拿出|取|摸|拿)?|至少(?:需要)?(?:取出|摸出|拿出|取|摸|拿)?|the\s+answer|minimum)[^\d\n。！？;；]{0,32}([0-9]+(?:\.[0-9]+)?)`)
var leadingAnswerPattern = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)\s*(?:(?:个|颗)(?:糖果)?)?\s*[，,。.!！\n]`)
var boxedPattern = regexp.MustCompile(`\\boxed\{\s*([0-9]+)\s*\}`)
var finalNumberPattern = regexp.MustCompile(`(?m)^\s*([0-9]+)\s*(?:个(?:糖果)?|颗(?:糖果)?)?\s*[。.!！]?\s*$`)
var deductionPattern = regexp.MustCompile(`(?:因此|所以|故|综上)[^\d\n。！？;；]{0,40}([0-9]+)\s*(?:个|颗)`)

// Extract the stated answer, allowing explanation without accepting incidental 21s.
// A later explicit conclusion supersedes an earlier tentative answer.
func AnswerIs21(answer string) bool {
	structured := strings.TrimSpace(answer)
	if strings.HasPrefix(structured, "```json") || strings.HasPrefix(structured, "```\n") {
		if first := strings.IndexByte(structured, '\n'); first >= 0 && strings.HasSuffix(structured, "```") {
			structured = strings.TrimSpace(structured[first+1 : len(structured)-3])
		}
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(structured), &object) == nil {
		if raw, ok := object["answer"]; ok {
			var number float64
			if json.Unmarshal(raw, &number) == nil {
				return number == 21
			}
			var text string
			return json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) == "21"
		}
	}
	text := answerMarkup.Replace(strings.TrimSpace(answer))
	if text == "21" {
		return true
	}
	var selected string
	lastEnd := 0
	lastPrefix := ""
	last := -1
	for _, pattern := range []*regexp.Regexp{conclusionPattern, boxedPattern, finalNumberPattern, deductionPattern, leadingAnswerPattern} {
		for _, match := range pattern.FindAllStringSubmatchIndex(text, -1) {
			if match[0] > last {
				last, selected = match[0], text[match[2]:match[3]]
				lastEnd = match[3]
				lastPrefix = text[match[0]:match[2]]
			}
		}
	}
	if selected != "21" {
		return false
	}
	for _, word := range []string{"不是", "并非", "不为", "不等于", "可能", "也许", "not ", "either "} {
		if strings.Contains(strings.ToLower(lastPrefix), word) {
			return false
		}
	}
	suffix := strings.TrimSpace(text[lastEnd:])
	for _, word := range []string{"或", "或者", "or ", ".", "-", "～", "~"} {
		if strings.HasPrefix(suffix, word) && (word != "." || (len(suffix) > 1 && suffix[1] >= '0' && suffix[1] <= '9')) {
			return false
		}
	}
	return true
}

func Grade(answer string) Result {
	if strings.TrimSpace(answer) == "" {
		return Unknown("empty_response")
	}
	if len(answer) > MaxAnswerBytes {
		return Unknown("response_too_large")
	}
	status := "degraded"
	if AnswerIs21(answer) {
		status = "smart"
	}
	return Result{Status: status, Answer: answer}
}

func Payload(chat bool) map[string]any {
	p := map[string]any{"model": Model, "store": false, "stream": true}
	if chat {
		p["messages"] = []map[string]string{{"role": "system", "content": OutputInstruction}, {"role": "user", "content": Prompt}}
		p["reasoning_effort"] = Effort
	} else {
		p["instructions"] = OutputInstruction
		p["input"] = []map[string]any{{"role": "user", "content": []map[string]string{{"type": "input_text", "text": Prompt}}}}
		p["reasoning"] = map[string]string{"effort": Effort}
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
	Status   string          `json:"status"`
	Output   []message       `json:"output"`
	Response *response       `json:"response"`
	Error    json.RawMessage `json:"error"`
	Choices  []struct {
		Index        int     `json:"index"`
		FinishReason *string `json:"finish_reason"`
		Delta        struct {
			Content string `json:"content"`
		} `json:"delta"`
		Message struct {
			Content string `json:"content"`
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
			if c.Type == "output_text" {
				answer.WriteString(c.Text)
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
	consume := func(data []byte) error {
		var r response
		if err := json.Unmarshal(data, &r); err != nil {
			return errors.New("invalid_response")
		}
		if len(r.Error) > 0 && string(r.Error) != "null" {
			return errors.New("upstream_error")
		}
		if chat {
			for _, c := range r.Choices {
				if c.Index != 0 {
					continue
				}
				if completed && (c.Delta.Content != "" || c.Message.Content != "") {
					return errors.New("invalid_response")
				}
				if sse {
					answer.WriteString(c.Delta.Content)
				} else {
					answer.WriteString(c.Message.Content)
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
				text, err := responseAnswer(r)
				if err != nil {
					return err
				}
				answer.Reset()
				answer.WriteString(text)
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
	return Grade(answer.String())
}
