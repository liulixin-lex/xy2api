package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

const (
	openAIAccountScheduleLayerPreviousResponse = "previous_response_id"
	openAIAccountScheduleLayerGuardianParent   = "guardian_parent"
	openAIAccountScheduleLayerSessionSticky    = "session_hash"
	openAIAccountScheduleLayerLoadBalance      = "load_balance"
)

const (
	defaultOpenAIOAuthSchedulingRateMultiplier = 1.0
)

type OpenAIAccountScheduleRequest struct {
	GroupID                 *int64
	Platform                string
	SessionHash             string
	StickyAccountID         int64
	GuardianParentAccountID int64
	StickyPreviousAccountID int64
	PreserveStickyBinding   bool
	// DisableStickyEscape keeps task-owner lookups on their account even when
	// generic sticky health or concurrency heuristics would prefer another.
	DisableStickyEscape     bool
	SkipStickyCapacityRetry bool
	RequirePrivacySet       bool
	PreviousResponseID      string
	PreviousResponseCanMove bool
	UseUpstreamTokenCost    bool
	RequestedModel          string
	RequiredTransport       OpenAIUpstreamTransport
	RequiredCapability      OpenAIEndpointCapability
	RequiredImageCapability OpenAIImagesCapability
	// RequireCompact is only for legacy /responses/compact capability filtering
	// and compact_model_mapping; native remote compaction v2 leaves it false.
	RequireCompact bool
	ExcludedIDs    map[int64]struct{}
}

type OpenAIAccountScheduleDecision struct {
	Layer               string
	StickyPreviousHit   bool
	StickySessionHit    bool
	LatencyMs           int64
	SelectedAccountID   int64
	SelectedAccountType string
}

func openAIStickyAccountMatchesGroup(account *Account, groupID *int64) bool {
	if account == nil {
		return false
	}
	if groupID == nil {
		return len(account.AccountGroups) == 0 && len(account.GroupIDs) == 0
	}
	for _, accountGroupID := range account.GroupIDs {
		if accountGroupID == *groupID {
			return true
		}
	}
	for _, accountGroup := range account.AccountGroups {
		if accountGroup.GroupID == *groupID {
			return true
		}
	}
	return false
}

// openAISelectionFilterStats counts why candidates were dropped by the
// selectByLoadBalance initial filter. Historically these exclusions were
// silent (debug logs at best), so a "no available accounts" failure with
// excluded_account_count=0 was undiagnosable from the error alone (#4599).
// The reasons map is lazily allocated: on the happy path (nothing filtered
// out, or an account is eventually selected) no extra allocation happens.
type openAISelectionFilterStats struct {
	pool    int
	reasons map[string]int
}

func (s openAISelectionFilterStats) capabilityOnly() bool {
	return s.pool > 0 && len(s.reasons) == 1 && s.reasons[resilience.ReasonCapabilityStreamOnly] == s.pool
}

func (s *openAISelectionFilterStats) exclude(reason string) {
	if s.reasons == nil {
		s.reasons = make(map[string]int, 4)
	}
	s.reasons[reason]++
}

// summary renders deterministic exclusion statistics for scheduling error
// messages, e.g. "pool=3, filtered: model_not_supported=2 quota_auto_pause_7d=1".
// Reasons are sorted lexicographically so the output is stable for tests and
// log aggregation. extra, when non-empty, is appended as a trailing marker.
func (s openAISelectionFilterStats) summary(extra string) string {
	var b strings.Builder
	_, _ = b.WriteString("pool=")
	_, _ = b.WriteString(strconv.Itoa(s.pool))
	if len(s.reasons) > 0 {
		reasons := make([]string, 0, len(s.reasons))
		for reason := range s.reasons {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		_, _ = b.WriteString(", filtered:")
		for _, reason := range reasons {
			_, _ = b.WriteString(" ")
			_, _ = b.WriteString(reason)
			_, _ = b.WriteString("=")
			_, _ = b.WriteString(strconv.Itoa(s.reasons[reason]))
		}
	}
	if extra != "" {
		_, _ = b.WriteString(", ")
		_, _ = b.WriteString(extra)
	}
	return b.String()
}

func (s *OpenAIGatewayService) SelectAccountWithScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requireCompact bool,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	return s.selectAccountWithScheduler(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, "", "", requireCompact, PlatformOpenAI, false, true)
}

// SelectAccountWithSchedulerForCapability 按能力要求调度账号。
// previousResponseCanMove 表示首包 input 可自行重建工具续链，previous_response_id 允许跨账号迁移
// （粘性加权模式下改为加权偏好而非硬粘连）。
func (s *OpenAIGatewayService) SelectAccountWithSchedulerForCapability(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requireCompact bool,
	previousResponseCanMove bool,
	useUpstreamTokenCost bool,
	platformOverride ...string,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	platform := PlatformOpenAI
	switch requiredCapability {
	case OpenAIEndpointCapabilityEmbeddings, OpenAIEndpointCapabilityAlphaSearch, OpenAIEndpointCapabilityLive:
		ctx = withOpenAISelectionManualOnly(ctx)
	}
	if len(platformOverride) > 0 {
		platform = platformOverride[0]
	}
	return s.selectAccountWithScheduler(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, "", requireCompact, platform, previousResponseCanMove, useUpstreamTokenCost)
}

func (s *OpenAIGatewayService) SelectAccountWithSchedulerForImages(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredCapability OpenAIImagesCapability,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	ctx = withOpenAISelectionManualOnly(ctx)
	selection, decision, err := s.selectAccountWithScheduler(ctx, groupID, "", sessionHash, requestedModel, excludedIDs, OpenAIUpstreamTransportHTTPSSE, "", requiredCapability, false, PlatformOpenAI, false, false)
	if err == nil && selection != nil && selection.Account != nil {
		return selection, decision, nil
	}
	// 如果要求 native 能力（如指定了模型）但没有可用的 APIKey 账号，回退到 basic（OAuth 账号）
	if requiredCapability == OpenAIImagesCapabilityNative {
		return s.selectAccountWithScheduler(ctx, groupID, "", sessionHash, requestedModel, excludedIDs, OpenAIUpstreamTransportHTTPSSE, "", OpenAIImagesCapabilityBasic, false, PlatformOpenAI, false, false)
	}
	return selection, decision, err
}

// selectAccountWithScheduler wraps selectAccountWithSchedulerOnce with a
// fail-open second pass for the proxy stream circuit (#5056): when the only
// reason no account is available is that every candidate sits behind a
// quarantined proxy, the quarantine must degrade to a preference instead of
// zeroing out capacity. The retry re-runs the exact same selection with the
// quarantine checks bypassed, so healthy proxies always win the first pass
// and quarantined ones only serve when nothing else can.
func (s *OpenAIGatewayService) selectAccountWithScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
	platform string,
	previousResponseCanMove bool,
	useUpstreamTokenCost bool,
) (selection *AccountSelectionResult, decision OpenAIAccountScheduleDecision, err error) {
	sessionHash = monitorProbeSessionHash(ctx, sessionHash)
	state := resilience.StateFrom(ctx)
	state.ResetStickyBypassReason()
	if guard := openAIRejectionScope(ctx).selectionPreservesBinding; guard != nil {
		guard.Store(false)
	}
	stickyOwner := state.StickyOwner()
	if stickyOwner <= 0 && sessionHash != "" && s != nil {
		stickyOwner, _ = s.getStickySessionAccountID(ctx, groupID, sessionHash)
		state.NoteStickyOwner(stickyOwner)
	}
	_, ownerExcluded := excludedIDs[stickyOwner]
	ownerFailedInRequest := ownerExcluded && (state.AccountFailed(stickyOwner) || state.StateRepairAvoided(stickyOwner))
	defer func() {
		selected := int64(0)
		if selection != nil && selection.Account != nil {
			selected = selection.Account.ID
		}
		if stickyOwner <= 0 || stickyOwner == selected || !state.ClaimStickyBypassLog() {
			return
		}
		reason := state.StickyBypassReason()
		// A failed attempt can already have removed the cached binding before
		// this pass. Its request exclusion takes precedence over soft migration.
		if ownerFailedInRequest {
			reason = "owner_failed_in_request"
		}
		if reason == "" {
			reason = "unrecorded"
		}
		fields := append(resilience.EventFields(ctx),
			"group_id", derefGroupID(groupID),
			"session", shortSessionHash(sessionHash),
			"owner", stickyOwner,
			"selected", selected,
			"reason", reason,
		)
		if reason == "unrecorded" || state.StickyBypassChangesBinding() {
			slog.Warn("openai.sticky_bypass", fields...)
		} else {
			slog.Info("openai.sticky_bypass", fields...)
		}
	}()
	origExcluded := excludedIDs
	resilience.StateFrom(ctx).NoteTurn(derefGroupID(groupID), requestedModel, sessionHash)
	negativeCacheable := strings.TrimSpace(previousResponseID) == "" && requiredImageCapability == "" && !requireCompact
	negativeVariant := "openai:" + string(requiredTransport) + ":" + string(requiredCapability) + ":" + NormalizeOpenAICompatiblePlatform(platform)
	negativeCtx := ctx
	if negativeCacheable && groupID != nil && resilience.Current().ErrorsOn() {
		negativeCtx = s.withOpenAIProfitControlGate(ctx, groupID)
	}
	if negativeCacheable {
		if cachedErr := resilienceNegativeCacheHit(negativeCtx, groupID, requestedModel, negativeVariant, origExcluded); cachedErr != nil {
			return nil, OpenAIAccountScheduleDecision{}, cachedErr
		}
	}
	ctx = s.withResilienceOpenAIEligibility(ctx, groupID, previousResponseID, requestedModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform)
	avoided := s.resilience.sessionAvoid(ctx, groupID, sessionHash)
	if scope := openAIRejectionScope(ctx).session; scope != "" && scope != sessionHash {
		threadAvoid := s.resilience.sessionAvoid(ctx, groupID, scope)
		avoided = mergeExcluded(avoided, threadAvoid)
		if rejectionScope := openAIRejectionScope(ctx); len(threadAvoid) > 0 && rejectionScope.preserveBinding && rejectionScope.selectionPreservesBinding != nil {
			owner, ownerErr := s.getStickySessionAccountID(ctx, groupID, sessionHash)
			_, ownerAvoided := threadAvoid[owner]
			// A child-only avoidance must not replace an unknown shared owner.
			// Keep affinity reads, but suppress this selection's binding writes.
			rejectionScope.selectionPreservesBinding.Store(ownerErr != nil || owner <= 0 || ownerAvoided)
		}
	}
	resilience.StateFrom(ctx).SetAvoided(avoided)
	excludedIDs = mergeExcluded(excludedIDs, avoided)
	selection, decision, err = s.selectAccountWithQualityRouting(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove, useUpstreamTokenCost)
	markResilienceConfigError(ctx, err)
	// xy2api resilience: last-resort soft selection instead of "no available
	// account" (design §4.3). Continuations owned by an account keep failing
	// as before.
	if isNoAccountSelectionError(err) && strings.TrimSpace(previousResponseID) == "" {
		lrCtx := s.withOpenAIQuotaAutoPauseContext(ctx)
		if requiredImageCapability == "" {
			lrCtx = s.withOpenAIProfitControlGate(lrCtx, groupID)
		}
		if lr := s.resilienceOpenAILastResort(lrCtx, OpenAIAccountScheduleRequest{
			GroupID: groupID, Platform: NormalizeOpenAICompatiblePlatform(platform), SessionHash: sessionHash,
			RequestedModel: requestedModel, RequiredTransport: requiredTransport, RequiredCapability: requiredCapability,
			RequiredImageCapability: requiredImageCapability, RequireCompact: requireCompact, ExcludedIDs: origExcluded,
			RequirePrivacySet: s.openAIGroupRequiresPrivacySet(lrCtx, groupID), UseUpstreamTokenCost: useUpstreamTokenCost,
		}, avoided); lr != nil {
			selection, err = lr, nil
			decision.SelectedAccountID = lr.Account.ID
			decision.SelectedAccountType = lr.Account.Type
		} else if negativeCacheable {
			resilienceNegativeCachePut(negativeCtx, groupID, requestedModel, negativeVariant, origExcluded, err)
		}
	}
	// xy2api resilience: selections that are never cached still count for the
	// per-key fast fail when no account of the group could serve them.
	if !negativeCacheable && isNoAccountSelectionError(err) {
		resilienceMarkGroupNoAccount(ctx, groupID, requestedModel, origExcluded)
	}
	if err == nil && selection != nil {
		if sessionHash != "" {
			resilience.StateFrom(ctx).SetSession(derefGroupID(groupID), sessionHash, s.openAISessionRecorder())
		}
	}
	return selection, decision, capabilitySelectionError(ctx, err)
}

func (s *OpenAIGatewayService) selectAccountWithoutQualityRouting(
	ctx context.Context, groupID *int64, previousResponseID, sessionHash, requestedModel string,
	excludedIDs map[int64]struct{}, requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability,
	requireCompact bool, platform string, previousResponseCanMove, useUpstreamTokenCost bool,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	selection, decision, err := s.selectAccountWithSchedulerOnce(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove, useUpstreamTokenCost)
	if err == nil || openAIProxyStreamQuarantineBypassed(ctx) {
		return selection, decision, err
	}
	if !errors.Is(err, ErrNoAvailableAccounts) && !errors.Is(err, ErrNoAvailableCompactAccounts) {
		return selection, decision, err
	}
	// The circuit only ever quarantines PlatformOpenAI accounts.
	if NormalizeOpenAICompatiblePlatform(platform) != PlatformOpenAI {
		return selection, decision, err
	}
	blocked := s.getOpenAIProxyStreamCircuit().activeBlockCount(time.Now())
	if blocked == 0 {
		return selection, decision, err
	}
	s.logOpenAIProxyStreamQuarantineFailOpen(requestedModel, blocked)
	return s.selectAccountWithSchedulerOnce(withOpenAIProxyStreamQuarantineBypass(ctx), groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove, useUpstreamTokenCost)
}

type openAIGroupPrivacyRequirementContextKey struct{}

type openAIGroupPrivacyRequirement struct {
	groupID  int64
	required bool
}

func (s *OpenAIGatewayService) withOpenAIGroupPrivacyRequirement(ctx context.Context, groupID *int64) context.Context {
	if cached, ok := ctx.Value(openAIGroupPrivacyRequirementContextKey{}).(openAIGroupPrivacyRequirement); ok && cached.groupID == derefGroupID(groupID) {
		return ctx
	}
	return context.WithValue(ctx, openAIGroupPrivacyRequirementContextKey{}, openAIGroupPrivacyRequirement{
		groupID:  derefGroupID(groupID),
		required: s.loadOpenAIGroupRequiresPrivacySet(ctx, groupID),
	})
}

func (s *OpenAIGatewayService) openAIGroupRequiresPrivacySet(ctx context.Context, groupID *int64) bool {
	if cached, ok := ctx.Value(openAIGroupPrivacyRequirementContextKey{}).(openAIGroupPrivacyRequirement); ok && cached.groupID == derefGroupID(groupID) {
		return cached.required
	}
	return s.loadOpenAIGroupRequiresPrivacySet(ctx, groupID)
}

func (s *OpenAIGatewayService) loadOpenAIGroupRequiresPrivacySet(ctx context.Context, groupID *int64) bool {
	if s == nil || groupID == nil || s.schedulerSnapshot == nil {
		return false
	}
	// GetByID 会聚合账号计数；隐私开关只在分组行上。
	group, err := s.schedulerSnapshot.GetGroupByIDLite(ctx, *groupID)
	if err != nil {
		return true
	}
	return group != nil && group.RequirePrivacySet
}

// applyLegacySelectionDecision 把非高级调度路径的选号结果回填到决策：记录选中账号，
// 命中会话粘性时标记 session_hash 层，否则保持 load_balance。
func applyLegacySelectionDecision(decision *OpenAIAccountScheduleDecision, selection *AccountSelectionResult) {
	if decision == nil || selection == nil || selection.Account == nil {
		return
	}
	decision.SelectedAccountID = selection.Account.ID
	decision.SelectedAccountType = selection.Account.Type
	if selection.stickySessionHit {
		decision.Layer = openAIAccountScheduleLayerSessionSticky
		decision.StickySessionHit = true
	}
}

func (s *OpenAIGatewayService) selectAccountWithSchedulerOnce(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
	platform string,
	previousResponseCanMove bool,
	useUpstreamTokenCost bool,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	resilience.StateFrom(ctx).ResetStickyBypassReason()
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, groupID)
	// 分组利润控制：唯一文本调度入口的防御性装门。handler 文本
	// 入口已在请求开始经 WithOpenAIRequestPricingContext 装门并固定 pricingAt，
	// 此处对同分组门直接复用（failover 重入阈值稳定），仅为不经 handler 装配的
	// 内部调用兜底。图片/视频调度不在利润门范围：requiredImageCapability 非空的
	// Images 调度不装门；其他使用 Responses 能力的文本请求（包括原生远程压缩）
	// 仍须装门。其余媒体路径通过 WithOpenAIProfitControlSuppressed 显式跳过。
	if requiredImageCapability == "" {
		ctx = s.withOpenAIProfitControlGate(ctx, groupID)
	}
	platform = NormalizeOpenAICompatiblePlatform(platform)
	decision := OpenAIAccountScheduleDecision{}
	preserveGuardianParentBinding := preserveOpenAIGuardianParentBinding(ctx, sessionHash)
	guardianParentAccountID := int64(0)
	if strings.TrimSpace(previousResponseID) == "" {
		guardianParentAccountID = s.resolveOpenAIGuardianParentAccountID(ctx, groupID)
	}
	ctx = withOpenAIStickyMigrationGuard(ctx, previousResponseID, requiredTransport)
	decision.Layer = openAIAccountScheduleLayerLoadBalance
	if selection, hit, err := s.selectLegacyAccountByPreviousResponse(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform); err != nil {
		return nil, decision, err
	} else if hit {
		decision.Layer = openAIAccountScheduleLayerPreviousResponse
		decision.StickyPreviousHit = true
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
		return selection, decision, nil
	}
	if guardianParentAccountID > 0 {
		if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
			return nil, decision, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
		}
		fallbackScheduler := s
		selection, _, err := fallbackScheduler.selectBySessionHash(withOpenAISelectionExempt(ctx), OpenAIAccountScheduleRequest{
			GroupID:                 groupID,
			Platform:                platform,
			SessionHash:             sessionHash,
			StickyAccountID:         guardianParentAccountID,
			PreserveStickyBinding:   true,
			RequestedModel:          requestedModel,
			RequiredTransport:       requiredTransport,
			RequiredCapability:      requiredCapability,
			RequiredImageCapability: requiredImageCapability,
			RequireCompact:          requireCompact,
			ExcludedIDs:             excludedIDs,
			RequirePrivacySet:       s.openAIGroupRequiresPrivacySet(ctx, groupID),
		})
		if err != nil {
			return nil, decision, err
		}
		if selection != nil && selection.Account != nil {
			decision.Layer = openAIAccountScheduleLayerGuardianParent
			decision.StickySessionHit = true
			decision.SelectedAccountID = selection.Account.ID
			decision.SelectedAccountType = selection.Account.Type
			return selection, decision, nil
		}
	}
	legacySessionHash := sessionHash
	if preserveGuardianParentBinding {
		legacySessionHash = ""
	}
	ctx = withOpenAILegacyStickyCapacity(ctx, requiredTransport, requiredImageCapability)
	if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
		effectiveExcludedIDs := cloneExcludedAccountIDs(excludedIDs)
		for {
			selection, err := s.selectAccountWithLoadAwareness(ctx, groupID, platform, legacySessionHash, requestedModel, effectiveExcludedIDs, requireCompact, requiredCapability, useUpstreamTokenCost)
			if err != nil {
				return nil, decision, err
			}
			if selection == nil || selection.Account == nil {
				return selection, decision, nil
			}
			if accountSupportsOpenAICapabilities(selection.Account, requiredCapability, requiredImageCapability) {
				applyLegacySelectionDecision(&decision, selection)
				return selection, decision, nil
			}
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			if effectiveExcludedIDs == nil {
				effectiveExcludedIDs = make(map[int64]struct{})
			}
			if _, exists := effectiveExcludedIDs[selection.Account.ID]; exists {
				return nil, decision, ErrNoAvailableAccounts
			}
			effectiveExcludedIDs[selection.Account.ID] = struct{}{}
		}
	}

	effectiveExcludedIDs := cloneExcludedAccountIDs(excludedIDs)
	for {
		selection, err := s.selectAccountWithLoadAwareness(ctx, groupID, platform, legacySessionHash, requestedModel, effectiveExcludedIDs, requireCompact, requiredCapability, useUpstreamTokenCost)
		if err != nil {
			return nil, decision, err
		}
		if selection == nil || selection.Account == nil {
			return selection, decision, nil
		}
		if s.isOpenAIAccountTransportCompatible(selection.Account, requiredTransport) &&
			accountSupportsOpenAICapabilities(selection.Account, requiredCapability, requiredImageCapability) {
			applyLegacySelectionDecision(&decision, selection)
			return selection, decision, nil
		}
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		if effectiveExcludedIDs == nil {
			effectiveExcludedIDs = make(map[int64]struct{})
		}
		if _, exists := effectiveExcludedIDs[selection.Account.ID]; exists {
			return nil, decision, ErrNoAvailableAccounts
		}
		effectiveExcludedIDs[selection.Account.ID] = struct{}{}
	}
}

func accountSupportsOpenAICapabilities(account *Account, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability) bool {
	if account == nil {
		return false
	}
	return account.SupportsOpenAIEndpointCapability(requiredCapability) &&
		account.SupportsOpenAIImageCapability(requiredImageCapability)
}

func cloneExcludedAccountIDs(excludedIDs map[int64]struct{}) map[int64]struct{} {
	if len(excludedIDs) == 0 {
		return nil
	}
	cloned := make(map[int64]struct{}, len(excludedIDs))
	for id := range excludedIDs {
		cloned[id] = struct{}{}
	}
	return cloned
}

func (s *OpenAIGatewayService) isOpenAIAccountTransportCompatible(account *Account, requiredTransport OpenAIUpstreamTransport) bool {
	if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
		return true
	}
	if s == nil || account == nil {
		return false
	}
	if requiredTransport == OpenAIUpstreamTransportResponsesWebsocketV2Ingress {
		if s.cfg == nil || !s.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled {
			return s.getOpenAIWSProtocolResolver().Resolve(account).Transport == OpenAIUpstreamTransportResponsesWebsocketV2
		}
		mode := account.ResolveOpenAIResponsesWebSocketV2Mode(s.cfg.Gateway.OpenAIWS.IngressModeDefault)
		switch mode {
		case OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough, OpenAIWSIngressModeHTTPBridge, OpenAIWSIngressModeShared, OpenAIWSIngressModeDedicated:
			return true
		default:
			return false
		}
	}
	return s.getOpenAIWSProtocolResolver().Resolve(account).Transport == requiredTransport
}

func (s *OpenAIGatewayService) ReportOpenAIAccountScheduleResult(account *Account, model string, success bool, firstTokenMs *int, observedErr ...error) bool {
	return s.ReportOpenAIAccountScheduleResultWithContext(context.Background(), account, model, success, firstTokenMs, observedErr...)
}

// ReportOpenAIAccountScheduleResultWithContext reports a request outcome to the
// scheduler and API-key health observer. Credential, quota and provider
// Retry-After handlers remain independent.
func (s *OpenAIGatewayService) ReportOpenAIAccountScheduleResultWithContext(ctx context.Context, account *Account, model string, success bool, firstTokenMs *int, observedErr ...error) bool {
	if account == nil {
		return false
	}
	accountID := account.ID
	healthTripped := false
	if s != nil && s.rateLimitService != nil {
		if success {
			s.rateLimitService.ObserveOpenAIAPIKeyHealthSuccess(ctx, account)
		} else if len(observedErr) > 0 && observedErr[0] != nil {
			healthTripped = s.rateLimitService.ObserveOpenAIAPIKeyHealthFailure(ctx, account, observedErr[0])
		}
	}
	if success {
		s.openaiOAuth429RetryStartedAt.Delete(accountID)
		s.ReportOpenAIAttemptSuccess(ctx, account, model)
	}
	return healthTripped
}

// ObserveOpenAIAccountHealthFailure records failures that cannot reach the
// scheduler-result path, for example after semantic response bytes were sent.
func (s *OpenAIGatewayService) ObserveOpenAIAccountHealthFailure(ctx context.Context, account *Account, observedErr error) bool {
	if s == nil || s.rateLimitService == nil || account == nil || observedErr == nil {
		return false
	}
	return s.rateLimitService.ObserveOpenAIAPIKeyHealthFailure(ctx, account, observedErr)
}

func (s *OpenAIGatewayService) openAIWSSessionStickyTTL() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds > 0 {
		return time.Duration(s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds) * time.Second
	}
	return openaiStickySessionTTL
}

func clamp01(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}
