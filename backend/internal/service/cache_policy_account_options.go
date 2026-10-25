package service

import (
	"context"
	"errors"
	"sort"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
)

const cachePolicyAccountOptionLimit = 500
const cachePolicyAccountOptionsUnavailable = "account_options_unavailable"

var errCachePolicyAccountOptionsUnavailable = errors.New("cache policy account options unavailable")

// Optional administrator-only capability on the already injected repository.
// Keep lookup-only test/minimal services valid without widening their interface.
type cachePolicyAccountPageLookup interface {
	ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, string, int64, string) ([]Account, *pagination.PaginationResult, error)
}

type cachePolicyAccountOption struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

func cachePolicyUpstreamAccountEligible(account *Account) bool {
	return account != nil && account.IsOpenAIApiKey() &&
		!account.UsesOpenAICodexProtocol() && !account.IsOpenAIPassthroughEnabled() &&
		account.Platform != PlatformGrok && account.GetCodexFingerprintMode() == codexFingerprintOff
}

// These are configuration candidates, so inactive and temporarily unschedulable
// accounts remain selectable. Request snapshots and scheduling never call this.
func (s *CachePolicyService) upstreamIdentityAccountOptions(ctx context.Context) ([]cachePolicyAccountOption, error) {
	options := []cachePolicyAccountOption{}
	lookup, ok := s.accounts.(cachePolicyAccountPageLookup)
	if !ok {
		return options, nil
	}
	seen := make(map[int64]bool)
	for page := 1; len(options) < cachePolicyAccountOptionLimit; page++ {
		params := pagination.PaginationParams{Page: page, PageSize: cachePolicyAccountOptionLimit, SortBy: "id", SortOrder: pagination.SortOrderAsc}
		accounts, _, err := lookup.ListWithFilters(ctx, params, PlatformOpenAI, AccountTypeAPIKey, "", "", 0, "")
		if err != nil {
			// Never propagate provider/database details to an admin response or log.
			// A failed later page invalidates the entire candidate snapshot.
			return []cachePolicyAccountOption{}, errCachePolicyAccountOptionsUnavailable
		}
		for i := range accounts {
			account := &accounts[i]
			if account.ID <= 0 || seen[account.ID] || !cachePolicyUpstreamAccountEligible(account) {
				continue
			}
			seen[account.ID] = true
			// Never marshal Account: credentials, extra, proxy and group state
			// must not become part of this narrowly defined admin response.
			options = append(options, cachePolicyAccountOption{account.ID, account.Name, account.Platform})
			if len(options) == cachePolicyAccountOptionLimit {
				break
			}
		}
		if len(accounts) < params.PageSize {
			break
		}
	}
	sort.Slice(options, func(i, j int) bool { return options[i].ID < options[j].ID })
	return options, nil
}

// grok.account_ids (L-17) candidates: every Grok account, OAuth and API key.
func (s *CachePolicyService) grokIdentityAccountOptions(ctx context.Context) ([]cachePolicyAccountOption, error) {
	options := []cachePolicyAccountOption{}
	lookup, ok := s.accounts.(cachePolicyAccountPageLookup)
	if !ok {
		return options, nil
	}
	seen := make(map[int64]bool)
	for page := 1; len(options) < cachePolicyAccountOptionLimit; page++ {
		params := pagination.PaginationParams{Page: page, PageSize: cachePolicyAccountOptionLimit, SortBy: "id", SortOrder: pagination.SortOrderAsc}
		accounts, _, err := lookup.ListWithFilters(ctx, params, PlatformGrok, "", "", "", 0, "")
		if err != nil {
			return []cachePolicyAccountOption{}, errCachePolicyAccountOptionsUnavailable
		}
		for i := range accounts {
			account := &accounts[i]
			if account.ID <= 0 || seen[account.ID] || account.Platform != PlatformGrok {
				continue
			}
			seen[account.ID] = true
			options = append(options, cachePolicyAccountOption{account.ID, account.Name, account.Platform})
			if len(options) == cachePolicyAccountOptionLimit {
				break
			}
		}
		if len(accounts) < params.PageSize {
			break
		}
	}
	sort.Slice(options, func(i, j int) bool { return options[i].ID < options[j].ID })
	return options, nil
}

func setCachePolicyAccountOptions(view *CachePolicyView, options, grokOptions []cachePolicyAccountOption) {
	for _, field := range view.Schema {
		switch field["field"] {
		case "account_ids":
			field["options"] = options
		case "grok.account_ids":
			field["options"] = grokOptions
		}
	}
}

// Options are response-only picker metadata, not a prerequisite for reading or
// committing policy state. The warning never enters runtime snapshots/validate.
// One warning covers both account pickers of upstream_identity.
func (s *CachePolicyService) populateCachePolicyAccountOptions(ctx context.Context, view *CachePolicyView) {
	options, err := s.upstreamIdentityAccountOptions(ctx)
	if err != nil {
		setCachePolicyUnavailableAccountOptions(view)
		return
	}
	grokOptions, err := s.grokIdentityAccountOptions(ctx)
	if err != nil {
		setCachePolicyUnavailableAccountOptions(view)
		return
	}
	setCachePolicyAccountOptions(view, options, grokOptions)
}

func setCachePolicyUnavailableAccountOptions(view *CachePolicyView) {
	setCachePolicyAccountOptions(view, []cachePolicyAccountOption{}, []cachePolicyAccountOption{})
	view.Effective.Warnings = append(view.Effective.Warnings, cachePolicyAccountOptionsUnavailable)
}
