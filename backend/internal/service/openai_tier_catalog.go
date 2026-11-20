package service

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

// Restrict the final catalog, after upstream/default metadata and public aliases
// have been merged. The source cache is credential-scoped and never mutated.
func restrictCodexTierCatalog(ctx context.Context, repo AccountRepository, body []byte, group *Group, accounts []Account, routes []CompositeModelRoute, routesAvailable bool) ([]byte, error) {
	if group == nil || group.OpenAITierPolicyVersion < OpenAITierPolicyExplicit {
		return body, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["models"], &rows); err != nil {
		return nil, err
	}
	visible := make([]map[string]json.RawMessage, 0, len(rows))
	now := time.Now()
	parents := make(map[int64]*Account)
	for _, row := range rows {
		var slug string
		if err := json.Unmarshal(row["slug"], &slug); err != nil {
			return nil, err
		}
		platform, model := group.Platform, slug
		if platform == PlatformComposite {
			var resolved bool
			platform, model, resolved = resolveCodexCompositeModelTarget(slug, accounts, routes, routesAvailable)
			if !resolved {
				platform = ""
			}
		}
		if platform != PlatformOpenAI {
			visible = append(visible, row)
			continue
		}
		routed := codexModelRoutingAccountIDs(group, slug)
		eligible, textRoute, otherRoute := 0, false, false
		fast, ultra := true, true
		for i := range accounts {
			account := &accounts[i]
			if !account.IsOpenAI() || !account.IsModelSupported(model) || len(routed) > 0 && !containsInt64(routed, account.ID) {
				continue
			}
			_, mapped := resolveOpenAIForwardMappedModels(account, model, false)
			endpoint := resolveUpstreamProtocol(account, APIProtocolResponses, mapped, nil)
			if !openAITierTextRoute(account, mapped) || endpoint == APIProtocolAnthropic {
				otherRoute = true
				continue
			}
			textRoute = true
			credential := account
			if account.IsShadow() {
				parentID := *account.ParentAccountID
				var loaded bool
				credential, loaded = parents[parentID]
				if !loaded && repo != nil {
					credential, _ = resolveCredentialAccount(ctx, repo, account)
					parents[parentID] = credential
				}
			}
			limits := openAITierLimits(group, group, account, mapped)
			if t := limits.capabilityTier(); t != "" && resolveOpenAITierCapability(credential, mapped, t, now, endpoint).state != "supported" {
				continue
			}
			eligible++
			fast = fast && !limits.FastDisabled && limits.forced != OpenAIFastTierUltrafast && resolveOpenAITierCapability(credential, mapped, OpenAIFastTierPriority, now, endpoint).state == "supported"
			ultra = ultra && !limits.UltrafastDisabled && limits.forced != OpenAIFastTierPriority && resolveOpenAITierCapability(credential, mapped, OpenAIFastTierUltrafast, now, endpoint).state == "supported"
		}
		if !textRoute && (otherRoute || !openAITierTextModel(model)) {
			visible = append(visible, row)
			continue
		}
		if eligible == 0 && !otherRoute && openAITierGroupHasHardLimit(group) {
			continue
		}
		fast = fast && eligible > 0 && !otherRoute
		ultra = ultra && eligible > 0 && !otherRoute
		var advertised []map[string]json.RawMessage
		if raw := row["service_tiers"]; len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
			if err := json.Unmarshal(raw, &advertised); err != nil {
				return nil, err
			}
		}
		opts := make([]map[string]json.RawMessage, 0, len(advertised)+2)
		present := map[string]bool{}
		for _, option := range advertised {
			var id string
			_ = json.Unmarshal(option["id"], &id)
			t := normalizedOpenAIServiceTierValue(id)
			if t == OpenAIFastTierPriority && !fast || t == OpenAIFastTierUltrafast && !ultra {
				continue
			}
			present[t] = true
			opts = append(opts, option)
		}
		for _, o := range []struct {
			tier, name string
			enabled    bool
		}{{OpenAIFastTierPriority, "Fast", fast}, {OpenAIFastTierUltrafast, "Ultrafast", ultra}} {
			if o.enabled && !present[o.tier] {
				id, _ := json.Marshal(o.tier)
				name, _ := json.Marshal(o.name)
				opts = append(opts, map[string]json.RawMessage{"id": id, "name": name})
			}
		}
		options, err := json.Marshal(opts)
		if err != nil {
			return nil, err
		}
		row["service_tiers"] = options
		var defaultTier string
		_ = json.Unmarshal(row["default_service_tier"], &defaultTier)
		if normalizedOpenAIServiceTierValue(defaultTier) == OpenAIFastTierPriority && !fast || normalizedOpenAIServiceTierValue(defaultTier) == OpenAIFastTierUltrafast && !ultra {
			row["default_service_tier"] = json.RawMessage("null")
		}
		visible = append(visible, row)
	}
	encoded, err := json.Marshal(visible)
	if err != nil {
		return nil, err
	}
	envelope["models"] = encoded
	return json.Marshal(envelope)
}
