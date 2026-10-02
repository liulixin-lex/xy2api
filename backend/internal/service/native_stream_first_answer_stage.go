package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

func nativeResponsesFirstAnswerOutput(payload []byte) bool {
	if !gjson.ValidBytes(payload) {
		return false
	}
	_, answer, _, _ := classifySemanticEvent(payload)
	if answer {
		return true
	}
	typ := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	switch typ {
	case "response.reasoning_text.delta", "response.reasoning_text.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		return false
	case "response.content_part.added", "response.content_part.done":
		if gjson.GetBytes(payload, "part.type").String() == "summary_text" {
			return false
		}
	case "response.output_item.added", "response.output_item.done":
		if gjson.GetBytes(payload, "item.type").String() == "reasoning" {
			return false
		}
	case "response.completed", "response.done":
		for _, item := range gjson.GetBytes(payload, "response.output").Array() {
			if item.Get("type").String() != "reasoning" && openAIStreamItemHasVisibleOutput(item) {
				return true
			}
		}
		return false
	}
	return openAIStreamDataStartsVisibleOutput(string(payload), typ)
}
