package service

import (
	"context"
	"fmt"
	"sort"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
)

const cachePolicyAccountOptionLimit = 500

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
			return nil, fmt.Errorf("list cache policy account options: %w", err)
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

func setCachePolicyAccountOptions(view *CachePolicyView, options []cachePolicyAccountOption) {
	for _, field := range view.Schema {
		if field["field"] == "account_ids" {
			field["options"] = options
			return
		}
	}
}
