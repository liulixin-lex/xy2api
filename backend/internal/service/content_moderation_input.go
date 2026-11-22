package service

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"github.com/tidwall/gjson"
)

func ExtractContentModerationText(protocol string, body []byte) string {
	return ExtractContentModerationInput(protocol, body).Text
}

func ExtractContentModerationInput(protocol string, body []byte) ContentModerationInput {
	return extractContentModerationInput(protocol, body, true)
}

// Keyword checks share semantic moderation's current-user boundaries, but must
// inspect client-supplied reminder blocks as ordinary user text.
func extractContentModerationKeywordText(protocol string, body []byte) string {
	return extractContentModerationInput(protocol, body, false).Text
}

type moderationTextCollector struct {
	filterReminders bool
}

func extractContentModerationInput(protocol string, body []byte, filterReminders bool) ContentModerationInput {
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
		if messages := gjson.GetBytes(body, "messages"); messages.Exists() {
			collector.collectLastRoleMessage(messages, "user", &parts, &images)
		} else {
			// Chat without messages forwards its input as a Responses request.
			collector.collectLastResponsesInput(gjson.GetBytes(body, "input"), &parts, &images)
		}
	case ContentModerationProtocolOpenAIResponses:
		collector.collectLastResponsesInput(gjson.GetBytes(body, "input"), &parts, &images)
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
		collector.collectLastResponsesInput(gjson.GetBytes(body, "input"), &parts, &images)
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

// collectSystemOneInput moderates every client-controlled text of a System One
// request: question IDs, every question field except the validated type,
// unknown top-level extension fields, and the evaluated state. Object keys are
// sent to Jev as part of the JSON, so they are moderated like values.
func (collector moderationTextCollector) collectSystemOneInput(body []byte, parts *[]string) {
	root := gjson.ParseBytes(body)
	questions := root.Get("questions")
	if !questions.IsObject() {
		collector.collectSystemOneText(questions, parts)
	}
	questions.ForEach(func(id, question gjson.Result) bool {
		collector.addModerationText(parts, id.String())
		if !question.IsObject() {
			collector.collectSystemOneText(question, parts)
			return true
		}
		question.ForEach(func(field, value gjson.Result) bool {
			switch field.String() {
			case "type":
				return true
			case "instructions", "criteria":
			default:
				collector.addModerationText(parts, field.String())
			}
			collector.collectSystemOneText(value, parts)
			return true
		})
		return true
	})
	root.ForEach(func(field, value gjson.Result) bool {
		switch field.String() {
		case "model", "stream", "state", "questions":
			return true
		}
		collector.addModerationText(parts, field.String())
		collector.collectSystemOneText(value, parts)
		return true
	})
	collector.collectSystemOneText(root.Get("state"), parts)
}

func (collector moderationTextCollector) collectSystemOneText(value gjson.Result, parts *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		collector.addModerationText(parts, value.String())
	case value.IsArray():
		value.ForEach(func(_, child gjson.Result) bool {
			collector.collectSystemOneText(child, parts)
			return true
		})
	case value.IsObject():
		value.ForEach(func(key, child gjson.Result) bool {
			collector.addModerationText(parts, key.String())
			collector.collectSystemOneText(child, parts)
			return true
		})
	}
}

func (collector moderationTextCollector) collectLastRoleMessage(messages gjson.Result, role string, parts *[]string, images *[]string) {
	if !messages.IsArray() {
		return
	}
	array := messages.Array()
	if len(array) == 0 {
		return
	}
	last := array[len(array)-1]
	if strings.ToLower(strings.TrimSpace(last.Get("role").String())) != role {
		return
	}
	var candidate []string
	var candidateImages []string
	collector.collectContentValue(last.Get("content"), &candidate, &candidateImages)
	if normalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func (collector moderationTextCollector) collectLastAnthropicUserMessage(messages gjson.Result, parts *[]string, images *[]string) {
	if !messages.IsArray() {
		return
	}
	array := messages.Array()
	if len(array) == 0 {
		return
	}
	// Anthropic requests may append a system message after the user turn. It
	// does not represent a new model/tool turn, so retain the latest user
	// content while continuing to reject assistant/tool-ended loops.
	lastUser := len(array) - 1
	for lastUser >= 0 && strings.ToLower(strings.TrimSpace(array[lastUser].Get("role").String())) == "system" {
		lastUser--
	}
	if lastUser < 0 || strings.ToLower(strings.TrimSpace(array[lastUser].Get("role").String())) != "user" {
		return
	}
	var candidate []string
	var candidateImages []string
	collector.collectAnthropicUserContentValue(array[lastUser].Get("content"), &candidate, &candidateImages)
	if normalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func (collector moderationTextCollector) collectAnthropicUserContentValue(value gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		if !collector.filterReminders || !isAnthropicSystemReminderText(value.String()) {
			collector.addModerationText(parts, value.String())
		}
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collector.collectAnthropicUserContentValue(item, parts, images)
			return true
		})
	case value.IsObject():
		typ := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
		switch typ {
		case "", "text", "input_text", "message":
			if value.Get("text").Exists() && (!collector.filterReminders || !isAnthropicSystemReminderText(value.Get("text").String())) {
				collector.addModerationText(parts, value.Get("text").String())
			}
			if value.Get("content").Exists() {
				collector.collectAnthropicUserContentValue(value.Get("content"), parts, images)
			}
		case "image_url", "input_image", "image":
			collector.collectContentValue(value, parts, images)
		}
	}
}

func isAnthropicSystemReminderText(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "<system-reminder>")
}

func (collector moderationTextCollector) collectLastResponsesInput(input gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !input.Exists():
		return
	case input.Type == gjson.String:
		collector.addModerationText(parts, input.String())
	case input.IsArray():
		item, ok := lastResponsesUserTextItem(input.Array())
		if !ok {
			return
		}
		collector.collectContentValue(item.Get("content"), parts, images)
		if item.Get("type").String() == "input_text" || item.Get("text").Exists() {
			collector.collectContentValue(item, parts, images)
		}
	case input.IsObject():
		if collector.isResponsesUserTextItem(input) {
			collector.collectContentValue(input.Get("content"), parts, images)
			if input.Get("type").String() == "input_text" || input.Get("text").Exists() {
				collector.collectContentValue(input, parts, images)
			}
		}
	}
}

// lastResponsesUserTextItem picks the item of a Responses input array to
// moderate: the last item when it is user text, whatever its type, and
// otherwise the last user text item in the trailing window. A tool output
// inside the window is never the user's words. Reminder blocks count as text
// here, so the keyword and semantic checks read the same item.
func lastResponsesUserTextItem(items []gjson.Result) (gjson.Result, bool) {
	if len(items) == 0 {
		return gjson.Result{}, false
	}
	unfiltered := moderationTextCollector{}
	if last := items[len(items)-1]; unfiltered.isResponsesUserTextItem(last) {
		return last, true
	}
	window := responsesTrailingWindow(items)
	for i := len(window) - 1; i >= 0; i-- {
		if !isResponsesToolOutputItem(window[i]) && unfiltered.isResponsesUserTextItem(window[i]) {
			return window[i], true
		}
	}
	return gjson.Result{}, false
}

// responsesTrailingWindow returns the run of consecutive non-model items at
// the end of a Responses input array: what the client added after the last
// model output. A request that continues a tool loop ends with a window of
// tool outputs only, and an array that ends with model output has none.
//
// Two item types are transparent: the window spans them, but nothing is read
// from them. additional_tools declares tools with a developer role and may
// sit between or after client items (Codex Responses Lite), and
// compaction_trigger has to be the last item, where Codex appends it to a
// compaction request. Neither is model output, and stopping at them would
// hide what the client just added.
//
// Every other type still bounds the window, item_reference, empty objects and
// unknown types included: they may be or point to model output, and crossing
// them would moderate content of earlier turns again.
func responsesTrailingWindow(items []gjson.Result) []gjson.Result {
	start := len(items)
	for start > 0 && (isResponsesNonModelItem(items[start-1]) || isResponsesTransparentItem(items[start-1])) {
		start--
	}
	return items[start:]
}

func isResponsesTransparentItem(item gjson.Result) bool {
	if !item.IsObject() {
		return false
	}
	typ := item.Get("type")
	return typ.Type == gjson.String && (typ.String() == "additional_tools" || typ.String() == "compaction_trigger")
}

// isResponsesNonModelItem reports whether a Responses input item came from
// the client side of a turn: a message whose role is not assistant (a message
// may omit its type), or any tool output (*_output). Everything else,
// including non-object elements, counts as model output.
func isResponsesNonModelItem(item gjson.Result) bool {
	if !item.IsObject() {
		return false
	}
	if isResponsesToolOutputItem(item) {
		return true
	}
	typ := item.Get("type")
	role := item.Get("role")
	isMessage := typ.Type == gjson.String && typ.String() == "message" || !typ.Exists() && role.Type == gjson.String
	return isMessage && strings.ToLower(strings.TrimSpace(role.String())) != "assistant"
}

func isResponsesToolOutputItem(item gjson.Result) bool {
	typ := item.Get("type")
	return typ.Type == gjson.String && strings.HasSuffix(typ.String(), "_output")
}

func (collector moderationTextCollector) isResponsesUserTextItem(item gjson.Result) bool {
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role == "user" {
		return collector.responseItemHasModerationText(item)
	}
	if role != "" {
		return false
	}
	return collector.responseItemHasModerationText(item)
}

func (collector moderationTextCollector) responseItemHasModerationText(item gjson.Result) bool {
	var parts []string
	var images []string
	collector.collectContentValue(item.Get("content"), &parts, &images)
	if item.Get("type").String() == "input_text" || item.Get("text").Exists() {
		collector.collectContentValue(item, &parts, &images)
	}
	return normalizeContentModerationText(strings.Join(parts, "\n")) != "" || len(images) > 0
}

func (collector moderationTextCollector) collectLastGeminiContent(contents gjson.Result, parts *[]string, images *[]string) {
	if !contents.IsArray() {
		return
	}
	array := contents.Array()
	if len(array) == 0 {
		return
	}
	last := array[len(array)-1]
	role := strings.ToLower(strings.TrimSpace(last.Get("role").String()))
	if role != "" && role != "user" {
		return
	}
	var candidate []string
	var candidateImages []string
	if arr := last.Get("parts"); arr.IsArray() {
		arr.ForEach(func(_, part gjson.Result) bool {
			collector.addModerationText(&candidate, part.Get("text").String())
			addGeminiModerationImage(&candidateImages, part)
			return true
		})
	}
	if normalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
		return
	}
	*parts = append(*parts, candidate...)
	*images = append(*images, candidateImages...)
}

func (collector moderationTextCollector) collectContentValue(value gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		collector.addModerationText(parts, value.String())
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collector.collectContentValue(item, parts, images)
			return true
		})
	case value.IsObject():
		typ := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
		addModerationImage(images, value.Get("image_url.url").String())
		addModerationImage(images, value.Get("image_url").String())
		addModerationImage(images, value.Get("url").String())
		addModerationImageData(images, value.Get("source.media_type").String(), value.Get("source.data").String())
		addModerationImageData(images, value.Get("source.mediaType").String(), value.Get("source.data").String())
		addModerationImageData(images, value.Get("media_type").String(), value.Get("data").String())
		addModerationImageData(images, value.Get("mime_type").String(), value.Get("data").String())
		addModerationImageData(images, value.Get("mimeType").String(), value.Get("data").String())
		addModerationImage(images, value.Get("source.data").String())
		addModerationImage(images, value.Get("data").String())
		addModerationImage(images, value.Get("base64").String())
		switch typ {
		case "", "text", "input_text", "message":
			if value.Get("text").Exists() {
				collector.addModerationText(parts, value.Get("text").String())
			}
			if value.Get("content").Exists() {
				collector.collectContentValue(value.Get("content"), parts, images)
			}
		case "image_url", "input_image", "image":
		}
	}
}

func addGeminiModerationImage(images *[]string, part gjson.Result) {
	if inlineData := part.Get("inline_data"); inlineData.IsObject() {
		mimeType := strings.TrimSpace(inlineData.Get("mime_type").String())
		data := strings.TrimSpace(inlineData.Get("data").String())
		if mimeType != "" && data != "" {
			addModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
		}
	}
	if inlineData := part.Get("inlineData"); inlineData.IsObject() {
		mimeType := strings.TrimSpace(inlineData.Get("mimeType").String())
		data := strings.TrimSpace(inlineData.Get("data").String())
		if mimeType != "" && data != "" {
			addModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
		}
	}
	addModerationImage(images, part.Get("file_data.file_uri").String())
	addModerationImage(images, part.Get("fileData.fileUri").String())
}

func addModerationImageData(images *[]string, mimeType string, data string) {
	mimeType = strings.TrimSpace(mimeType)
	data = strings.TrimSpace(data)
	if mimeType == "" || data == "" {
		return
	}
	addModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
}

func addModerationImage(images *[]string, image string) {
	image = strings.TrimSpace(image)
	if image == "" {
		return
	}
	if strings.HasPrefix(image, "data:") || strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		*images = append(*images, image)
	}
}

func normalizeModerationImages(images []string) []string {
	out := make([]string, 0, len(images))
	seen := make(map[string]struct{}, len(images))
	for _, image := range images {
		image = strings.TrimSpace(image)
		if image == "" {
			continue
		}
		if _, ok := seen[image]; ok {
			continue
		}
		seen[image] = struct{}{}
		out = append(out, image)
	}
	return out
}

func limitContentModerationImages(images []string) []string {
	if len(images) <= maxContentModerationInputImages {
		return images
	}
	idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(images))))
	if err != nil {
		return images[:maxContentModerationInputImages]
	}
	return []string{images[int(idx.Int64())]}
}

func (collector moderationTextCollector) addModerationText(parts *[]string, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if collector.filterReminders && strings.Contains(text, "<system-reminder>") {
		return
	}
	*parts = append(*parts, text)
}

func normalizeContentModerationText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}
