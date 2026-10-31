package service

import (
	"encoding/json"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
)

func (u *ClaudeUsage) UnmarshalJSON(data []byte) error {
	type wire ClaudeUsage
	var v wire
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*u = ClaudeUsage(v)
	var src apicompat.AnthropicUsage
	if err := json.Unmarshal(data, &src); err != nil {
		return err
	}
	mergeAnthropicCacheUsage(u, src)
	return nil
}

func mergeAnthropicCacheUsage(dst *ClaudeUsage, src apicompat.AnthropicUsage) {
	if src.CacheCreationInputTokensPresent || src.CacheCreationInputTokens > 0 {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
		dst.CacheCreationInputTokensPresent = true
	}
	if src.CacheCreation != nil {
		if p := src.CacheCreation.Ephemeral5mInputTokens; p != nil {
			dst.CacheCreation5mTokens, dst.CacheCreation5mTokensPresent = *p, true
		}
		if p := src.CacheCreation.Ephemeral1hInputTokens; p != nil {
			dst.CacheCreation1hTokens, dst.CacheCreation1hTokensPresent = *p, true
		}
	}
	if !dst.CacheCreationInputTokensPresent && (dst.CacheCreation5mTokensPresent || dst.CacheCreation1hTokensPresent) {
		dst.CacheCreationInputTokens = dst.CacheCreation5mTokens + dst.CacheCreation1hTokens
	}
}
