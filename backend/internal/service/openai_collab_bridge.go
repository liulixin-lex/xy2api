package service

import (
	"context"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Anthropic does not have agent_message items. Use the existing Chat bridge's
// text convention for providers that do not encrypt these message parts.
func collabAnthropicBody(ctx context.Context, c *gin.Context, body []byte) []byte {
	if c == nil || !resilience.StateFrom(ctx).CollabSettingsSnapshot().CollabLadderOn() || !openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")) {
		return body
	}
	root := gjson.ParseBytes(body)
	if !gjson.ValidBytes(body) || !codexCollabUnambiguousJSON(root) || !root.Get("input").IsArray() || root.Get("previous_response_id").String() != "" {
		return body
	}
	next := body
	for i, item := range root.Get("input").Array() {
		if item.Get("type").String() != "agent_message" {
			continue
		}
		var text strings.Builder
		for _, part := range item.Get("content").Array() {
			switch part.Get("type").String() {
			case "input_text", "text":
				text.WriteString(part.Get("text").String())
			case "encrypted_content":
				text.WriteString(part.Get("encrypted_content").String())
			}
		}
		if text.Len() == 0 {
			continue
		}
		path := "input." + strconv.Itoa(i)
		var err error
		next, err = sjson.SetBytes(next, path, map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": text.String()}}})
		if err != nil {
			return body
		}
	}
	return next
}
