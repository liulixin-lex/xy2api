package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// selectionNegativeKey keys the selection-level negative cache by group and
// model; the group fixes the platform except for composite groups, whose
// target platform depends on the model that is part of the key.
func selectionNegativeKey(groupID *int64, model string) string {
	return resilience.NegativeKey(derefGroupID(groupID), "select", model)
}

// errNegativeCached marks a no-account result replayed from the negative
// cache.
var errNegativeCached = errors.New("no available account (cached)")

// resilienceNegativeCacheHit reports a cached "no available account" for the
// first attempt of a request (design §9.2).
//
// Profit-controlled groups decide per request (the downstream rate is
// frozen per user and request), so their results are never cached.
func resilienceNegativeCacheHit(ctx context.Context, groupID *int64, model string, excludedIDs map[int64]struct{}) error {
	cfg := resilience.Current()
	if !cfg.ErrorsOn() || groupID == nil || len(excludedIDs) > 0 || gatewayProfitControlGateActive(ctx) {
		return nil
	}
	if _, ok := resilience.ProcessNegativeCache().Get(selectionNegativeKey(groupID, model), time.Now()); ok {
		return fmt.Errorf("%w supporting model: %s (%v)", ErrNoAvailableAccounts, model, errNegativeCached)
	}
	return nil
}

// profitRejectedPattern matches selection diagnostics in which profit
// control removed accounts; the zero counters are always part of the text.
var profitRejectedPattern = regexp.MustCompile(`profit_(?:threshold|invalid_account_rate)=[1-9]`)

// resilienceNegativeCachePut stores a final no-account result of a first
// attempt.
func resilienceNegativeCachePut(ctx context.Context, groupID *int64, model string, excludedIDs map[int64]struct{}, err error) {
	cfg := resilience.Current()
	if !cfg.ErrorsOn() || groupID == nil || len(excludedIDs) > 0 || !isNoAccountSelectionError(err) || errors.Is(err, errNegativeCached) ||
		gatewayProfitControlGateActive(ctx) || profitRejectedPattern.MatchString(err.Error()) {
		return
	}
	ttl := time.Duration(cfg.Errors.NegativeCacheSeconds) * time.Second
	resilience.ProcessNegativeCache().Put(selectionNegativeKey(groupID, model), derefGroupID(groupID), resilience.CachedResponse{}, ttl, time.Now())
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
