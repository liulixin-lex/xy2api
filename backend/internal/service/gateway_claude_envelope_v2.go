package service

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// This grammar deliberately recognizes only standalone protocol attribution.
// Quoted text, unknown keys and multiline instructions are not protocol markers.
var compatV2Billing = regexp.MustCompile(`^x-anthropic-billing-header: cc_version=[0-9]+\.[0-9]+\.[0-9]+(?:\.[0-9a-fA-F]{3})?;(?: cc_entrypoint=(?:cli|sdk-cli|claude-vscode);)?(?: cch=[0-9a-fA-F]+;)?$`)

// compatSystemV2 separates only the known leading envelope. Business blocks
// remain raw JSON and in order, including their cache controls and whitespace.
// Moving markers through business instructions would change semantics, so that
// shape is rejected rather than silently rewritten.
func compatSystemV2(system gjson.Result) ([]json.RawMessage, json.RawMessage, error) {
	var raw []json.RawMessage
	if system.Exists() && system.Type != gjson.Null {
		if system.Type == gjson.String {
			block, _ := json.Marshal(map[string]string{"type": "text", "text": system.String()})
			raw = append(raw, block)
		} else if system.IsArray() {
			for _, block := range system.Array() {
				raw = append(raw, json.RawMessage(block.Raw))
			}
		} else {
			return nil, nil, compatInvalid("anthropic_compat_request_unsupported", "system must be text or a text block array")
		}
	}
	var business []json.RawMessage
	var identity json.RawMessage
	leading := true
	for _, rawBlock := range raw {
		block := gjson.ParseBytes(rawBlock)
		if !block.IsObject() || block.Get("type").String() != "text" || block.Get("text").Type != gjson.String {
			return nil, nil, compatInvalid("anthropic_compat_request_unsupported", "system must contain text blocks")
		}
		text := block.Get("text").String()
		isIdentity, isBilling := text == claudeCodeSystemPrompt, compatV2Billing.MatchString(text)
		if !isIdentity && !isBilling {
			if strings.HasPrefix(text, "x-anthropic-billing-header:") {
				return nil, nil, compatInvalid("anthropic_compat_request_unsupported", "Unrecognized Claude Code billing block cannot be normalized")
			}
			leading = false
			business = append(business, rawBlock)
			continue
		}
		if !leading {
			return nil, nil, compatInvalid("anthropic_compat_request_unsupported", "Claude Code envelope blocks must precede business system instructions")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawBlock, &fields); err != nil {
			return nil, nil, err
		}
		for key := range fields {
			if key != "type" && key != "text" && (!isIdentity || key != "cache_control") {
				return nil, nil, compatInvalid("anthropic_compat_request_unsupported", "Claude Code envelope contains unsupported block attributes")
			}
		}
		if isIdentity {
			canonical, _ := json.Marshal(fields)
			if identity != nil && !bytes.Equal(identity, canonical) {
				return nil, nil, compatInvalid("anthropic_compat_request_unsupported", "Duplicate Claude Code identity blocks have different attributes")
			}
			identity = canonical
		}
	}
	if identity == nil {
		identity, _ = json.Marshal(map[string]string{"type": "text", "text": claudeCodeSystemPrompt})
	}
	return business, identity, nil
}

func compatInjectBodyV2(body []byte, profile *AnthropicCompatProfile, metadata string) ([]byte, error) {
	business, identity, err := compatSystemV2(gjson.GetBytes(body, "system"))
	if err != nil {
		return nil, err
	}
	text, err := buildBillingAttributionText(body, ExtractCLIVersion(profile.UserAgent))
	if err != nil {
		return nil, compatInvalid("anthropic_compat_configuration_conflict", "Compatibility profile has no CLI version")
	}
	billing, _ := json.Marshal(map[string]string{"type": "text", "text": text})
	blocks := append([]json.RawMessage{billing, identity}, business...)
	system, _ := json.Marshal(blocks)
	result, err := sjson.SetRawBytes(body, "system", system)
	if err != nil {
		return nil, err
	}
	if value := gjson.GetBytes(result, "metadata"); value.Exists() && value.Type != gjson.Null && !value.IsObject() {
		return nil, compatInvalid("anthropic_compat_request_unsupported", "metadata must be an object")
	}
	return sjson.SetBytes(result, "metadata.user_id", metadata)
}
