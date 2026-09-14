package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
)

type IQModel struct {
	ID                       string            `json:"id"`
	DisplayName              string            `json:"display_name"`
	Source                   string            `json:"source"`
	Reasoning                *bool             `json:"reasoning,omitempty"`
	SupportedReasoningLevels []string          `json:"supported_reasoning_levels,omitempty"`
	DefaultReasoningLevel    string            `json:"default_reasoning_level,omitempty"`
	CapabilitySources        map[string]string `json:"capability_sources"`
}

type IQModelCatalog struct {
	Models    []IQModel  `json:"models"`
	FetchedAt *time.Time `json:"fetched_at"`
	FromCache bool       `json:"from_cache"`
	Stale     bool       `json:"stale"`
	Error     string     `json:"error,omitempty"`
}

// Discovery uses the raw upstream catalog, without SyncUpstreamModelCatalog's
// configured-model fallback, registry requests or account metadata writes.
func (s *IQCheckService) Models(ctx context.Context, id int64, refresh bool) (IQModelCatalog, error) {
	account, err := s.accounts.GetByID(ctx, id)
	if err != nil {
		return IQModelCatalog{}, err
	}
	if account.Platform != PlatformOpenAI || account.IsCredentialShadow() {
		return IQModelCatalog{}, ErrIQCheckInvalid
	}
	if s.tester == nil {
		return IQModelCatalog{}, ErrIQCheckInvalid
	}
	keyData, _ := json.Marshal([]any{id, account.Type, account.IQCheck.Revision, account.GetOpenAIBaseURL(), account.ProxyID, upstreamModelsProxyURL(account)})
	key := fmt.Sprintf("%x", sha256.Sum256(keyData))
	s.modelsMu.Lock()
	cached, exists := s.modelsCache[key]
	s.modelsMu.Unlock()
	if !refresh && exists && cached.FetchedAt != nil && time.Since(*cached.FetchedAt) < 5*time.Minute {
		cached.FromCache = true
		return cached, nil
	}
	channel := s.modelsFlight.DoChan(key, func() (any, error) {
		requestCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		local := *account
		local.Credentials = maps.Clone(account.Credentials)
		if account.Type == AccountTypeOAuth && !account.IsOpenAIAgentIdentity() && s.tokens != nil {
			expires := account.GetCredentialAsTime("expires_at")
			if !account.IsOpenAIPersonalAccessToken() && expires != nil && !time.Now().Before(*expires) && account.GetOpenAIRefreshToken() == "" {
				return nil, errors.New("authentication_unavailable")
			}
			token, err := s.tokens.GetAccessToken(requestCtx, account)
			if err != nil {
				return nil, err
			}
			if local.Credentials == nil {
				local.Credentials = map[string]any{}
			}
			local.Credentials["access_token"] = token
		}
		ids, body, err := s.tester.fetchUpstreamModelList(requestCtx, &local)
		if err != nil {
			return nil, err
		}
		_, metadata, err := extractUpstreamModelCatalog(body, false)
		if err != nil {
			return nil, err
		}

		rawEntries, _ := extractUpstreamModelRawEntries(body)
		capabilities := map[string]upstreamModelCapabilityEntry{}
		for _, raw := range rawEntries {
			var entry upstreamModelCapabilityEntry
			if json.Unmarshal(raw, &entry) == nil {
				capabilities[strings.TrimSpace(upstreamModelEntryID(entry.upstreamModelEntry))] = entry
			}
		}
		now := time.Now().UTC()
		catalog := IQModelCatalog{Models: []IQModel{}, FetchedAt: &now}
		for _, id := range ids {
			if IsGPTImageGenerationModel(id) || strings.Contains(id, "embedding") || strings.Contains(id, "whisper") || strings.Contains(id, "tts") {
				continue
			}
			m := metadata[id]
			// Preserve actual upstream presence; the shared catalog infers defaults.
			m.Reasoning = capabilities[id].Reasoning
			m.DefaultReasoningLevel = strings.TrimSpace(capabilities[id].DefaultReasoningLevel)
			m.SupportedReasoningLevels = iqRawReasoningLevels(capabilities[id])
			name := m.DisplayName
			if name == "" {
				name = id
			}
			item := IQModel{ID: id, DisplayName: name, Source: "upstream", Reasoning: m.Reasoning, SupportedReasoningLevels: m.SupportedReasoningLevels, DefaultReasoningLevel: m.DefaultReasoningLevel, CapabilitySources: map[string]string{}}
			if m.Reasoning != nil {
				item.CapabilitySources["reasoning"] = "upstream"
			}
			if len(m.SupportedReasoningLevels) > 0 {
				item.CapabilitySources["supported_reasoning_levels"] = "upstream"
			}
			if m.DefaultReasoningLevel != "" {
				item.CapabilitySources["default_reasoning_level"] = "upstream"
			}
			if id == domain.DefaultIQModel && m.Reasoning == nil && len(m.SupportedReasoningLevels) == 0 {
				item.SupportedReasoningLevels = []string{"low", "medium", "high", "xhigh", "max"}
				item.CapabilitySources["supported_reasoning_levels"] = "reference"
			}
			catalog.Models = append(catalog.Models, item)
		}
		s.modelsMu.Lock()
		defer s.modelsMu.Unlock()
		if s.modelsCache == nil {
			s.modelsCache = map[string]IQModelCatalog{}
		}
		for k, value := range s.modelsCache {
			if value.FetchedAt == nil || now.Sub(*value.FetchedAt) > 24*time.Hour {
				delete(s.modelsCache, k)
			}
		}
		if len(s.modelsCache) >= 256 {
			oldest := ""
			for k, v := range s.modelsCache {
				if oldest == "" || v.FetchedAt.Before(*s.modelsCache[oldest].FetchedAt) {
					oldest = k
				}
			}
			delete(s.modelsCache, oldest)
		}
		s.modelsCache[key] = catalog
		return catalog, nil
	})
	select {
	case <-ctx.Done():
		return IQModelCatalog{}, ctx.Err()
	case result := <-channel:
		if result.Err != nil {
			if exists && cached.FetchedAt != nil && time.Since(*cached.FetchedAt) < 24*time.Hour {
				cached.Stale = true
				cached.FromCache = true
				cached.Error = "model_discovery_failed"
				return cached, nil
			}
			return IQModelCatalog{Models: []IQModel{}, Error: "model_discovery_failed"}, nil
		}
		catalog, ok := result.Val.(IQModelCatalog)
		if !ok {
			return IQModelCatalog{}, errors.New("unexpected model catalog result type")
		}
		return catalog, nil
	}
}

// IQ configuration accepts custom effort tokens. Do not discard or alias values
// merely because the shared business-model catalog does not recognize them.
func iqRawReasoningLevels(entry upstreamModelCapabilityEntry) []string {
	levels := []string{}
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		settings := domain.IQCheckSettings{ReasoningEffort: &value}
		if settings.Validate() == nil && value != "upstream_default" && !seen[value] {
			seen[value] = true
			levels = append(levels, value)
		}
	}
	for _, raw := range entry.SupportedReasoningLevels {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			add(value)
			continue
		}
		var object struct {
			Effort string `json:"effort"`
		}
		if json.Unmarshal(raw, &object) == nil {
			add(object.Effort)
		}
	}
	if len(levels) == 0 {
		for _, option := range entry.ReasoningOptions {
			if strings.EqualFold(strings.TrimSpace(option.Type), "effort") {
				for _, value := range option.Values {
					if text, ok := value.(string); ok {
						add(text)
					}
				}
			}
		}
	}
	return levels
}
