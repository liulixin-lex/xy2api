package service

// This file is a test-only differential oracle. Below the imports is a
// verbatim copy of extractContentModerationInput and of the Responses helpers
// it calls from content_moderation_input.go as of commit 9eb394e3e; only the
// identifiers carry a baseline prefix. Do not "fix" the logic: its value is
// that it is the earlier behaviour.

import (
	"strings"

	"github.com/tidwall/gjson"
)

func baselineExtractModerationInput(protocol string, body []byte, filterReminders bool) ContentModerationInput {
	collector := moderationTextCollector{filterReminders: filterReminders}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ContentModerationInput{}
	}
	var parts []string
	var images []string
	switch protocol {
	case ContentModerationProtocolAnthropicMessages:
		collector.collectLastAnthropicUserMessage(gjson.GetBytes(body, "messages"), &parts, &images)
	case ContentModerationProtocolOpenAIChat:
		collector.collectLastRoleMessage(gjson.GetBytes(body, "messages"), "user", &parts, &images)
	case ContentModerationProtocolOpenAIResponses:
		collector.baselineCollectLastResponsesInput(gjson.GetBytes(body, "input"), &parts, &images)
	case ContentModerationProtocolGemini:
		collector.collectLastGeminiContent(gjson.GetBytes(body, "contents"), &parts, &images)
	case ContentModerationProtocolOpenAIImages:
		collector.addModerationText(&parts, gjson.GetBytes(body, "prompt").String())
		collector.collectContentValue(gjson.GetBytes(body, "images"), &parts, &images)
	case ContentModerationProtocolTypeSafeSystemOne:
		// System One carries no client-harness reminder blocks, so a literal
		// <system-reminder> is ordinary user text and must never be skipped.
		moderationTextCollector{}.collectSystemOneInput(body, &parts)
	default:
		collector.baselineCollectLastResponsesInput(gjson.GetBytes(body, "input"), &parts, &images)
		collector.collectLastRoleMessage(gjson.GetBytes(body, "messages"), "user", &parts, &images)
		collector.collectLastGeminiContent(gjson.GetBytes(body, "contents"), &parts, &images)
	}
	out := ContentModerationInput{
		Text:   normalizeContentModerationText(strings.Join(parts, "\n")),
		Images: normalizeModerationImages(images),
	}
	if filterReminders {
		out.Normalize()
	}
	return out
}

func (collector moderationTextCollector) baselineCollectLastResponsesInput(input gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !input.Exists():
		return
	case input.Type == gjson.String:
		collector.addModerationText(parts, input.String())
	case input.IsArray():
		array := input.Array()
		if len(array) == 0 {
			return
		}
		last := array[len(array)-1]
		if !collector.baselineIsResponsesUserTextItem(last) {
			return
		}
		collector.collectContentValue(last.Get("content"), parts, images)
		if last.Get("type").String() == "input_text" || last.Get("text").Exists() {
			collector.collectContentValue(last, parts, images)
		}
	case input.IsObject():
		if collector.baselineIsResponsesUserTextItem(input) {
			collector.collectContentValue(input.Get("content"), parts, images)
			if input.Get("type").String() == "input_text" || input.Get("text").Exists() {
				collector.collectContentValue(input, parts, images)
			}
		}
	}
}

func (collector moderationTextCollector) baselineIsResponsesUserTextItem(item gjson.Result) bool {
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role == "user" {
		return collector.baselineResponseItemHasModerationText(item)
	}
	if role != "" {
		return false
	}
	return collector.baselineResponseItemHasModerationText(item)
}

func (collector moderationTextCollector) baselineResponseItemHasModerationText(item gjson.Result) bool {
	var parts []string
	var images []string
	collector.collectContentValue(item.Get("content"), &parts, &images)
	if item.Get("type").String() == "input_text" || item.Get("text").Exists() {
		collector.collectContentValue(item, &parts, &images)
	}
	return normalizeContentModerationText(strings.Join(parts, "\n")) != "" || len(images) > 0
}
