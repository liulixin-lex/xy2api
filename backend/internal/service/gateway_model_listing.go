package service

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
)

// AvailableModelCatalog preserves an authoritative empty capability intersection.
// RestrictFallback prevents handlers from reintroducing unsupported defaults,
// including through group allowlists and Codex/composite catalogs.
type AvailableModelCatalog struct {
	Models           []string
	RestrictFallback bool
}

type gatewayModelsListCacheEntry struct {
	catalog         AvailableModelCatalog
	profileRevision string
}

type gatewayModelOwnershipCacheEntry struct {
	ownership       CompositeModelOwnership
	profileRevision string
}

func (s *GatewayService) GetAvailableModelCatalog(ctx context.Context, groupID *int64, platform string) AvailableModelCatalog {
	platform = strings.TrimSpace(platform)
	cacheKey := modelsListCacheKey(groupID, platform)
	// Profile revisions version cached capabilities; they do not migrate signed
	// history. Account edits still require invalidation or the short cache TTL.
	revision := claudeCodeV1CompatProfile.Revision
	s.modelsListCacheMu.Lock()
	generation := s.modelsListGeneration
	if s.modelsListCache != nil {
		if cached, found := s.modelsListCache.Get(cacheKey); found {
			if entry, ok := cached.(gatewayModelsListCacheEntry); ok && entry.profileRevision == revision {
				s.modelsListCacheMu.Unlock()
				modelsListCacheHitTotal.Add(1)
				entry.catalog.Models = slices.Clone(entry.catalog.Models)
				return entry.catalog
			}
		}
	}
	s.modelsListCacheMu.Unlock()
	modelsListCacheMissTotal.Add(1)

	var accounts []Account
	var err error
	if platform == PlatformAnthropic {
		// A cooldown must not remove the only profile and unlock static defaults.
		accounts, err = s.accountRepo.ListModelAvailabilityCandidates(ctx, groupID, []string{PlatformAnthropic}, groupID == nil)
	} else if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupID(ctx, *groupID)
	} else {
		accounts, err = s.accountRepo.ListSchedulable(ctx)
	}
	if err != nil {
		return AvailableModelCatalog{RestrictFallback: platform == PlatformAnthropic || platform == ""}
	}
	if platform == "" {
		configured, lookupErr := s.accountRepo.ListModelAvailabilityCandidates(ctx, groupID, []string{PlatformAnthropic}, groupID == nil)
		if lookupErr != nil {
			return AvailableModelCatalog{RestrictFallback: true}
		}
		other := make([]Account, 0, len(accounts))
		for _, account := range accounts {
			if account.Platform != PlatformAnthropic {
				other = append(other, account)
			}
		}
		accounts = append(other, configured...)
	}
	if len(accounts) == 0 {
		return AvailableModelCatalog{}
	}
	filtered := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if platform == "" || account.Platform == platform || mixedListingAccountAllowed(platform, &account) {
			filtered = append(filtered, account)
		}
	}
	accounts = filtered
	catalog := AvailableModelCatalog{}
	modelSet := make(map[string]struct{})
	for i := range accounts {
		account := &accounts[i]
		profile, profileErr := ResolveAnthropicCompatProfile(account)
		if profileErr != nil || profile != nil {
			catalog.RestrictFallback = true
			if profileErr == nil {
				for _, model := range anthropicCompatPublicModels(account, profile) {
					modelSet[model] = struct{}{}
				}
			}
			continue
		}
		if platform == PlatformOpenAI && account.IsOpenAIPassthroughEnabled() {
			continue
		}
		for model := range account.GetModelMapping() {
			if platform != "" && account.Platform != platform && !mixedListingModelAllowed(platform, model) {
				continue
			}
			modelSet[model] = struct{}{}
		}
	}
	if catalog.RestrictFallback {
		// An ordinary unmapped Anthropic account can still supply defaults in a
		// mixed pool. Compatibility accounts only contribute their intersection.
		for i := range accounts {
			account := &accounts[i]
			profile, err := ResolveAnthropicCompatProfile(account)
			if err == nil && profile == nil && account.Platform == PlatformAnthropic && len(account.GetModelMapping()) == 0 {
				for _, model := range claude.DefaultModels {
					modelSet[model.ID] = struct{}{}
				}
			}
		}
	}
	if len(modelSet) > 0 || catalog.RestrictFallback {
		catalog.Models = make([]string, 0, len(modelSet))
		for model := range modelSet {
			catalog.Models = append(catalog.Models, model)
		}
		sort.Strings(catalog.Models)
		if platform == PlatformOpenAI {
			catalog.Models = supplementUnmappedOpenAIModels(accounts, catalog.Models)
		}
	}
	s.modelsListCacheMu.Lock()
	if s.modelsListCache != nil && generation == s.modelsListGeneration {
		entry := gatewayModelsListCacheEntry{catalog: catalog, profileRevision: revision}
		entry.catalog.Models = slices.Clone(catalog.Models)
		s.modelsListCache.Set(cacheKey, entry, s.modelsListCacheTTL)
		modelsListCacheStoreTotal.Add(1)
	}
	s.modelsListCacheMu.Unlock()
	return catalog
}

func anthropicCompatPublicModels(account *Account, profile *AnthropicCompatProfile) []string {
	mapping := account.GetModelMapping()
	candidates := make(map[string]struct{}, len(mapping))
	includeDefaults := len(mapping) == 0
	for model := range mapping {
		if strings.Contains(model, "*") {
			includeDefaults = true
		} else {
			candidates[model] = struct{}{}
		}
	}
	if includeDefaults {
		for _, model := range claude.DefaultModels {
			candidates[model.ID] = struct{}{}
		}
	}
	models := make([]string, 0, len(candidates))
	for model := range candidates {
		if account.IsModelSupported(model) && profile.SupportsModel(account.GetMappedModel(model)) {
			models = append(models, model)
		}
	}
	return models
}
