package apicompat

import (
	"math"

	"github.com/tidwall/gjson"
)

// OpenAIUsageCounts keeps field presence separate from a reported zero. All
// Responses/Chat adapters use the same precedence before billing conversion.
type OpenAIUsageCounts struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int
	CacheReadSource, CacheWriteSource                            string
}

func ParseOpenAIUsageCounts(value gjson.Result) OpenAIUsageCounts {
	input, _ := firstUsageCount(value, "input_tokens", "prompt_tokens")
	output, _ := firstUsageCount(value, "output_tokens", "completion_tokens")
	read, readSource := firstUsageCount(value,
		"input_tokens_details.cached_tokens", "prompt_tokens_details.cached_tokens",
		"cache_read_input_tokens", "cache_read_tokens", "cached_tokens")
	write, writeSource := firstUsageCount(value,
		"input_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_write_tokens",
		"input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_creation_tokens",
		"cache_write_tokens", "cache_creation_input_tokens", "cache_write_input_tokens", "cache_creation_tokens")
	return OpenAIUsageCounts{input, output, read, write, readSource, writeSource}
}

func firstUsageCount(value gjson.Result, paths ...string) (int, string) {
	for _, path := range paths {
		field := value.Get(path)
		if field.Type != gjson.Number || math.IsNaN(field.Num) || math.IsInf(field.Num, 0) ||
			field.Num != math.Trunc(field.Num) || field.Num >= float64(math.MaxInt) {
			continue
		}
		if field.Num <= 0 {
			return 0, path
		}
		return int(field.Num), path
	}
	return 0, ""
}
