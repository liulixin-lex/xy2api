package service

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// ResilienceExplanation shows how gateway resilience sees a group and, when
// given, one session (design §4, tasks 3.5). It is read-only and the same
// for share and original scheduling: penalty tiers, the session's failure
// counts and avoid set, the last-resort pick when nothing is healthy and the
// availability fallback decision.
type ResilienceExplanation struct {
	GroupID int64 `json:"group_id"`
	Enabled bool  `json:"enabled"`
	// ModelRestriction is set when the group rejects the model before any
	// account is chosen: "not_allowed" (model allowlist) or
	// "channel_pricing" (channel pricing list). Every request then fails
	// whatever the accounts' state.
	ModelRestriction string                         `json:"model_restriction,omitempty"`
	HealthyCount     int                            `json:"healthy_count"`
	Accounts         []ResilienceAccountExplanation `json:"accounts"`
	LastResort       *ResilienceLastResortChoice    `json:"last_resort,omitempty"`
	FallbackGroup    *ResilienceFallbackExplanation `json:"fallback_group,omitempty"`
	Session          *ResilienceSessionExplanation  `json:"session,omitempty"`
}

// ResilienceAccountExplanation is one account of the group.
type ResilienceAccountExplanation struct {
	AccountID       int64      `json:"account_id"`
	Name            string     `json:"name"`
	Platform        string     `json:"platform"`
	Priority        int        `json:"priority"`
	Tier            string     `json:"tier"`
	Reason          string     `json:"reason,omitempty"`
	RecoverAt       *time.Time `json:"recover_at,omitempty"`
	BalanceState    string     `json:"balance_state,omitempty"`
	SessionFailures int64      `json:"session_failures,omitempty"`
	Avoided         bool       `json:"avoided,omitempty"`
}

// ResilienceLastResortChoice is the account last-resort selection would try
// first. It ignores per-request gates (model, channel, quota), so the actual
// pick may be a later candidate.
type ResilienceLastResortChoice struct {
	AccountID int64      `json:"account_id"`
	RecoverAt *time.Time `json:"recover_at,omitempty"`
}

// ResilienceFallbackExplanation is the availability fallback decision.
type ResilienceFallbackExplanation struct {
	GroupID         int64  `json:"group_id"`
	Name            string `json:"name"`
	Usable          bool   `json:"usable"`
	FallbackHealthy bool   `json:"fallback_healthy"`
	WouldMove       bool   `json:"would_move"`
}

// ResilienceSessionExplanation summarises the session part.
type ResilienceSessionExplanation struct {
	Failures int     `json:"failures"`
	Avoided  []int64 `json:"avoided"`
}

// ErrResilienceExplainUnavailable reports a service without its repositories.
var ErrResilienceExplainUnavailable = errors.New("gateway resilience explain is not configured")

// Explain describes the group as resilience sees it now.
func (r *ResilienceService) Explain(ctx context.Context, groupID int64, sessionHash, model string) (*ResilienceExplanation, error) {
	if r == nil || r.fallbackAccounts == nil || r.fallbackGroups == nil {
		return nil, ErrResilienceExplainUnavailable
	}
	group, err := r.fallbackGroups.GetByIDLite(ctx, groupID)
	if err != nil {
		return nil, err
	}
	accounts, err := r.fallbackAccounts.ListByGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := &ResilienceExplanation{GroupID: groupID, Enabled: resilience.Current().Enabled, Accounts: []ResilienceAccountExplanation{}}
	if model != "" {
		switch {
		case group != nil && !group.ModelAllowlist.Allows(model):
			out.ModelRestriction = "not_allowed"
		case r.pricingRestricted != nil && r.pricingRestricted(ctx, groupID, model):
			out.ModelRestriction = "channel_pricing"
		}
	}

	var failures map[int64]int64
	var avoided map[int64]struct{}
	if sessionHash != "" && r.store != nil {
		failures, _ = r.store.SessionFailures(ctx, groupID, sessionHash)
		avoided, _ = r.store.SessionAvoid(ctx, groupID, sessionHash)
		session := &ResilienceSessionExplanation{Avoided: []int64{}}
		for id := range avoided {
			session.Avoided = append(session.Avoided, id)
		}
		sort.Slice(session.Avoided, func(i, j int) bool { return session.Avoided[i] < session.Avoided[j] })
		for _, n := range failures {
			session.Failures += int(n)
		}
		out.Session = session
	}

	var cands []lastResortCandidate
	for i := range accounts {
		acc := accounts[i]
		tier, recoverAt, reason := resiliencePenaltyOf(&acc, model, now)
		e := ResilienceAccountExplanation{AccountID: acc.ID, Name: acc.Name, Platform: acc.Platform, Priority: acc.Priority,
			Tier: tierName(tier), Reason: reason, SessionFailures: failures[acc.ID]}
		if !recoverAt.IsZero() {
			at := recoverAt
			e.RecoverAt = &at
		}
		if state, _ := opsBalanceState(&acc); state != "" {
			e.BalanceState = state
		}
		if _, ok := avoided[acc.ID]; ok {
			e.Avoided = true
		}
		if tier == resilience.TierNone && !e.Avoided {
			out.HealthyCount++
		}
		if lastResortTierAllowed(tier, acc.ID, avoided) {
			candidate := acc
			cands = append(cands, lastResortCandidate{account: &candidate, recoverAt: recoverAt})
		}
		out.Accounts = append(out.Accounts, e)
	}
	sort.SliceStable(out.Accounts, func(i, j int) bool {
		if out.Accounts[i].Priority != out.Accounts[j].Priority {
			return out.Accounts[i].Priority < out.Accounts[j].Priority
		}
		return out.Accounts[i].AccountID < out.Accounts[j].AccountID
	})
	if out.HealthyCount == 0 && resilience.Current().LastResortOn() {
		if picked := pickLastResort(cands); len(picked) > 0 {
			choice := &ResilienceLastResortChoice{AccountID: picked[0].account.ID}
			if !picked[0].recoverAt.IsZero() {
				at := picked[0].recoverAt
				choice.RecoverAt = &at
			}
			out.LastResort = choice
		}
	}

	if group != nil && group.AvailabilityFallbackGroupID != nil {
		fb := &ResilienceFallbackExplanation{GroupID: *group.AvailabilityFallbackGroupID}
		if fallback := r.loadFallbackGroup(ctx, *group.AvailabilityFallbackGroupID, now); fallback != nil {
			fb.Name = fallback.Name
			fb.Usable = resilience.Current().FallbackGroupOn() && fallback.Status == StatusActive &&
				fallback.Platform == group.Platform && fallback.SubscriptionType != SubscriptionTypeSubscription &&
				group.SubscriptionType != SubscriptionTypeSubscription
			fb.FallbackHealthy = r.groupHasHealthyAccount(ctx, fallback.ID, now)
			fb.WouldMove = fb.Usable && fb.FallbackHealthy && !r.groupHasHealthyAccount(ctx, groupID, now)
		}
		out.FallbackGroup = fb
	}
	return out, nil
}

func tierName(t resilience.PenaltyTier) string {
	switch t {
	case resilience.TierHard:
		return "hard"
	case resilience.TierSoft:
		return "soft"
	}
	return "none"
}
