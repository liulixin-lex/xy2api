package service

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// ResilienceSameHostAccountIDs returns the other accounts of the request's
// group that reach the same upstream host as accountID. After capacity
// shedding the gateway may only switch to a different host (design §6.3).
func (s *GatewayService) ResilienceSameHostAccountIDs(ctx context.Context, accountID int64) []int64 {
	if s == nil || s.resilience == nil || s.accountRepo == nil {
		return nil
	}
	return s.resilience.sameHostAccountIDs(ctx, s.accountRepo, accountID)
}

// ResilienceSameHostAccountIDs is the OpenAI-gateway variant.
func (s *OpenAIGatewayService) ResilienceSameHostAccountIDs(ctx context.Context, accountID int64) []int64 {
	if s == nil || s.resilience == nil || s.accountRepo == nil {
		return nil
	}
	return s.resilience.sameHostAccountIDs(ctx, s.accountRepo, accountID)
}

func (r *ResilienceService) sameHostAccountIDs(ctx context.Context, repo AccountRepository, accountID int64) []int64 {
	groupID := resilience.StateFrom(ctx).GroupID()
	if groupID <= 0 {
		return nil
	}
	accounts, err := r.groupAccountsForLastResort(ctx, repo, groupID)
	if err != nil {
		return nil
	}
	host := ""
	for i := range accounts {
		if accounts[i].ID == accountID {
			host = accountUpstreamHost(&accounts[i])
			break
		}
	}
	if host == "" {
		return nil
	}
	var ids []int64
	for i := range accounts {
		if accounts[i].ID != accountID && accountUpstreamHost(&accounts[i]) == host {
			ids = append(ids, accounts[i].ID)
		}
	}
	return ids
}

// accountUpstreamHost identifies the upstream an account reaches: the
// base_url host of API-key accounts, the platform's official endpoint for
// OAuth-style accounts.
func accountUpstreamHost(a *Account) string {
	if a == nil {
		return ""
	}
	if a.Type != AccountTypeAPIKey {
		return a.Platform + "|official"
	}
	u, err := url.Parse(strings.TrimSpace(a.GetBaseURL()))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return a.Platform + "|" + strings.ToLower(u.Hostname())
}

// resilienceStatusOverloaded is Anthropic's capacity-shedding status.
const resilienceStatusOverloaded = 529

// resilienceInServiceRetryAllowed gates the in-service same-account retries
// of the Anthropic-style forwarders (design §6.2). Under a request budget only
// capacity shedding (529) is retried in place, at most
// budget.same_account_retry_max times per account, and each retry draws from
// the request budget; other failures go back to the handler, which switches
// accounts. Without a budget the 0.3.0 rules apply.
func resilienceInServiceRetryAllowed(ctx context.Context, accountID int64, status int) bool {
	b := resilience.StateFrom(ctx).Budget()
	if b == nil {
		return true
	}
	if status != resilienceStatusOverloaded {
		return false
	}
	return b.ConsumeInPlaceRetry(accountID, true, time.Now())
}

func resilienceInServiceRetryAllowedWithBody(ctx context.Context, accountID int64, status int, body []byte) bool {
	if isUnrecoverableAccountFailure(status, body) {
		return false
	}
	return resilienceInServiceRetryAllowed(ctx, accountID, status)
}

func isUnrecoverableAccountFailure(status int, body []byte) bool {
	switch classifyUpstreamHTTP(status, nil, body, "").Category {
	case resilience.CategoryBalance, resilience.CategoryAuth, resilience.CategoryModelUnsupported:
		return true
	default:
		return false
	}
}
