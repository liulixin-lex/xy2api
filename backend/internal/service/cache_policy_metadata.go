package service

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
)

// Only newly granted account scope is checked; removals must always work even
// if an old account was deleted, changed type, or the account store is down.
func (s *CachePolicyService) validateCacheMetadataAccounts(ctx context.Context, v CachePolicyValidation) (CachePolicyValidation, error) {
	if !v.Valid || v.Direction == "disable" {
		return v, nil
	}
	ids := []int64{}
	for _, d := range v.Diff {
		if d.Field != "metadata_account_ids" {
			continue
		}
		before, _ := d.Before.([]int64)
		after, _ := d.After.([]int64)
		for _, id := range after {
			if !slices.Contains(before, id) {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return v, nil
	}
	if s.accounts == nil {
		return v, fmt.Errorf("cache policy account validation unavailable")
	}
	accounts, err := s.accounts.GetByIDs(ctx, ids)
	if err != nil {
		return v, fmt.Errorf("validate cache metadata accounts: %w", err)
	}
	found := make(map[int64]*Account, len(accounts))
	for _, a := range accounts {
		if a != nil {
			found[a.ID] = a
		}
	}
	for _, id := range ids {
		a := found[id]
		code := ""
		switch {
		case a == nil:
			code = "unknown_account"
		case a.Platform != PlatformAnthropic:
			code = "wrong_platform"
		case a.Type != AccountTypeAPIKey:
			code = "not_apikey"
		case !cacheMetadataEndpointAllowed(a.GetBaseURL()):
			code = "official_endpoint"
		}
		if code != "" {
			v.Errors = append(v.Errors, CachePolicyFieldError{Field: "metadata_account_ids", Code: code})
		}
	}
	v.Valid = len(v.Errors) == 0
	if !v.Valid {
		v.Warnings = []string{}
		v.EffectivePreview = nil
		v.Affected = []CachePolicyAffected{}
	}
	return v, nil
}

func cachePolicyMetadataAccountEligible(a *Account) bool {
	return a != nil && a.Platform == PlatformAnthropic && a.Type == AccountTypeAPIKey && cacheMetadataEndpointAllowed(a.GetBaseURL())
}

// Administrative candidates are response metadata, never request-path inputs.
func (s *CachePolicyService) cacheMetadataAccountOptions(ctx context.Context) ([]cachePolicyAccountOption, error) {
	options := []cachePolicyAccountOption{}
	lookup, ok := s.accounts.(cachePolicyAccountPageLookup)
	if !ok {
		return options, errCachePolicyAccountOptionsUnavailable
	}
	seen := map[int64]bool{}
	for page := 1; len(options) < cachePolicyAccountOptionLimit; page++ {
		params := pagination.PaginationParams{Page: page, PageSize: cachePolicyAccountOptionLimit, SortBy: "id", SortOrder: pagination.SortOrderAsc}
		accounts, _, err := lookup.ListWithFilters(ctx, params, PlatformAnthropic, AccountTypeAPIKey, "", "", 0, "")
		if err != nil {
			return []cachePolicyAccountOption{}, errCachePolicyAccountOptionsUnavailable
		}
		added := false
		for i := range accounts {
			a := &accounts[i]
			if a.ID <= 0 || seen[a.ID] {
				continue
			}
			seen[a.ID] = true
			added = true
			if !cachePolicyMetadataAccountEligible(a) {
				continue
			}
			options = append(options, cachePolicyAccountOption{a.ID, a.Name, a.Platform})
			if len(options) == cachePolicyAccountOptionLimit {
				break
			}
		}
		if len(accounts) < params.PageSize {
			break
		}
		if !added {
			return []cachePolicyAccountOption{}, errCachePolicyAccountOptionsUnavailable
		}
	}
	sort.Slice(options, func(i, j int) bool { return options[i].ID < options[j].ID })
	return options, nil
}
