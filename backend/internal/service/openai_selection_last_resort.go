package service

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func (s *OpenAIGatewayService) pickOpenAILastResort(ctx context.Context, req OpenAIAccountScheduleRequest, candidates []lastResortCandidate) []lastResortCandidate {
	accounts := make([]*Account, 0, len(candidates))
	for _, c := range candidates {
		accounts = append(accounts, c.account)
	}
	var loads map[int64]*AccountLoadInfo
	if s.concurrencyService != nil {
		loads, _ = s.concurrencyService.GetAccountsLoadBatchFresh(ctx, selectionLoadRequests(accounts))
	}
	s.selectionFullMarks.observe(accounts, loads, s.selectionNow())
	request := s.selectionRequest(ctx, req, loads)
	view, now := s.selectionView(), s.selectionNow()
	keys := make(map[int64]selectionKey, len(accounts))
	for _, a := range accounts {
		keys[a.ID] = buildSelectionKey(view, derefGroupID(req.GroupID), req.RequestedModel, a, request, now)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.avoided != b.avoided {
			return !a.avoided
		}
		if req.RequireCompact {
			at, bt := openAICompactSupportTier(a.account), openAICompactSupportTier(b.account)
			if at != bt {
				return at > bt
			}
		}
		return selectionLess(keys[a.account.ID], keys[b.account.ID])
	})
	return candidates
}

// resilienceOpenAILastResort is the OpenAI-scheduler variant of last-resort
// selection. It reuses the scheduler's per-account request gate on a copy
// with the soft restrictions cleared.
func (s *OpenAIGatewayService) resilienceOpenAILastResort(ctx context.Context, req OpenAIAccountScheduleRequest, avoided map[int64]struct{}) *AccountSelectionResult {
	if s == nil || s.resilience == nil || req.GroupID == nil || s.accountRepo == nil || !resilience.Current().LastResortOn() || ctx.Err() != nil {
		return nil
	}
	accounts, err := s.resilience.groupAccountsForLastResort(ctx, s.accountRepo, *req.GroupID)
	if err != nil {
		slog.Warn("resilience.last_resort_list_failed", "group_id", *req.GroupID, "error", err)
		return nil
	}
	platform := NormalizeOpenAICompatiblePlatform(req.Platform)
	gate := s
	gateCtx := withResilienceLastResortGate(ctx)
	now := s.selectionNow()
	var cands []lastResortCandidate
	meta := lastResortLogMeta{candidates: len(accounts)}
	for i := range accounts {
		acc := accounts[i]
		_, isAvoided := avoided[acc.ID]
		if isAvoided {
			meta.addReason("session_avoid")
		}
		if _, excluded := req.ExcludedIDs[acc.ID]; excluded {
			if !isAvoided {
				meta.addReason("request_excluded")
			}
			continue
		}
		if acc.Platform != platform || !acc.IsOpenAICompatible() {
			meta.addReason("platform_mismatch")
			continue
		}
		compactOutboundModel := ""
		if req.RequireCompact {
			compactOutboundModel = s.openAICodexTicketOutboundModel(&acc, req.RequestedModel, true)
		}
		if iqGateBlocksRequest(&acc, req.RequestedModel, req.RequireCompact, compactOutboundModel) {
			meta.addReason("threshold")
			continue
		}
		tier, recoverAt, penaltyReason := s.resilienceOpenAIPenaltyOf(&acc, req.RequestedModel, now)
		meta.addReason(penaltyReason)
		if !lastResortTierAllowed(tier, acc.ID, avoided) {
			if tier == resilience.TierNone {
				meta.addReason("tier_none")
			}
			continue
		}
		candidate := withoutSoftPenalties(acc, now)
		if reason := openAICompatibleAccountEligibilityFailureReasonBeforeProfit(gateCtx, candidate, platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability, s); reason != "" &&
			(reason != "model_rate_limited" || !lastResortModelLimitSoft(gateCtx, candidate, req.RequestedModel, req.RequireCompact, compactOutboundModel)) {
			meta.addReason(reason)
			continue
		}
		if ok, reason := gate.isAccountRequestCompatibleReason(gateCtx, candidate, req); !ok {
			meta.addReason(string(reason))
			continue
		}
		if !gate.isAccountTransportCompatible(candidate, req.RequiredTransport) {
			meta.addReason("transport_mismatch")
			continue
		}
		cands = append(cands, lastResortCandidate{account: candidate, recoverAt: recoverAt, avoided: isAvoided})
	}
	cands = s.pickOpenAILastResort(ctx, req, cands)
	if len(cands) == 0 {
		return nil
	}
	// Take the first candidate with a free slot; when all are busy, wait
	// for the best one. Codex accounts under upstream pressure take slots
	// only up to their adaptive limit.
	verified := cands[:0]
	var compactOutboundForFresh func(*Account) string
	if req.RequireCompact {
		compactOutboundForFresh = func(fresh *Account) string {
			return s.openAICodexTicketOutboundModel(fresh, req.RequestedModel, true)
		}
	}
	for _, c := range cands {
		acc, ok := s.resilience.verifyLastResortAccount(ctx, s.accountRepo, *req.GroupID, c.account, req.RequestedModel, req.RequireCompact, s.resilienceOpenAIPenaltyOf, compactOutboundForFresh)
		if !ok {
			meta.addReason("verify_failed")
			continue
		}
		if !s.isAccountRequestCompatible(gateCtx, acc, req) || !s.isAccountTransportCompatible(acc, req.RequiredTransport) {
			meta.addReason("verify_failed")
			continue
		}
		c.account = acc
		verified = append(verified, c)
		result, err := s.tryAcquireAccountSlot(ctx, acc.ID, s.codexEffectiveConcurrency(ctx, acc.ID, acc.Concurrency))
		if err == nil && result != nil && result.Acquired {
			latest, vetoed, _ := s.SelectionAdmissionVetoLatest(gateCtx, acc)
			if vetoed || latest == nil {
				result.ReleaseFunc()
				verified = verified[:len(verified)-1]
				continue
			}
			acc = latest
			s.noteOpenAILastResort(ctx, req.GroupID, acc, c.recoverAt, avoided, meta)
			return attachSelectionProfitGate(gateCtx, &AccountSelectionResult{Account: acc, Acquired: true, ReleaseFunc: result.ReleaseFunc})
		}
		meta.addReason("capacity")
	}
	if len(verified) == 0 {
		return nil
	}
	cfg := s.schedulingConfig()
	acc := verified[0].account
	s.noteOpenAILastResort(ctx, req.GroupID, acc, verified[0].recoverAt, avoided, meta)
	return attachSelectionProfitGate(gateCtx, &AccountSelectionResult{
		Account: acc,
		WaitPlan: &AccountWaitPlan{
			AccountID:      acc.ID,
			MaxConcurrency: s.codexEffectiveConcurrency(ctx, acc.ID, acc.Concurrency),
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		},
	})
}

func (s *OpenAIGatewayService) noteOpenAILastResort(ctx context.Context, groupID *int64, account *Account, recoverAt time.Time, avoided map[int64]struct{}, meta lastResortLogMeta) {
	resilience.StateFrom(ctx).SetLastResort(account.ID)
	s.resilience.noteLastResortSelected(account.ID)
	resilience.Count(resilience.MetricLastResort, derefGroupID(groupID), account.ID, account.Platform)
	slog.Warn("resilience.last_resort", append(resilience.EventFields(ctx),
		"group_id", derefGroupID(groupID),
		"account_id", account.ID,
		"platform", account.Platform,
		"recover_at", recoverAt,
		"reason", "last_resort_soft",
		"candidates", meta.candidates,
		"penalty_reason", meta.penaltyReason(),
	)...)
	if _, sessionAvoided := avoided[account.ID]; sessionAvoided {
		slog.Warn("resilience.last_resort_avoided", append(resilience.EventFields(ctx),
			"group_id", derefGroupID(groupID),
			"account_id", account.ID,
			"reason", "session_avoid",
			"candidates", meta.candidates,
			"penalty_reason", meta.penaltyReason(),
		)...)
	}
}
