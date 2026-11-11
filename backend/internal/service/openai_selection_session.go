package service

import (
	"context"
	"sort"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type selectionSessionSlots interface {
	tryAcquireAccountSlot(context.Context, int64, int) (*AcquireResult, error)
}

// selectionSession receives already admitted candidates. Recheck repeats fresh
// account admission before and after acquiring a slot; view always reads latest.
type selectionSession struct {
	view       func() schedquality.View
	now        func() time.Time
	slots      selectionSessionSlots
	cache      OpenAIStickyRebindCache
	cacheKey   func(string) string
	bindings   gatewaySessionBindingReader
	marks      *fullMarks
	bindingTTL time.Duration
	recheck    func(context.Context, *Account) *Account
}

func (s *selectionSession) takeoverReady(ctx context.Context, view schedquality.View, groupID int64, key string, owner int64, now time.Time) bool {
	if !selectionAuto(view, now) {
		return false
	}
	idle, enabled := view.TakeoverIdle(groupID)
	if !enabled || idle < 0 {
		return false
	}
	if idle == 0 {
		return true
	}
	if s.bindings == nil {
		return false
	}
	id, remaining, err := s.bindings.GetSessionAccountIDWithTTL(ctx, groupID, key)
	return err == nil && id == owner && remaining > 0 && s.bindingTTL-remaining >= idle
}

func (s *selectionSession) candidateAllowed(view schedquality.View, groupID int64, model string, owner, target *Account, migrate bool, now time.Time) bool {
	state := selectionState(view, groupID, target, model)
	auto := selectionAuto(view, now)
	if migrate {
		return schedquality.StandingOf(state, auto, now) == schedquality.StandFront
	}
	return target.Priority < owner.Priority && schedquality.CanTakeOver(state, auto, now) && !s.marks.Recently(target.ID, now)
}

func (s *selectionSession) selectOwner(ctx context.Context, groupID int64, sessionHash, model string, owner *Account, candidates []*Account, req selectionRequest) *AccountSelectionResult {
	if owner == nil || s.cache == nil || s.cacheKey == nil || openAINoStickyMigration(ctx) || groupQualityGateExempt(ctx) {
		return nil
	}
	key := s.cacheKey(sessionHash)
	if key == "" {
		return nil
	}
	now, view := s.now(), s.view()
	migrate := schedquality.StandingOf(selectionState(view, groupID, owner, model), selectionAuto(view, now), now) == schedquality.StandBack
	if !migrate && !s.takeoverReady(ctx, view, groupID, key, owner.ID, now) {
		return nil
	}
	s.marks.observe(candidates, req.Loads, now)
	type candidate struct {
		account *Account
		key     selectionKey
	}
	ordered := make([]candidate, 0, len(candidates))
	for _, account := range candidates {
		if account == nil || account.ID == owner.ID || !s.candidateAllowed(view, groupID, model, owner, account, migrate, now) {
			continue
		}
		ordered = append(ordered, candidate{account, buildSelectionKey(view, groupID, model, account, req, now)})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if req.RequireCompact {
			a, b := openAICompactSupportTier(ordered[i].account), openAICompactSupportTier(ordered[j].account)
			if a != b {
				return a > b
			}
		}
		return selectionLess(ordered[i].key, ordered[j].key)
	})
	if !migrate && len(ordered) > schedquality.TakeoverMaxCandidates {
		ordered = ordered[:schedquality.TakeoverMaxCandidates]
	}
	for _, item := range ordered {
		if ctx.Err() != nil {
			return nil
		}
		target := s.recheck(ctx, item.account)
		if target == nil || !s.candidateAllowed(s.view(), groupID, model, owner, target, migrate, s.now()) {
			continue
		}
		slot, err := s.slots.tryAcquireAccountSlot(ctx, target.ID, target.Concurrency)
		if err != nil || slot == nil || !slot.Acquired {
			if err == nil && slot != nil && !slot.Acquired {
				s.marks.Mark(target.ID, s.now())
			}
			if slot != nil && slot.ReleaseFunc != nil {
				slot.ReleaseFunc()
			}
			if migrate {
				return nil
			}
			continue
		}
		latest := s.recheck(ctx, target)
		latestOwner := owner
		if !migrate {
			latestOwner = s.recheck(ctx, owner)
		}
		now, view = s.now(), s.view()
		allowed := latest != nil && latestOwner != nil && s.candidateAllowed(view, groupID, model, latestOwner, latest, migrate, now)
		if !migrate {
			allowed = allowed && s.takeoverReady(ctx, view, groupID, key, owner.ID, now)
		}
		if !allowed || ctx.Err() != nil {
			slot.ReleaseFunc()
			if migrate {
				return nil
			}
			continue
		}
		changed, err := s.cache.CompareAndSwapSessionAccountID(ctx, groupID, key, owner.ID, latest.ID, s.bindingTTL)
		if err != nil || !changed {
			slot.ReleaseFunc()
			if migrate {
				return nil
			}
			continue
		}
		resilience.StateFrom(ctx).ClearKeptStickyIfOwner(owner.ID)
		return attachSelectionProfitGate(ctx, &AccountSelectionResult{Account: latest, Acquired: true, ReleaseFunc: slot.ReleaseFunc})
	}
	return nil
}
