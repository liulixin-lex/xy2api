package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// selectionNegativeKey keys the selection-level negative cache by group,
// model and selection variant. The group fixes the platform except for
// composite groups, whose target platform depends on the model in the key;
// the variant separates OpenAI selections that need a different transport,
// endpoint capability or platform, so one result never answers another.
func selectionNegativeKey(groupID *int64, model, variant string) string {
	scope := "select"
	if variant != "" {
		scope += ":" + variant
	}
	return resilience.NegativeKey(derefGroupID(groupID), scope, model)
}

// errNegativeCached marks a no-account result replayed from the negative
// cache.
var errNegativeCached = errors.New("no available account (cached)")

// resilienceNegativeCacheHit reports a cached "no available account" for the
// first attempt of a request (design §9.2).
//
// Profit-controlled groups decide per request (the downstream rate is
// frozen per user and request), so their results are never cached.
func resilienceNegativeCacheHit(ctx context.Context, groupID *int64, model, variant string, excludedIDs map[int64]struct{}) error {
	cfg := resilience.Current()
	if !cfg.ErrorsOn() || groupID == nil || len(excludedIDs) > 0 || gatewayProfitControlGateActive(ctx) {
		return nil
	}
	if _, ok := resilience.ProcessNegativeCache().Get(selectionNegativeKey(groupID, model, variant), time.Now()); ok {
		return fmt.Errorf("%w supporting model: %s (%w)", ErrNoAvailableAccounts, model, errNegativeCached)
	}
	return nil
}

// profitRejectedPattern matches selection diagnostics in which profit
// control removed accounts; the zero counters are always part of the text.
var profitRejectedPattern = regexp.MustCompile(`profit_(?:threshold|invalid_account_rate)=[1-9]`)

// resilienceNegativeCachePut stores a final no-account result of a first
// attempt.
func resilienceNegativeCachePut(ctx context.Context, groupID *int64, model, variant string, excludedIDs map[int64]struct{}, err error) {
	cfg := resilience.Current()
	if !cfg.ErrorsOn() || groupID == nil || len(excludedIDs) > 0 || !isNoAccountSelectionError(err) || errors.Is(err, errNegativeCached) ||
		gatewayProfitControlGateActive(ctx) || profitRejectedPattern.MatchString(err.Error()) {
		return
	}
	ttl := time.Duration(cfg.Errors.NegativeCacheSeconds) * time.Second
	resilience.ProcessNegativeCache().Put(selectionNegativeKey(groupID, model, variant), derefGroupID(groupID), resilience.CachedResponse{}, ttl, time.Now())
}

// markResilienceConfigError records configuration-caused selection
// failures on the request state.
func markResilienceConfigError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, ErrClaudeCodeOnly) || (errors.Is(err, ErrNoAvailableAccounts) && strings.Contains(err.Error(), "channel pricing restriction")) {
		resilience.StateFrom(ctx).MarkConfigError()
	}
}

// resilienceStreamErrorStatus maps the error.type of an Anthropic in-stream
// error event to the HTTP status it stands for: rate_limit_error is a 429 and
// overloaded_error a 529 whether or not output was written, api_error a 502,
// and any other type keeps the 0.3.0 403 (which clients saw as "Upstream
// access forbidden"). The status drives classification and the client's
// answer; a written stream still never switches accounts.
//
// The returned headers carry the account side effects of that status, nil
// when none apply: only before any output, for overload (as in 0.3.0) and
// rate limits. A rate limit is applied without the stream's headers, whose
// Anthropic window values describe a successful answer and would read as an
// exhausted window. With the switch off only a pre-output overloaded_error
// is a 529, as in 0.3.0.
func resilienceStreamErrorStatus(errType string, outputWritten bool, header http.Header) (int, http.Header) {
	errType = strings.TrimSpace(errType)
	status := http.StatusForbidden
	if resilience.Current().FailureOn() {
		switch errType {
		case "overloaded_error":
			status = 529
		case "rate_limit_error":
			status = http.StatusTooManyRequests
		case "api_error":
			status = http.StatusBadGateway
		}
	} else if !outputWritten && errType == "overloaded_error" {
		status = 529
	}
	if outputWritten {
		return status, nil
	}
	switch status {
	case 529:
		return status, header.Clone()
	case http.StatusTooManyRequests:
		return status, http.Header{}
	}
	return status, nil
}

// NoAccountRetryAfter estimates when an account of the group may serve the
// model again: the earliest end of a soft penalty, a rate-limit reset or a
// balance re-check. It returns zero when nothing in the group will recover
// on its own (design §9.1). The caller clamps the value.
func (s *GatewayService) NoAccountRetryAfter(ctx context.Context, groupID *int64, model string) time.Duration {
	if s == nil || s.resilience == nil || groupID == nil || s.accountRepo == nil {
		return 0
	}
	return s.resilience.noAccountRetryAfter(ctx, s.accountRepo, *groupID, model)
}

// NoAccountRetryAfter is the OpenAI-scheduler variant.
func (s *OpenAIGatewayService) NoAccountRetryAfter(ctx context.Context, groupID *int64, model string) time.Duration {
	if s == nil || s.resilience == nil || groupID == nil || s.accountRepo == nil {
		return 0
	}
	return s.resilience.noAccountRetryAfter(ctx, s.accountRepo, *groupID, model)
}

func (r *ResilienceService) noAccountRetryAfter(ctx context.Context, repo AccountRepository, groupID int64, model string) time.Duration {
	accounts, err := r.groupAccountsForLastResort(ctx, repo, groupID)
	if err != nil {
		return 0
	}
	now := time.Now()
	var earliest time.Time
	consider := func(t time.Time) {
		if t.After(now) && (earliest.IsZero() || t.Before(earliest)) {
			earliest = t
		}
	}
	for i := range accounts {
		a := &accounts[i]
		if !a.IsActive() || !a.Schedulable {
			continue
		}
		tier, recoverAt, _ := resiliencePenaltyOf(a, model, now)
		switch tier {
		case resilience.TierSoft:
			consider(recoverAt)
		case resilience.TierNone:
			// Healthy but busy (concurrency, session limits): soon.
			consider(now.Add(time.Second))
		case resilience.TierHard:
			if a.RateLimitResetAt != nil {
				consider(*a.RateLimitResetAt)
			}
			if rec := resilience.BalanceRecordFrom(a.Extra); rec.State == resilience.BalanceExhausted {
				consider(rec.NextCheckAt)
			}
		}
	}
	if earliest.IsZero() {
		return 0
	}
	return earliest.Sub(now)
}
