package service

import (
	"math"
	"unicode/utf8"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
)

//nolint:unused // M6/M7 wire readers after this prerequisite; no production caller is changed here.
func openAIResponsesUsageEvidence(data []byte, source string) (usage OpenAIUsage, present bool) {
	switch source {
	case "upstream_json", "upstream_sse_terminal", "upstream_sse_progressive":
	default:
		return usage, false
	}
	if !utf8.Valid(data) || !gjson.ValidBytes(data) || !gjson.ParseBytes(data).IsObject() {
		return usage, false
	}
	// Keep the extractor's candidate ordering, including explicitly empty usage.
	for _, path := range []string{"usage", "response.usage", "data.usage", "data.response.usage"} {
		value := gjson.GetBytes(data, path)
		if !value.IsObject() {
			continue
		}
		if _, ok := openAIUsageFromGJSON(value); !ok {
			continue
		}
		valid := func(path string) bool {
			v := value.Get(path)
			return v.Type == gjson.Number && v.Num >= 0 && v.Num < float64(math.MaxInt) && !math.IsInf(v.Num, 0) && v.Num == math.Trunc(v.Num)
		}
		counts := apicompat.ParseOpenAIUsageCounts(value)
		for _, field := range []string{"input_tokens", "prompt_tokens", "output_tokens", "completion_tokens", "total_tokens", "input_tokens_details.cached_tokens", "prompt_tokens_details.cached_tokens", "cache_read_input_tokens", "cache_read_tokens", "cached_tokens", "input_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_write_tokens", "input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_creation_tokens", "cache_write_tokens", "cache_creation_input_tokens", "cache_write_input_tokens", "cache_creation_tokens", "completion_tokens_details.reasoning_tokens", "output_tokens_details.reasoning_tokens", "output_tokens_details.image_tokens", "completion_tokens_details.image_tokens", "input_tokens_details.image_tokens", "prompt_tokens_details.image_tokens"} {
			if valid(field) {
				present = true
				break
			}
		}
		if !present {
			return OpenAIUsage{}, false
		}
		usage, _ = extractOpenAIUsageFromJSONBytes(data)
		usage.InputTokensPresent, usage.OutputTokensPresent = counts.InputTokensPresent, counts.OutputTokensPresent
		usage.CacheReadInputTokensPresent = counts.CacheReadSource != "" && valid(counts.CacheReadSource)
		usage.CacheCreationInputTokensPresent = counts.CacheWriteSource != "" && valid(counts.CacheWriteSource)
		return usage, true
	}
	return usage, false
}
