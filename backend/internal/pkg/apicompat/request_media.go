package apicompat

import (
	"encoding/json"
	"net/url"
	"strings"
)

func validRemoteMediaURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}

func anthropicMediaToResponses(b AnthropicContentBlock) (ResponsesContentPart, error) {
	part := ResponsesContentPart{CacheControl: b.CacheControl}
	if b.Source == nil || (b.Source.Type != "url" && b.Source.Type != "base64") {
		return part, unsupportedConversion("content.source", "expected a base64 or URL media source")
	}
	if b.Source.Type == "url" && (!validRemoteMediaURL(b.Source.URL) || b.Source.Data != "" || b.Source.MediaType != "") {
		return part, unsupportedConversion("content.source", "invalid or ambiguous URL source")
	}
	if b.Source.Type == "base64" && b.Source.URL != "" {
		return part, unsupportedConversion("content.source", "ambiguous base64 source")
	}
	reference := anthropicImageToDataURI(b.Source)
	if reference == "" {
		return part, unsupportedConversion("content.source", "empty or invalid media source")
	}
	if b.Type == "image" {
		part.Type, part.ImageURL = "input_image", reference
	} else {
		part.Type, part.Filename = "input_file", b.Title
		if b.Source.Type == "url" {
			part.FileURL = reference
		} else {
			part.FileData = reference
		}
	}
	return part, nil
}

func responsesMediaToAnthropic(p ResponsesContentPart) (AnthropicContentBlock, error) {
	block := AnthropicContentBlock{CacheControl: p.CacheControl}
	if p.Detail != "" && p.Detail != "auto" {
		return block, unsupportedConversion("content.detail", "Anthropic has no image/file detail equivalent")
	}
	if p.FileID != "" {
		return block, unsupportedConversion("content.file_id", "provider-scoped file IDs cannot be transferred to Anthropic")
	}
	var reference string
	switch p.Type {
	case "input_image":
		if p.FileData != "" || p.FileURL != "" || p.Filename != "" {
			return block, unsupportedConversion("content.input_image", "unexpected file fields on an image")
		}
		block.Type, reference = "image", p.ImageURL
	case "input_file":
		block.Type, block.Title = "document", p.Filename
		if p.FileData != "" && p.FileURL != "" {
			return block, unsupportedConversion("content.input_file", "multiple file sources are ambiguous")
		}
		reference = p.FileData
		if p.FileURL != "" {
			reference = p.FileURL
		}
	}
	if validRemoteMediaURL(reference) {
		block.Source = &AnthropicImageSource{Type: "url", URL: reference}
		return block, nil
	}
	source := dataURIToAnthropicImageSource(reference)
	if source == nil || strings.TrimSpace(source.Data) == "" {
		return block, unsupportedConversion("content."+p.Type, "expected a nonempty base64 data URI or an HTTP(S) URL")
	}
	if block.Type == "document" {
		if source.MediaType != "application/pdf" {
			return block, unsupportedConversion("content.file_data", "only PDF data URIs have a supported document representation")
		}
	} else {
		switch source.MediaType {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
		default:
			return block, unsupportedConversion("content.image_url", "unsupported Anthropic image media type")
		}
	}
	block.Source = source
	return block, nil
}

// Preserve typed media in function outputs across marshal/unmarshal boundaries.
func (i ResponsesInputItem) MarshalJSON() ([]byte, error) {
	type wire ResponsesInputItem
	if len(i.outputRaw) == 0 {
		return json.Marshal(wire(i))
	}
	return json.Marshal(struct {
		wire
		Output json.RawMessage `json:"output"`
	}{wire: wire(i), Output: i.outputRaw})
}

// Chat tool messages cannot carry media. Reuse the established attributed user
// message placement, while validating media and preserving file references and
// image detail. Opaque tool data remains byte-for-byte unchanged without media.
func extractChatToolOutputMedia(raw json.RawMessage) (string, []ChatContentPart, bool, error) {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		if isToolOutputImageDataURL(encoded) {
			return toolOutputMediaMarker, []ChatContentPart{toolOutputImagePart(encoded)}, true, nil
		}
		raw = json.RawMessage(encoded)
	}
	value, ok := decodeToolOutputJSON(raw)
	if !ok {
		return "", nil, false, nil
	}
	value, media, err := rewriteChatToolOutputMedia(value)
	if err != nil {
		return "", nil, false, err
	}
	if len(media) == 0 {
		return "", nil, false, nil
	}
	wire, err := json.Marshal(value)
	return string(wire), media, true, err
}

func rewriteChatToolOutputMedia(value any) (any, []ChatContentPart, error) {
	switch v := value.(type) {
	case []any:
		var media []ChatContentPart
		for i, child := range v {
			rewritten, parts, err := rewriteChatToolOutputMedia(child)
			if err != nil {
				return nil, nil, err
			}
			v[i] = rewritten
			media = append(media, parts...)
		}
		return v, media, nil
	case map[string]any:
		switch v["type"] {
		case "input_image", "image_url", "input_file":
			raw, _ := json.Marshal(v)
			content, err := responsesContentPartsToChatContent([]json.RawMessage{raw}, "user")
			if err != nil {
				return nil, nil, err
			}
			var parts []ChatContentPart
			if err := json.Unmarshal(content, &parts); err != nil {
				return nil, nil, err
			}
			return map[string]any{"type": "input_text", "text": toolOutputMediaMarker}, parts, nil
		case "input_audio", "input_video":
			return nil, nil, unsupportedConversion("input.output", "unsupported tool output media")
		}
		if content, ok := v["content"]; ok {
			rewritten, parts, err := rewriteChatToolOutputMedia(content)
			if err != nil {
				return nil, nil, err
			}
			v["content"] = rewritten
			return v, parts, nil
		}
	}
	return value, nil, nil
}
