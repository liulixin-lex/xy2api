package service

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// compatSystemEnvelope canonicalizes protocol markers, retaining the first
// identity's cache control and the order and values of all business blocks.
func compatSystemEnvelope(system gjson.Result) ([]json.RawMessage, json.RawMessage) {
	var blocks []gjson.Result
	if system.IsArray() {
		blocks = system.Array()
	} else if system.Type == gjson.String {
		raw, _ := json.Marshal(map[string]string{"type": "text", "text": system.String()})
		blocks = []gjson.Result{gjson.ParseBytes(raw)}
	} else if system.Exists() && system.Type != gjson.Null {
		// Leave malformed client values to the same upstream validation as ordinary accounts.
		blocks = []gjson.Result{system}
	}
	var business []json.RawMessage
	var identity json.RawMessage
	for _, block := range blocks {
		if block.Get("type").String() == "text" {
			text := block.Get("text").String()
			if strings.HasPrefix(text, "x-anthropic-billing-header:") {
				continue
			}
			if text == claudeCodeSystemPrompt {
				if identity == nil {
					fields := map[string]json.RawMessage{}
					_ = json.Unmarshal([]byte(block.Raw), &fields)
					canonical := map[string]json.RawMessage{"type": json.RawMessage(`"text"`), "text": fields["text"]}
					if cc, ok := fields["cache_control"]; ok {
						canonical["cache_control"] = cc
					}
					identity, _ = json.Marshal(canonical)
				}
				continue
			}
		}
		business = append(business, json.RawMessage(block.Raw))
	}
	if identity == nil {
		identity, _ = json.Marshal(map[string]string{"type": "text", "text": claudeCodeSystemPrompt})
	}
	return business, identity
}

func compatInjectBody(body []byte, profile *AnthropicCompatProfile, metadata string) ([]byte, error) {
	business, identity := compatSystemEnvelope(gjson.GetBytes(body, "system"))
	text, err := buildBillingAttributionText(body, ExtractCLIVersion(profile.UserAgent))
	if err != nil {
		return nil, err
	}
	billing, _ := json.Marshal(map[string]string{"type": "text", "text": text})
	system, err := json.Marshal(append([]json.RawMessage{billing, identity}, business...))
	if err != nil {
		return nil, err
	}
	result, err := sjson.SetRawBytes(body, "system", system)
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(result, "metadata.user_id", metadata)
}
