package service

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

// Keep the scheduler's view dependency explicit for W0 wiring. A nil view is
// equivalent to a disabled quality system and therefore leaves accounts front.
var _ schedquality.View = (*emptySelectionView)(nil)

type emptySelectionView struct{}

func (emptySelectionView) Version() int64      { return 0 }
func (emptySelectionView) At() time.Time       { return time.Time{} }
func (emptySelectionView) GlobalEnabled() bool { return false }
func (emptySelectionView) StateOf(int64, int64, string) schedquality.StateView {
	return schedquality.StateView{}
}
func (emptySelectionView) TakeoverIdle(int64) (time.Duration, bool) { return 0, false }

func (s *OpenAIGatewayService) isAccountTransportCompatible(account *Account, requiredTransport OpenAIUpstreamTransport) bool {
	if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
		return true
	}
	if s == nil {
		return false
	}
	return s.isOpenAIAccountTransportCompatible(account, requiredTransport)
}

func (s *OpenAIGatewayService) lookupShadowParentAccount(ctx context.Context, id int64) *Account {
	if s == nil {
		return nil
	}
	if s.schedulerSnapshot != nil {
		if account, err := s.schedulerSnapshot.GetAccount(ctx, id); err == nil && account != nil {
			return account
		}
	}
	if s.accountRepo == nil {
		return nil
	}
	account, _ := s.accountRepo.GetByID(ctx, id)
	return account
}

func (s *OpenAIGatewayService) isAccountRequestCompatible(ctx context.Context, account *Account, req OpenAIAccountScheduleRequest) bool {
	compatible, _ := s.isAccountRequestCompatibleReason(ctx, account, req)
	return compatible
}

// isAccountRequestCompatibleReason reports whether the account can serve the
// request, and when it cannot, names the veto point. The reason feeds
// openAISelectionFilterStats so that "no available accounts" errors state why
// each candidate was dropped instead of failing silently (#4599).
func (s *OpenAIGatewayService) isAccountRequestCompatibleReason(ctx context.Context, account *Account, req OpenAIAccountScheduleRequest) (bool, string) {
	if account == nil {
		return false, "account_nil"
	}
	if source, ok := CompositeRouteSourceFromContext(ctx); ok && source == CompositeRouteSourceAccount {
		if publicModel, modelOK := RequestedPublicModelFromContext(ctx); modelOK && !explicitModelMappingClaims(*account, publicModel) {
			return false, "account_model_not_owned"
		}
	}
	if req.RequirePrivacySet && !account.IsPrivacySet() {
		return false, "privacy_not_set"
	}
	// xy2api resilience: last-resort selection treats in-memory runtime
	// blocks and proxy quarantine as soft restrictions (design §4.3).
	lastResort := resilienceLastResortGate(ctx)
	if !lastResort && s != nil && s.isOpenAIAccountRequestRuntimeBlocked(account, req.RequestedModel, req.RequireCompact) {
		return false, "runtime_blocked"
	}
	if !lastResort && s != nil && s.isOpenAIProxyStreamQuarantined(ctx, account) {
		return false, "proxy_stream_quarantined"
	}
	// Quota auto-pause must be evaluated during the initial filter too. Without it the
	// TopK candidate pool can be filled with paused accounts and the later fresh/DB
	// rechecks won't reach healthy accounts that fell outside TopK — manifesting as
	// "no available accounts" even though healthy ones exist.
	if paused, decision := shouldAutoPauseOpenAIAccountByQuota(ctx, account); paused {
		if decision.reason != "" {
			return false, decision.reason
		}
		reason := "quota_auto_pause"
		if decision.window != "" {
			reason += "_" + decision.window
		}
		return false, reason
	}
	// 母账号健康联动：影子账号的凭据来自母账号，母账号不可调度时影子也不应被选中。
	// Parent-health gate: shadow borrows the parent's credentials; an unschedulable
	// parent must block the shadow across all scheduler paths.
	if !parentHealthyForShadow(account, func(id int64) *Account {
		return s.lookupShadowParentAccount(ctx, id)
	}) {
		return false, "shadow_parent_unhealthy"
	}
	if req.RequestedModel != "" && !account.IsModelSupported(req.RequestedModel) {
		return false, "model_not_supported"
	}
	compactOutboundModel := ""
	if req.RequireCompact && s != nil {
		compactOutboundModel = s.openAICodexTicketOutboundModel(account, req.RequestedModel, true)
	}
	if iqGateBlocksRequest(account, req.RequestedModel, req.RequireCompact, compactOutboundModel) {
		return false, "model_rate_limited"
	}
	if lastResort && account.IsSchedulable() && account.isModelRateLimitedWithContext(ctx, req.RequestedModel) && !lastResortModelLimitSoft(ctx, account, req.RequestedModel, req.RequireCompact, compactOutboundModel) {
		return false, "model_rate_limited"
	}
	if req.GroupID != nil && s != nil &&
		s.needsUpstreamChannelRestrictionCheck(ctx, req.GroupID) &&
		s.isUpstreamModelRestrictedByChannel(ctx, *req.GroupID, account, req.RequestedModel, req.RequireCompact) {
		return false, "channel_upstream_restricted"
	}
	if !accountSupportsOpenAICapabilities(account, req.RequiredCapability, req.RequiredImageCapability) {
		return false, "capability_mismatch"
	}
	if !req.RequireCompact && req.RequiredImageCapability == "" {
		if reason := capabilityEligibilityReason(ctx, account, req.RequestedModel, time.Now()); reason != "" {
			return false, reason
		}
	}
	// 分组利润控制：不合格账号在候选过滤与抢槽后终检阶段即被排除，
	// 排序/评分/粘性/熔断只在合格账号之间工作；named reason 进入 filter stats。
	if vetoed, reason := openAIProfitControlVetoReason(ctx, account); vetoed {
		return false, reason
	}

	return true, ""
}

func (emptySelectionView) GroupEnabled(int64) bool { return false }
func (emptySelectionView) ResetAt(int64) time.Time { return time.Time{} }
