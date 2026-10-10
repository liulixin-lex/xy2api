package service

import (
	"log/slog"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// API-key relays own their protocol contract. Never infer capability from the
// client's User-Agent. Legacy Codex endpoints retain their existing allowlist.
func openAIUnsupportedCacheFields(account *Account, model string) []string {
	if account == nil || !account.IsOpenAI() || account.IsOpenAIApiKey() {
		return nil
	}
	return []string{"prompt_cache_retention", "safety_identifier", "prompt_cache_options"}
}

func normalizeOpenAICacheControls(account *Account, body []byte) ([]byte, error) {
	model := gjson.GetBytes(body, "model").String()
	for _, field := range openAIUnsupportedCacheFields(account, model) {
		if !gjson.GetBytes(body, field).Exists() {
			continue
		}
		var err error
		body, err = sjson.DeleteBytes(body, field)
		if err != nil {
			return nil, err
		}
		slog.Debug("openai_cache_control_filtered", "account_id", account.ID, "model", openAICacheModelLabel(model), "field", field, "reason", "legacy_codex_protocol")
	}
	return body, nil
}
