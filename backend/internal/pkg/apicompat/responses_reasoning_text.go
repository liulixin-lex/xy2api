package apicompat

import "strings"

func ResponsesOutputReasoningText(item ResponsesOutput) string {
	if item.Type != "reasoning" {
		return ""
	}
	var text strings.Builder
	for _, part := range item.Summary {
		if part.Type == "summary_text" {
			text.WriteString(part.Text)
		}
	}
	if text.Len() > 0 {
		return text.String()
	}
	for _, part := range item.Content {
		if part.Type == "reasoning_text" {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}
