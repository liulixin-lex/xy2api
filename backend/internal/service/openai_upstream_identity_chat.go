package service

import (
	"encoding/json"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
)

// The caller must retain the original json.Unmarshal branch when L-01 is off.
// decodeL01ChatRequestWithPresence expects the fresh zero-value target used by
// forwardAsChatCompletions; it replaces that existing successful body decode.
func decodeL01ChatRequestWithPresence(body []byte, target *apicompat.ChatCompletionsRequest, empty *bool) (bool, error) {
	wire := struct {
		*apicompat.ChatCompletionsRequest
		PromptCacheKey l01PresentString `json:"prompt_cache_key"`
	}{ChatCompletionsRequest: target}
	if wrapperErr := json.Unmarshal(body, &wire); wrapperErr != nil {
		// A field Unmarshaler changes type-error offsets and the wrapper changes
		// Struct/Field context. Recover the original decoder's exact error and
		// partial value, including duplicate-key/earliest-type-error semantics.
		// This extra whole-body decode occurs only on a rejected parse path;
		// successful requests still have only the replacement body decode.
		var canonical apicompat.ChatCompletionsRequest
		canonicalErr := json.Unmarshal(body, &canonical)
		*target = canonical
		if canonicalErr != nil {
			return false, canonicalErr
		}
		// Defensive: do not suppress an unexpected wrapper-only failure.
		return false, wrapperErr
	}
	if empty != nil {
		*empty = wire.PromptCacheKey.empty
	}
	target.PromptCacheKey = wire.PromptCacheKey.value
	return wire.PromptCacheKey.present, nil
}

type l01PresentString struct {
	present bool
	empty   bool
	value   string
}

func (p *l01PresentString) UnmarshalJSON(raw []byte) error {
	p.present = true
	p.empty = len(raw) == 2 && raw[0] == '"' && raw[1] == '"'
	// Preserve normal string decoding. In particular, null leaves the prior
	// string unchanged, so {"prompt_cache_key":"x","prompt_cache_key":null}
	// has the exact value retained by ChatCompletionsRequest's plain string.
	return json.Unmarshal(raw, &p.value)
}
