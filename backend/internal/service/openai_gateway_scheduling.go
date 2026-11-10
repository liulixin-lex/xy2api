package service

// 本文件由 openai_gateway_service.go 纯移动拆分而来：粘性会话哈希、账号选择与
// 负载感知调度、配额自动暂停判定、并发槽位获取。仅做代码搬迁，无任何行为变更。

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
	"github.com/liulixin-lex/xy2api/internal/pkg/xai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

const (
	openCodeSessionAffinityHeader = "X-Session-Affinity"
	openCodeSessionIDHeader       = "X-Session-Id"
	openCodeNativeSessionHeader   = "X-OpenCode-Session"
	codeBuddyConversationHeader   = "X-Conversation-ID"
)

var explicitOpenAIHeaderSessionNames = []string{
	"session-id",
	"session_id",
	"conversation_id",
	openCodeSessionAffinityHeader,
	openCodeSessionIDHeader,
	openCodeNativeSessionHeader,
	codeBuddyConversationHeader,
}

// explicitOpenAIHeaderSessionID resolves stable conversation identifiers sent
// by OpenAI-compatible clients. Keep this list limited to session-scoped
// fields: request/message IDs rotate every turn and would defeat sticky routing
// and upstream prompt caching.
func explicitOpenAIHeaderSessionID(c *gin.Context) string {
	if c == nil {
		return ""
	}

	for _, header := range explicitOpenAIHeaderSessionNames {
		if sessionID := strings.TrimSpace(c.GetHeader(header)); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

// ExtractSessionID extracts the raw session ID from headers or body without hashing.
// Used by ForwardAsAnthropic to pass as prompt_cache_key for upstream cache.
func (s *OpenAIGatewayService) ExtractSessionID(c *gin.Context, body []byte) string {
	return explicitOpenAIRequestSessionID(c, body)
}

func explicitOpenAISessionID(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}

	sessionID := explicitOpenAIHeaderSessionID(c)
	if sessionID == "" && len(body) > 0 {
		sessionID = strings.TrimSpace(openAIRequestPayloadView(body).Get("prompt_cache_key").String())
	}
	return sessionID
}

// openAIRequestPayloadView unwraps Responses WebSocket event envelopes while
// leaving ordinary HTTP objects untouched even when they contain a response
// field for another purpose.
func openAIRequestPayloadView(body []byte) gjson.Result {
	root := parseRawJSONView(body)
	eventType := strings.ToLower(strings.TrimSpace(root.Get("type").String()))
	if strings.HasPrefix(eventType, "response.") {
		if response := root.Get("response"); response.Exists() && response.IsObject() {
			return response
		}
	}
	return root
}

// explicitOpenAIRequestSessionID extends the common OpenAI session signals
// with Grok's native conversation header only for requests authenticated to a
// Grok group. This keeps an unrelated x-grok-conv-id header from changing
// scheduling or upstream session behavior for non-Grok groups.
//
// For Grok groups only, previous_response_id is a last-resort sticky seed so
// multi-turn Responses chains stay on the same OAuth account when no explicit
// session/conversation/prompt_cache_key is present. Non-Grok groups omit this
// so HTTP OpenAI paths that delete previous_response_id before upstream are
// unchanged.
func explicitOpenAIRequestSessionID(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}

	sessionID := explicitOpenAIHeaderSessionID(c)
	if sessionID == "" && isGrokRequestContext(c) {
		sessionID = strings.TrimSpace(c.GetHeader(grokConversationIDHeader))
	}
	if sessionID == "" && len(body) > 0 {
		sessionID = strings.TrimSpace(openAIRequestPayloadView(body).Get("prompt_cache_key").String())
	}
	if sessionID == "" && isGrokRequestContext(c) && len(body) > 0 {
		sessionID = grokPreviousResponseSessionSeed(body)
	}
	return sessionID
}

// grokPreviousResponseSessionSeed returns a stable sticky seed from a Responses
// previous_response_id. Only resp_* response ids are accepted; message ids and
// unknown shapes must not pin sticky routing or prompt-cache identity.
func grokPreviousResponseSessionSeed(body []byte) string {
	id := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	if id == "" {
		return ""
	}
	if ClassifyOpenAIPreviousResponseIDKind(id) != OpenAIPreviousResponseIDKindResponseID {
		return ""
	}
	// Namespace so content-derived seeds never collide with response ids.
	return "grok-prev-resp:" + id
}

func (s *OpenAIGatewayService) openAISessionHashesForSeed(c *gin.Context, body []byte, seed string) (current, legacy string) {
	seed = s.scopedOpenAISessionSeed(c, body, seed)
	current, legacy = deriveOpenAISessionHashes(seed)
	if strings.HasPrefix(seed, "openai-routing-v2:") {
		current = "v2:" + current
		legacy = ""
	}
	return current, legacy
}

// GenerateExplicitSessionHash generates a sticky-session hash only from explicit
// client session signals. It intentionally skips content-derived fallback and is
// used by stateless endpoints such as /v1/images.
func (s *OpenAIGatewayService) GenerateExplicitSessionHash(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}
	sessionID := explicitOpenAIRequestSessionID(c, body)
	if sessionID == "" {
		return ""
	}

	currentHash, legacyHash := s.openAISessionHashesForSeed(c, body, sessionID)
	c.Set(openAICacheSessionGinKey, currentHash)
	if c.Request != nil {
		if diagnostic := s.newCacheDiagnostic(c, body, "ingress"); diagnostic != nil {
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), openAICacheDiagnosticKey{}, diagnostic))
		}
	}
	attachOpenAILegacySessionHashToGin(c, legacyHash)
	return currentHash
}

// GenerateSessionHash generates a sticky-session hash for OpenAI requests.
//
// Priority:
//  1. Header: session-id / session_id
//  2. Header: conversation_id
//  3. Header: x-session-affinity / x-session-id / x-opencode-session (OpenCode)
//  4. Header: x-conversation-id (CodeBuddy)
//  5. Header: x-grok-conv-id (Grok groups only)
//  6. Body:   prompt_cache_key
//  7. Body:   content-based fallback (model + system + tools + first user message)
//
// Grok sticky affinity is intentionally separate from the upstream
// prompt_cache_key identity (resolveGrokCacheIdentity): sticky pins an OAuth
// account for multi-turn routing, while the cache identity is tenant+model
// isolated for xAI server-side prompt cache. For Grok groups we scope the
// sticky seed with the client-requested model so switching models does not
// inherit a stale account binding (grok2api affinityKey pattern).
func (s *OpenAIGatewayService) GenerateSessionHash(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}
	if monitorProbeRequest(c) {
		return ""
	}

	sessionID := explicitOpenAIRequestSessionID(c, body)
	if sessionID == "" && len(body) > 0 {
		sessionID = deriveOpenAIContentSessionSeed(body)
	}
	if sessionID == "" {
		return ""
	}

	if isGrokRequestContext(c) {
		sessionID = grokStickyAffinitySeed(sessionID, body)
	}

	currentHash, legacyHash := s.openAISessionHashesForSeed(c, body, sessionID)
	c.Set(openAICacheSessionGinKey, currentHash)
	if c.Request != nil {
		if diagnostic := s.newCacheDiagnostic(c, body, "ingress"); diagnostic != nil {
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), openAICacheDiagnosticKey{}, diagnostic))
		}
	}
	attachOpenAILegacySessionHashToGin(c, legacyHash)
	if c.Request != nil && qualityRequest(c.Request.Context()) == nil {
		s.AttachOpenAIQualityRouting(c, currentHash, body)
	}
	return currentHash
}

// grokStickyAffinitySeed scopes sticky routing by model without changing the
// upstream prompt_cache_key written by applyGrokResponsesCacheIdentity.
func grokStickyAffinitySeed(sessionID string, body []byte) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	model := ""
	if len(body) > 0 {
		model = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "model").String()))
	}
	if model == "" {
		return "grok-affinity:v1:" + sessionID
	}
	return "grok-affinity:v1:" + model + ":" + sessionID
}

// GenerateSessionHashWithFallback 先按常规信号生成会话哈希；
// 当未携带 session_id/conversation_id/prompt_cache_key 时，使用 fallbackSeed 生成稳定哈希。
// 该方法用于 WS ingress，避免会话信号缺失时发生跨账号漂移。
func (s *OpenAIGatewayService) GenerateSessionHashWithFallback(c *gin.Context, body []byte, fallbackSeed string) string {
	if c == nil {
		return ""
	}
	if monitorProbeRequest(c) {
		return ""
	}
	sessionHash := s.GenerateSessionHash(c, body)
	if sessionHash != "" {
		return sessionHash
	}

	seed := strings.TrimSpace(fallbackSeed)
	if seed == "" {
		return ""
	}

	seed = s.scopedOpenAISessionSeed(c, body, seed)
	currentHash, legacyHash := deriveOpenAISessionHashes(seed)
	if strings.HasPrefix(seed, "openai-routing-v2:") {
		currentHash = "v2:" + currentHash
		legacyHash = ""
	}
	c.Set(openAICacheSessionGinKey, currentHash)
	if c.Request != nil {
		if diagnostic := s.newCacheDiagnostic(c, body, "ingress"); diagnostic != nil {
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), openAICacheDiagnosticKey{}, diagnostic))
		}
	}
	attachOpenAILegacySessionHashToGin(c, legacyHash)
	s.AttachOpenAIQualityRouting(c, currentHash, body)
	return currentHash
}

func resolveOpenAIUpstreamOriginator(c *gin.Context, isOfficialClient bool) string {
	if c != nil {
		if originator := strings.TrimSpace(c.GetHeader("originator")); originator != "" {
			return originator
		}
	}
	if isOfficialClient {
		return openai.CodexDefaultOriginator
	}
	return "opencode"
}

// BindStickySession sets session -> account binding with standard TTL.
func (s *OpenAIGatewayService) BindStickySession(ctx context.Context, groupID *int64, sessionHash string, accountID int64) error {
	if sessionHash == "" || accountID <= 0 {
		return nil
	}
	if !resilienceBindAllowed(ctx, accountID) {
		return nil
	}
	return s.setStickySessionAccountID(ctx, groupID, sessionHash, accountID, s.openAIStickyBindingTTL(groupID))
}

// SelectAccount selects an OpenAI account with sticky session support
func (s *OpenAIGatewayService) SelectAccount(ctx context.Context, groupID *int64, sessionHash string) (*Account, error) {
	return s.SelectAccountForModel(ctx, groupID, sessionHash, "")
}

// SelectAccountForModel selects an account supporting the requested model
func (s *OpenAIGatewayService) SelectAccountForModel(ctx context.Context, groupID *int64, sessionHash string, requestedModel string) (*Account, error) {
	return s.SelectAccountForModelWithExclusions(ctx, groupID, sessionHash, requestedModel, nil)
}

// SelectAccountForModelWithExclusions selects an account supporting the requested model while excluding specified accounts.
// SelectAccountForModelWithExclusions 选择支持指定模型的账号，同时排除指定的账号。
func (s *OpenAIGatewayService) SelectAccountForModelWithExclusions(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*Account, error) {
	return s.selectAccountForModelWithExclusions(s.withOpenAIQuotaAutoPauseContext(ctx), groupID, PlatformOpenAI, sessionHash, requestedModel, excludedIDs, false, 0, "", false)
}

// SelectAccountForTokenCount selects an account for a non-billable token-count
// request. It applies the normal platform, model, capability, and runtime
// eligibility checks without acquiring or waiting for a generation slot.
func (s *OpenAIGatewayService) SelectAccountForTokenCount(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	requestedModel string,
	requiredCapability OpenAIEndpointCapability,
	platform string,
) (*Account, error) {
	ctx = WithOpenAIProfitControlSuppressed(ctx)
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	return s.selectAccountForModelWithExclusions(
		ctx,
		groupID,
		platform,
		sessionHash,
		requestedModel,
		nil,
		false,
		0,
		requiredCapability,
		false,
	)
}

// NormalizeOpenAICompatiblePlatform 保留走 OpenAI 网关的平台（openai、grok 与多协议
// API Key 供应商，见平台清单）的原值，其他值一律归一为 openai。调度器据此对账号与请求做精确平台匹配：
// kimi 分组请求只命中 kimi 账号，语义与 openai/grok 一致。
// （upstream 曾将本函数改为未导出 normalizeOpenAICompatiblePlatform，本分支的
// handler 调度入口仍需导出，保持导出名。）
func NormalizeOpenAICompatiblePlatform(platform string) string {
	if domain.UsesOpenAIGateway(platform) {
		return platform
	}
	return PlatformOpenAI
}

// noAvailableOpenAISelectionError builds the standard "no account available" error
// while preserving the legacy /responses/compact error when applicable.
// details carries an optional machine-parseable exclusion summary (e.g.
// "pool=2, filtered: quota_auto_pause_7d=1 runtime_blocked=1") appended in
// parentheses. It is for server-side logs / ops diagnostics only: handlers
// never forward this error text to OpenAI-platform clients (they respond with
// the generic classification message). Callers that must preserve the legacy
// message pass "".
func noAvailableOpenAISelectionError(requestedModel string, compactBlocked bool, details string, capabilityOnly ...bool) error {
	if compactBlocked {
		return ErrNoAvailableCompactAccounts
	}
	message := "no available OpenAI accounts"
	if requestedModel != "" {
		message = fmt.Sprintf("no available OpenAI accounts supporting model: %s", requestedModel)
	}
	if details != "" {
		message += " (" + details + ")"
	}
	return openAINoAvailableSelectionError{message: message, capabilityOnly: len(capabilityOnly) > 0 && capabilityOnly[0]}
}

type openAINoAvailableSelectionError struct {
	message        string
	capabilityOnly bool
}

func (e openAINoAvailableSelectionError) CapabilityOnly() bool { return e.capabilityOnly }

func (e openAINoAvailableSelectionError) Error() string {
	return e.message
}

func (e openAINoAvailableSelectionError) Unwrap() error {
	return ErrNoAvailableAccounts
}

// openAICompactSupportTier classifies an OpenAI-compatible account by compact capability.
// 0 = explicitly unsupported, 1 = unknown / not yet probed, 2 = explicitly supported.
func openAICompactSupportTier(account *Account) int {
	if account == nil {
		return 0
	}
	if account.IsGrok() {
		return 2
	}
	if !account.IsOpenAI() {
		return 0
	}
	supported, known := account.OpenAICompactSupportKnown()
	if !known {
		return 1
	}
	if supported {
		return 2
	}
	return 0
}

// isOpenAICompatibleAccountEligibleForRequest 判断 OpenAI 兼容账号是否满足本次请求的调度条件。
// 检查内容包括：平台匹配、账号可用性、quota 自动暂停、spark 路由限制、模型支持及端点能力。
//
// 注意：对 spark 影子账号，调用方还须额外调用 parentHealthyForShadow(account, lookup)
// 检查母账号凭据可用性；该检查未内置于本函数，以避免注入 DB 依赖。
func isOpenAICompatibleAccountEligibleForRequest(ctx context.Context, account *Account, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability, gateway ...*OpenAIGatewayService) bool {
	return openAICompatibleAccountEligibilityFailureReason(ctx, account, platform, requestedModel, requireCompact, requiredCapability, gateway...) == ""
}

// openAICompatibleAccountEligibilityFailureReason mirrors the legacy boolean
// eligibility check while naming its first veto point. Load-batch selection uses
// the reason only for server-side no-account diagnostics; the admission behavior
// remains unchanged.
func openAICompatibleAccountEligibilityFailureReason(ctx context.Context, account *Account, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability, gateway ...*OpenAIGatewayService) string {
	if reason := openAICompatibleAccountEligibilityFailureReasonBeforeProfit(ctx, account, platform, requestedModel, requireCompact, requiredCapability, gateway...); reason != "" {
		return reason
	}
	// 分组利润控制：legacy 引擎的粘性/候选循环与 DB recheck 共用
	// 本判定，任何 fallback 都不能把利润不合格账号重新放回候选。
	if vetoed, reason := openAIProfitControlVetoReason(ctx, account); vetoed {
		return reason
	}
	return ""
}

// isOpenAICompatibleAccountEligibleForRequestBeforeProfit applies every
// ordinary scheduling gate. Legacy selection uses it before classifying the
// profit veto so earlier failures retain their actual reason.
func isOpenAICompatibleAccountEligibleForRequestBeforeProfit(ctx context.Context, account *Account, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability, gateway ...*OpenAIGatewayService) bool {
	return openAICompatibleAccountEligibilityFailureReasonBeforeProfit(ctx, account, platform, requestedModel, requireCompact, requiredCapability, gateway...) == ""
}

func openAICompatibleAccountEligibilityFailureReasonBeforeProfit(ctx context.Context, account *Account, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability, gateway ...*OpenAIGatewayService) string {
	platform = NormalizeOpenAICompatiblePlatform(platform)
	if account == nil {
		return "account_nil"
	}
	// account_model composite routes publish an alias that only accounts with an
	// explicit model mapping own. Both scheduler modes (advanced and legacy) must
	// enforce ownership before any priority/sticky/transport consideration; a
	// non-owner would forward the raw public alias and get model_not_found or
	// hit an unrelated same-name upstream model.
	if source, ok := CompositeRouteSourceFromContext(ctx); ok && source == CompositeRouteSourceAccount {
		if publicModel, modelOK := RequestedPublicModelFromContext(ctx); modelOK && !explicitModelMappingClaims(*account, publicModel) {
			return "account_model_not_owned"
		}
	}
	if account.Platform != platform || !account.IsOpenAICompatible() {
		return "platform_mismatch"
	}
	if !account.IsSchedulableForModelWithContext(ctx, requestedModel) {
		if account.IsSchedulable() {
			return "model_rate_limited"
		}
		return "not_schedulable"
	}
	if account.IsOpenAI() {
		if paused, reason := shouldAutoPauseOpenAIAccountByQuota(ctx, account); paused {
			// Debug level: this fires per-candidate on the scheduling hot path, so Info
			// would amplify into log spam once several accounts cross the threshold.
			slog.Debug("account_auto_paused_by_quota",
				"account_id", account.ID,
				"window", reason.window,
				"threshold", reason.threshold,
				"utilization", reason.utilization,
			)
			if reason.window != "" {
				return "quota_auto_pause_" + reason.window
			}
			return "quota_auto_pause"
		}
	}
	if account.IsGrok() {
		if paused, reason := shouldAutoPauseGrokAccountByQuota(account); paused {
			slog.Debug("grok_account_auto_paused_by_quota",
				"account_id", account.ID,
				"window", reason.window,
				"threshold", reason.threshold,
				"utilization", reason.utilization,
			)
			if reason.window != "" {
				return "quota_auto_pause_" + reason.window
			}
			return "quota_auto_pause"
		}
	}
	if requestedModel != "" && !account.IsModelSupported(requestedModel) {
		return "model_not_supported"
	}
	if !account.SupportsOpenAIEndpointCapability(requiredCapability) {
		if account.IsGrok() && requiredCapability == OpenAIEndpointCapabilityGrokMediaGeneration {
			_, reason := account.GrokMediaGenerationEligibility()
			slog.Debug("grok_media_account_ineligible", "account_id", account.ID, "reason", reason)
		}
		return "capability_mismatch"
	}
	if account.Platform == PlatformOpenAI && requireCompact {
		compactOutbound := ""
		if len(gateway) > 0 && gateway[0] != nil {
			compactOutbound = gateway[0].openAICodexTicketOutboundModel(account, requestedModel, true)
		}
		if iqGateBlocksRequest(account, requestedModel, true, compactOutbound) {
			return "model_rate_limited"
		}
	}
	if requireCompact && openAICompactSupportTier(account) == 0 {
		return "compact_unsupported"
	}
	if !requireCompact {
		if reason := capabilityEligibilityReason(ctx, account, requestedModel, time.Now()); reason != "" {
			return reason
		}
	}

	return ""
}

type openAIQuotaAutoPauseDecision struct {
	window      string
	threshold   float64
	utilization float64
	reason      string
}

func shouldAutoPauseGrokAccountByQuota(account *Account) (bool, openAIQuotaAutoPauseDecision) {
	if account == nil || !account.IsGrok() || account.Type != AccountTypeOAuth {
		return false, openAIQuotaAutoPauseDecision{}
	}
	snapshot, err := grokQuotaSnapshotFromExtra(account.Extra)
	if err != nil || snapshot == nil {
		return false, openAIQuotaAutoPauseDecision{}
	}
	now := time.Now()
	if grokQuotaSnapshotStaleForPause(snapshot, now) {
		return false, openAIQuotaAutoPauseDecision{}
	}
	if grokQuotaRetryAfterActive(snapshot, now) {
		return true, openAIQuotaAutoPauseDecision{window: "retry_after", threshold: 1, utilization: 1}
	}
	if paused, decision := shouldAutoPauseGrokQuotaWindow("requests", snapshot.Requests, now); paused {
		return true, decision
	}
	if paused, decision := shouldAutoPauseGrokQuotaWindow("tokens", snapshot.Tokens, now); paused {
		return true, decision
	}
	return false, openAIQuotaAutoPauseDecision{}
}

func grokQuotaRetryAfterActive(snapshot *xai.QuotaSnapshot, now time.Time) bool {
	if snapshot == nil || snapshot.RetryAfterSeconds == nil || *snapshot.RetryAfterSeconds <= 0 {
		return false
	}
	if strings.TrimSpace(snapshot.UpdatedAt) == "" {
		return true
	}
	updatedAt, err := parseTime(snapshot.UpdatedAt)
	if err != nil {
		return true
	}
	retryAfterUntil := updatedAt.Add(time.Duration(*snapshot.RetryAfterSeconds) * time.Second)
	return now.Before(retryAfterUntil)
}

func shouldAutoPauseGrokQuotaWindow(name string, window *xai.QuotaWindow, now time.Time) (bool, openAIQuotaAutoPauseDecision) {
	if window == nil || window.Limit == nil || window.Remaining == nil || *window.Limit <= 0 {
		return false, openAIQuotaAutoPauseDecision{}
	}
	if window.ResetUnix != nil && *window.ResetUnix > 0 && !now.Before(time.Unix(*window.ResetUnix, 0)) {
		return false, openAIQuotaAutoPauseDecision{}
	}
	utilization := float64(*window.Limit-*window.Remaining) / float64(*window.Limit)
	if *window.Remaining <= 0 || utilization >= 1 {
		return true, openAIQuotaAutoPauseDecision{window: name, threshold: 1, utilization: utilization}
	}
	return false, openAIQuotaAutoPauseDecision{}
}

func grokQuotaSnapshotStaleForPause(snapshot *xai.QuotaSnapshot, now time.Time) bool {
	if snapshot == nil || strings.TrimSpace(snapshot.UpdatedAt) == "" {
		return false
	}
	updatedAt, err := parseTime(snapshot.UpdatedAt)
	if err != nil {
		return false
	}
	return now.Sub(updatedAt) >= openAICodexAutoPauseStaleAfter
}

func shouldAutoPauseOpenAIAccountByQuota(ctx context.Context, account *Account) (bool, openAIQuotaAutoPauseDecision) {
	if account == nil || !account.IsOpenAI() {
		return false, openAIQuotaAutoPauseDecision{}
	}
	// 自动用卡有独立阈值：达到消费阈值时必须先退出调度；仅达到普通暂停阈值时，
	// 只有新鲜状态明确存在可用卡才继续放行到消费阈值。
	if config := ResolveOpenAIAutoResetCreditConfig(account); config.Enabled {
		now := time.Now()
		utilization5h, has5h := resolveOpenAIQuotaUtilization(account.Extra, "5h", now)
		utilization7d, has7d := resolveOpenAIQuotaUtilization(account.Extra, "7d", now)
		if has5h && utilization5h >= config.Threshold5h {
			notifyOpenAIAutoResetFromScheduler(account.ID)
			return true, openAIQuotaAutoPauseDecision{window: "5h", threshold: config.Threshold5h, utilization: utilization5h, reason: "quota_auto_reset_pending_5h"}
		}
		if has7d && utilization7d >= config.Threshold7d {
			notifyOpenAIAutoResetFromScheduler(account.ID)
			return true, openAIQuotaAutoPauseDecision{window: "7d", threshold: config.Threshold7d, utilization: utilization7d, reason: "quota_auto_reset_pending_7d"}
		}

		disabled5h := resolveAccountExtraBool(account.Extra, "auto_pause_5h_disabled")
		disabled7d := resolveAccountExtraBool(account.Extra, "auto_pause_7d_disabled")
		pause5h, pause7d := resolveOpenAIQuotaAutoPauseThresholds(ctx, account)
		pauseReached5h := !disabled5h && pause5h > 0 && has5h && utilization5h >= pause5h
		pauseReached7d := !disabled7d && pause7d > 0 && has7d && utilization7d >= pause7d
		if pauseReached5h || pauseReached7d {
			state := openAIAutoResetStateFromExtra(account.Extra)
			if state != nil && state.Status == OpenAIAutoResetStatusAvailable && state.AvailableCount > 0 && !openAIAutoResetStateStale(state, now) {
				return false, openAIQuotaAutoPauseDecision{}
			}
			notifyOpenAIAutoResetFromScheduler(account.ID)
			if pauseReached5h {
				return true, openAIQuotaAutoPauseDecision{window: "5h", threshold: pause5h, utilization: utilization5h, reason: "quota_auto_reset_credit_check_5h"}
			}
			return true, openAIQuotaAutoPauseDecision{window: "7d", threshold: pause7d, utilization: utilization7d, reason: "quota_auto_reset_credit_check_7d"}
		}
	}
	// Per-account explicit-disable flags must take precedence over the global default.
	// Without these, leaving the account threshold blank means "use global default",
	// so an admin has no way to exempt a single account from auto-pause once a global
	// default exists. The disable flag is per-window so an account can opt out of
	// only 5h or only 7d auto-pause.
	disabled5h := resolveAccountExtraBool(account.Extra, "auto_pause_5h_disabled")
	disabled7d := resolveAccountExtraBool(account.Extra, "auto_pause_7d_disabled")
	threshold5h, threshold7d := resolveOpenAIQuotaAutoPauseThresholds(ctx, account)
	now := time.Now()
	if !disabled5h && threshold5h > 0 {
		if utilization, ok := resolveOpenAIQuotaUtilization(account.Extra, "5h", now); ok && utilization >= threshold5h {
			return true, openAIQuotaAutoPauseDecision{window: "5h", threshold: threshold5h, utilization: utilization}
		}
	}
	if !disabled7d && threshold7d > 0 {
		if utilization, ok := resolveOpenAIQuotaUtilization(account.Extra, "7d", now); ok && utilization >= threshold7d {
			return true, openAIQuotaAutoPauseDecision{window: "7d", threshold: threshold7d, utilization: utilization}
		}
	}
	return false, openAIQuotaAutoPauseDecision{}
}

// resolveAccountExtraBool reads a bool-like value from account extra, tolerating
// the few shapes JSON unmarshalling may produce (real bool, "true"/"false"
// strings, 0/1 numbers).
func resolveAccountExtraBool(extra map[string]any, key string) bool {
	if len(extra) == 0 {
		return false
	}
	value, ok := extra[key]
	if !ok || value == nil {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		return err == nil && parsed
	case float64:
		return v != 0
	case float32:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i != 0
		}
	}
	return false
}

func resolveOpenAIQuotaAutoPauseThresholds(ctx context.Context, account *Account) (float64, float64) {
	threshold5h, _ := resolveAccountExtraNumber(account.Extra, "auto_pause_5h_threshold")
	threshold7d, _ := resolveAccountExtraNumber(account.Extra, "auto_pause_7d_threshold")
	threshold5h = clamp01(threshold5h)
	threshold7d = clamp01(threshold7d)
	if threshold5h > 0 && threshold7d > 0 {
		return threshold5h, threshold7d
	}
	settings := openAIQuotaAutoPauseSettingsFromContext(ctx)
	if threshold5h <= 0 {
		threshold5h = clamp01(settings.DefaultThreshold5h)
	}
	if threshold7d <= 0 {
		threshold7d = clamp01(settings.DefaultThreshold7d)
	}
	return threshold5h, threshold7d
}

func resolveAccountExtraNumber(extra map[string]any, keys ...string) (float64, bool) {
	if len(extra) == 0 {
		return 0, false
	}
	for _, key := range keys {
		value, ok := extra[key]
		if !ok || value == nil {
			continue
		}
		switch v := value.(type) {
		case float64:
			return v, true
		case float32:
			return float64(v), true
		case int:
			return float64(v), true
		case int64:
			return float64(v), true
		case json.Number:
			parsed, err := v.Float64()
			if err == nil {
				return parsed, true
			}
		case string:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

// resolveOpenAIQuotaUtilization returns the current utilization ratio (0..1) for the
// given Codex usage window. ok=false means there is no usable signal to pause on:
// either no snapshot exists, the window has rolled over, or an old snapshot has
// no known future reset. The stale guard matters because a paused account stops
// receiving requests, so its snapshot is never refreshed from upstream headers —
// without this check an old used_percent would keep the account paused forever even
// after the real window reset.
func resolveOpenAIQuotaUtilization(extra map[string]any, window string, now time.Time) (float64, bool) {
	usedPercent := readOpenAIQuotaUsedPercent(extra, window)
	if usedPercent <= 0 {
		return 0, false
	}
	if openAIQuotaWindowReset(extra, window, now) {
		return 0, false
	}
	// 快照过于陈旧且没有明确的未来重置时间时，不再据此暂停。放行后下一次响应头
	// 会刷新快照实现自愈，避免账号在错误/过期的 used% 上被永久跳过（issue #2994）。
	if openAICodexSnapshotStaleForPause(extra, now) && !openAIQuotaWindowResetPending(extra, window, now) {
		return 0, false
	}
	return usedPercent / 100, true
}

func openAIQuotaWindowResetPending(extra map[string]any, window string, now time.Time) bool {
	resetAt, ok := openAICodexWindowResetAt(extra, window)
	return ok && now.Before(resetAt)
}

// openAICodexSnapshotStaleForPause reports whether the Codex usage snapshot is stale
// enough that it should no longer keep an account auto-paused. It anchors on
// codex_usage_updated_at (always written by buildCodexUsageExtraUpdates). A missing or
// unparseable timestamp returns false (treated as fresh, so the account stays paused) —
// this is deliberate: it prevents any snapshot without a write time from silently escaping
// auto-pause, and a genuinely-exhausted account that is actively served refreshes the
// timestamp on every response so it never crosses the staleness bound.
func openAICodexSnapshotStaleForPause(extra map[string]any, now time.Time) bool {
	if len(extra) == 0 {
		return false
	}
	updatedRaw, ok := extra["codex_usage_updated_at"]
	if !ok {
		return false
	}
	updatedAt, err := parseTime(fmt.Sprint(updatedRaw))
	if err != nil {
		return false
	}
	return now.Sub(updatedAt) >= openAICodexAutoPauseStaleAfter
}

// openAIQuotaWindowReset reports whether the Codex usage window's reset time has
// already passed relative to now. It prefers the absolute codex_<window>_reset_at
// timestamp and falls back to codex_<window>_reset_after_seconds anchored at
// codex_usage_updated_at, mirroring AccountUsageService's window-progress logic.
func openAIQuotaWindowReset(extra map[string]any, window string, now time.Time) bool {
	resetAt, ok := openAICodexWindowResetAt(extra, window)
	return ok && !now.Before(resetAt)
}

// 绝对时间优先；相对倒计时必须锚定快照采样时间，不能随每次评分向后滑动。
func openAICodexWindowResetAt(extra map[string]any, window string) (time.Time, bool) {
	if len(extra) == 0 {
		return time.Time{}, false
	}
	if resetAtRaw, ok := extra["codex_"+window+"_reset_at"]; ok {
		if resetAt, err := parseTime(fmt.Sprint(resetAtRaw)); err == nil {
			return resetAt, true
		}
	}
	resetAfter := parseExtraInt(extra["codex_"+window+"_reset_after_seconds"])
	if resetAfter <= 0 {
		return time.Time{}, false
	}
	updatedAt, err := parseTime(fmt.Sprint(extra["codex_usage_updated_at"]))
	if err != nil {
		return time.Time{}, false
	}
	return updatedAt.Add(time.Duration(resetAfter) * time.Second), true
}

func readOpenAIQuotaUsedPercent(extra map[string]any, window string) float64 {
	if len(extra) == 0 {
		return 0
	}
	if value, ok := resolveAccountExtraNumber(extra, "codex_"+window+"_used_percent"); ok {
		return value
	}
	return 0
}

type openAIQuotaAutoPauseCtxKey struct{}

func withOpenAIQuotaAutoPauseSettings(ctx context.Context, settings OpsOpenAIAccountQuotaAutoPauseSettings) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIQuotaAutoPauseCtxKey{}, settings)
}

func openAIQuotaAutoPauseSettingsFromContext(ctx context.Context) OpsOpenAIAccountQuotaAutoPauseSettings {
	if ctx == nil {
		return OpsOpenAIAccountQuotaAutoPauseSettings{}
	}
	settings, _ := ctx.Value(openAIQuotaAutoPauseCtxKey{}).(OpsOpenAIAccountQuotaAutoPauseSettings)
	return settings
}

func (s *OpenAIGatewayService) withOpenAIQuotaAutoPauseContext(ctx context.Context) context.Context {
	if s == nil || s.settingService == nil {
		return ctx
	}
	return withOpenAIQuotaAutoPauseSettings(ctx, s.settingService.GetOpenAIQuotaAutoPauseSettings(ctx))
}

// prioritizeOpenAICompactAccounts re-orders a slice so that accounts with known
// compact support are tried first, followed by unknown, then explicitly unsupported.
// The relative order within each tier is preserved.
func prioritizeOpenAICompactAccounts(accounts []*Account) []*Account {
	if len(accounts) == 0 {
		return nil
	}
	supported := make([]*Account, 0, len(accounts))
	unknown := make([]*Account, 0, len(accounts))
	unsupported := make([]*Account, 0, len(accounts))
	for _, account := range accounts {
		switch openAICompactSupportTier(account) {
		case 2:
			supported = append(supported, account)
		case 1:
			unknown = append(unknown, account)
		default:
			unsupported = append(unsupported, account)
		}
	}
	out := make([]*Account, 0, len(accounts))
	out = append(out, supported...)
	out = append(out, unknown...)
	out = append(out, unsupported...)
	return out
}

// resolveOpenAIAccountUpstreamModelForRequest resolves the upstream model that
// would be sent for a given request, honoring the legacy compact-only mapping
// when the caller is on the /responses/compact path.
func resolveOpenAIAccountUpstreamModelForRequest(account *Account, requestedModel string, requireCompact bool) string {
	// Forward checks the raw Chat Completions fallback before passthrough.
	// These API-key accounts therefore apply normal account model_mapping and
	// upstream normalization, but never compact_model_mapping.
	if shouldForwardOpenAIResponsesViaRawChatCompletions(account) {
		upstreamModel := resolveOpenAIForwardModel(account, requestedModel, "")
		return normalizeOpenAIModelForUpstream(account, upstreamModel)
	}

	// Passthrough accounts only replace authentication. Their Forward path
	// keeps the channel-mapped model in the request body and does not apply the
	// account's normal model_mapping. Legacy /responses/compact is the one
	// exception: forwardOpenAIPassthrough applies compact_model_mapping
	// directly to that channel-mapped model.
	if account != nil && account.IsOpenAIPassthroughEnabled() {
		upstreamModel := strings.TrimSpace(requestedModel)
		if upstreamModel == "" {
			return ""
		}
		if requireCompact {
			return resolveOpenAICompactForwardModel(account, upstreamModel)
		}
		return upstreamModel
	}

	// Compact mappings are keyed by the client-visible model. Prefer an exact
	// compact rule before ordinary account mapping; otherwise a normal alias can
	// hide the compact-specific rule and make scheduling disagree with Forward.
	if requireCompact && account != nil {
		if compactModel, matched := account.ResolveCompactMappedModel(strings.TrimSpace(requestedModel)); matched {
			if compactModel = strings.TrimSpace(compactModel); compactModel != "" {
				return compactModel
			}
		}
	}

	upstreamModel := resolveOpenAIForwardModel(account, requestedModel, "")
	if upstreamModel == "" {
		return ""
	}
	if requireCompact {
		compactModel := resolveOpenAICompactForwardModel(account, upstreamModel)
		if compactModel != upstreamModel {
			return compactModel
		}
	}
	return normalizeOpenAIModelForUpstream(account, upstreamModel)
}

// ResolveOpenAIAccountUpstreamModelForRequest exposes the scheduler's exact
// account mapping chain to handler-side outcome reporting.
func ResolveOpenAIAccountUpstreamModelForRequest(account *Account, requestedModel string, requireCompact bool) string {
	return resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, requireCompact)
}

// resolveOpenAIForwardMappedModels is the shared account mapping chain for
// Forward callers. billingModel retains the ordinary mapping used for usage
// accounting, while upstreamModel is the model the scheduler has admitted.
func resolveOpenAIForwardMappedModels(account *Account, requestedModel string, requireCompact bool) (billingModel, upstreamModel string) {
	requestedModel = strings.TrimSpace(requestedModel)
	if account != nil && account.IsOpenAIPassthroughEnabled() {
		billingModel = requestedModel
	} else if account != nil {
		billingModel = strings.TrimSpace(account.GetMappedModel(requestedModel))
	}
	if billingModel == "" {
		billingModel = requestedModel
	}
	upstreamModel = resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, requireCompact)
	if strings.TrimSpace(upstreamModel) == "" {
		upstreamModel = billingModel
	}
	return billingModel, upstreamModel
}

func resolveOpenAIErrorSchedulingModel(billingModel, upstreamModel string) string {
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		return upstreamModel
	}
	return strings.TrimSpace(billingModel)
}

func (s *OpenAIGatewayService) selectAccountForModelWithExclusions(ctx context.Context, groupID *int64, platform string, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, stickyAccountID int64, requiredCapability OpenAIEndpointCapability, preferLowUpstreamRate bool) (*Account, error) {
	account, _, err := s.selectAccountForModelWithExclusionsStickyHit(ctx, groupID, platform, sessionHash, requestedModel, excludedIDs, requireCompact, stickyAccountID, requiredCapability, preferLowUpstreamRate)
	return account, err
}

// selectAccountForModelWithExclusionsStickyHit 与 selectAccountForModelWithExclusions 相同，
// 另返回账号是否来自粘性会话命中。
func (s *OpenAIGatewayService) selectAccountForModelWithExclusionsStickyHit(ctx context.Context, groupID *int64, platform string, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, stickyAccountID int64, requiredCapability OpenAIEndpointCapability, preferLowUpstreamRate bool) (*Account, bool, error) {
	platform = NormalizeOpenAICompatiblePlatform(platform)
	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		slog.Warn("channel pricing restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, false, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
	}

	// 1. 尝试粘性会话命中
	// Try sticky session hit
	if account := s.tryStickySessionHit(ctx, groupID, platform, sessionHash, requestedModel, excludedIDs, requireCompact, stickyAccountID, requiredCapability); account != nil {
		return account, true, nil
	}

	// 2. 获取可调度的 OpenAI 账号
	// Get schedulable OpenAI accounts
	accounts, err := s.listSchedulableAccounts(ctx, groupID, platform)
	if err != nil {
		return nil, false, fmt.Errorf("query accounts failed: %w", err)
	}

	// 3. 按优先级 + LRU 选择最佳账号
	// Select by priority + LRU
	selected, compactBlocked, filterStats := s.selectBestAccount(ctx, groupID, platform, accounts, requestedModel, excludedIDs, requireCompact, requiredCapability, preferLowUpstreamRate)

	if selected == nil {
		return nil, false, capabilitySelectionError(ctx, noAvailableOpenAISelectionError(requestedModel, compactBlocked, filterStats.summary(""), filterStats.capabilityOnly()))
	}

	hydrated, err := s.hydrateSelectedAccount(ctx, selected)
	if err != nil {
		return nil, false, err
	}

	// 4. 设置粘性会话绑定（利润门下推迟到 handler 终检通过后再绑定，
	// 终检否决的账号不得成为新的粘性目标；无门保持既有 eager 绑定与 TTL）
	// Set sticky session binding (deferred until terminal admission under a profit gate)
	if sessionHash != "" && !gatewayProfitControlGateActive(ctx) {
		_ = s.setStickySessionAccountID(ctx, groupID, sessionHash, selected.ID, openaiStickySessionTTL)
	}

	return hydrated, false, nil
}

// tryStickySessionHit 尝试从粘性会话获取账号。
// 如果命中且账号可用则返回账号；如果账号不可用则清理会话并返回 nil。
//
// tryStickySessionHit attempts to get account from sticky session.
// Returns account if hit and usable; clears session and returns nil if account is unavailable.
func (s *OpenAIGatewayService) tryStickySessionHit(ctx context.Context, groupID *int64, platform string, sessionHash, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, stickyAccountID int64, requiredCapability OpenAIEndpointCapability) *Account {
	if sessionHash == "" {
		return nil
	}
	platform = NormalizeOpenAICompatiblePlatform(platform)

	accountID := stickyAccountID
	if accountID <= 0 {
		var err error
		accountID, err = s.getStickySessionAccountID(ctx, groupID, sessionHash)
		if err != nil || accountID <= 0 {
			return nil
		}
	}

	account, err := s.getSchedulableAccount(ctx, accountID)
	if err != nil {
		resilience.StateFrom(ctx).NoteStickyBypassReason("owner_lookup_failed")
		return nil
	}
	if account == nil {
		resilience.StateFrom(ctx).NoteStickyBypassReason("owner_unavailable")
		return nil
	}

	if _, excluded := excludedIDs[accountID]; excluded {
		resilience.StateFrom(ctx).NoteStickyBypassReason("owner_excluded")
		return nil
	}
	// xy2api resilience: a soft-penalised owner keeps the binding (design §4.2).
	if s.resilienceKeepOpenAISoftOwner(ctx, account, platform, requestedModel) {
		resilience.StateFrom(ctx).NoteStickyBypassReason("soft_owner_preserved")
		return nil
	}

	// 检查账号是否需要清理粘性会话
	// Check if sticky session should be cleared
	if shouldClearStickySession(account, requestedModel) {
		resilience.StateFrom(ctx).NoteStickyBypassReason("owner_should_clear")
		_ = s.deleteStickySessionAccountIDIfOwner(ctx, groupID, sessionHash, accountID)
		return nil
	}

	// 验证账号是否可用于当前请求
	// Verify account is usable for current request
	if !isOpenAICompatibleAccountEligibleForRequest(ctx, account, platform, requestedModel, false, requiredCapability) {
		resilience.StateFrom(ctx).NoteStickyBypassReason("owner_ineligible")
		return nil
	}
	if !parentHealthyForShadow(account, s.parentAccountLookup(ctx)) {
		resilience.StateFrom(ctx).NoteStickyBypassReason("shadow_parent_unhealthy")
		_ = s.deleteStickySessionAccountIDIfOwner(ctx, groupID, sessionHash, accountID)
		return nil
	}
	if s.isOpenAIAccountRequestRuntimeBlocked(account, requestedModel, requireCompact) {
		resilience.StateFrom(ctx).NoteStickyBypassReason("runtime_blocked")
		_ = s.deleteStickySessionAccountIDIfOwner(ctx, groupID, sessionHash, accountID)
		return nil
	}
	account, transient := s.recheckOpenAIStickyOwnerFromDB(ctx, account, groupID, platform, requestedModel, requireCompact, requiredCapability)
	if transient {
		resilience.StateFrom(ctx).NoteStickyBypassReason("owner_recheck_transient")
		return nil
	}
	if account == nil || !s.openAIAccountMatchesSchedulingGroup(account, groupID) {
		resilience.StateFrom(ctx).NoteStickyBypassReason("owner_group_mismatch")
		_ = s.deleteStickySessionAccountIDIfOwner(ctx, groupID, sessionHash, accountID)
		return nil
	}
	if groupID != nil && s.needsUpstreamChannelRestrictionCheck(ctx, groupID) &&
		s.isUpstreamModelRestrictedByChannel(ctx, *groupID, account, requestedModel, requireCompact) {
		resilience.StateFrom(ctx).NoteStickyBypassReason("channel_upstream_restricted")
		_ = s.deleteStickySessionAccountIDIfOwner(ctx, groupID, sessionHash, accountID)
		return nil
	}

	// 刷新会话 TTL 并返回账号
	// Refresh session TTL and return account
	_ = s.refreshStickySessionTTL(ctx, groupID, sessionHash, s.openAIStickyBindingTTL(groupID), accountID)
	return account
}

// selectBestAccount 从候选账号中选择最佳账号（优先级 + LRU）。
// 返回 nil 表示无可用账号。
//
// selectBestAccount selects the best account from candidates (priority + LRU).
// Returns nil if no available account. The second return reports whether at
// least one candidate was filtered out solely because it lacks compact support
// (only meaningful when the legacy /responses/compact requireCompact flag is
// true); the third contains deterministic
// exclusion diagnostics for the evaluated snapshot.
func (s *OpenAIGatewayService) selectBestAccount(ctx context.Context, groupID *int64, platform string, accounts []Account, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, requiredCapability OpenAIEndpointCapability, preferLowUpstreamRate bool) (*Account, bool, openAISelectionFilterStats) {
	platform = NormalizeOpenAICompatiblePlatform(platform)
	compactBlocked := false
	filterStats := openAISelectionFilterStats{pool: len(accounts)}
	needsUpstreamCheck := s.needsUpstreamChannelRestrictionCheck(ctx, groupID)
	eligible := make([]*Account, 0, len(accounts))
	compactTiers := make(map[int64]int, len(accounts))

	for i := range accounts {
		acc := &accounts[i]

		// 跳过被排除的账号
		// Skip excluded accounts
		if _, excluded := excludedIDs[acc.ID]; excluded {
			filterStats.exclude("excluded")
			continue
		}

		fresh := s.resolveFreshSchedulableOpenAIAccountBeforeProfit(ctx, acc, platform, requestedModel, false, requiredCapability)
		if fresh == nil {
			reason := openAICompatibleAccountEligibilityFailureReasonBeforeProfit(ctx, acc, platform, requestedModel, false, requiredCapability)
			if reason != resilience.ReasonCapabilityStreamOnly {
				reason = "ineligible"
			}
			filterStats.exclude(reason)
			continue
		}
		fresh = s.recheckSelectedOpenAIAccountFromDBBeforeProfit(ctx, fresh, groupID, platform, requestedModel, false, requiredCapability)
		if fresh == nil {
			filterStats.exclude("ineligible")
			continue
		}
		if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, fresh, requestedModel, requireCompact) {
			filterStats.exclude("channel_restricted")
			continue
		}
		if vetoed, reason := openAIProfitControlVetoReason(ctx, fresh); vetoed {
			filterStats.exclude(reason)
			continue
		}
		compactTier := 0
		if requireCompact {
			compactTier = openAICompactSupportTier(fresh)
			if compactTier == 0 {
				compactBlocked = true
				filterStats.exclude("compact_unsupported")
				continue
			}
		}

		eligible = append(eligible, fresh)
		compactTiers[fresh.ID] = compactTier
	}

	if len(eligible) == 0 {
		return nil, compactBlocked, filterStats
	}
	rateOrder := openAILegacyUpstreamRateOrder{}
	if preferLowUpstreamRate {
		rateOrder = newOpenAILegacyUpstreamRateOrder(eligible, time.Now(), s.openAIOAuthSchedulingRateMultiplier(ctx))
		rateOrder.enabled = true
	}
	orderItems := make([]accountWithLoad, len(eligible))
	for i, account := range eligible {
		orderItems[i] = accountWithLoad{account: account}
	}
	order := s.openAICandidateOrderForItems(groupID, rateOrder, orderItems)
	sort.SliceStable(eligible, func(i, j int) bool {
		a, b := eligible[i], eligible[j]
		if requireCompact && compactTiers[a.ID] != compactTiers[b.ID] {
			return compactTiers[a.ID] > compactTiers[b.ID]
		}
		return openAICandidateLess(accountWithLoad{account: a}, accountWithLoad{account: b}, order)
	})
	s.orderOpenAIPredictedAccounts(eligible, order, requestedModel, requireCompact)
	eligible = s.partitionOpenAILatencyHeldAccounts(ctx, eligible, requestedModel, requireCompact, groupID)
	return eligible[0], compactBlocked, filterStats
}

// SelectAccountWithLoadAwareness selects an account with load-awareness and wait plan.
func (s *OpenAIGatewayService) SelectAccountWithLoadAwareness(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*AccountSelectionResult, error) {
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, groupID)
	// 分组利润控制：legacy 公共入口同样装门，保证不经
	// selectAccountWithScheduler 的调用方也无法绕过利润准入。
	ctx = s.withOpenAIProfitControlGate(ctx, groupID)
	return s.selectAccountWithLoadAwareness(ctx, groupID, PlatformOpenAI, sessionHash, requestedModel, excludedIDs, false, "", true)
}

func (s *OpenAIGatewayService) selectAccountWithLoadAwareness(ctx context.Context, groupID *int64, platform string, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, requiredCapability OpenAIEndpointCapability, useUpstreamTokenCost bool) (selection *AccountSelectionResult, selectionErr error) {
	resilience.StateFrom(ctx).ResetStickyBypassReason()
	defer func() { selectionErr = capabilitySelectionError(ctx, selectionErr) }()
	if cache, atomic := s.cache.(OpenAIStickyAtomicCache); atomic && openAIAtomicStickySession(sessionHash) && NormalizeOpenAICompatiblePlatform(platform) == PlatformOpenAI && !s.qualityOwnsSticky(ctx, groupID, sessionHash) && ctx.Value(openAIStickyReconciledKey{}) == nil {
		initialOwner, _ := s.getStickySessionAccountID(ctx, groupID, sessionHash)
		defer func() {
			if selectionErr != nil || selection == nil || selection.Account == nil || initialOwner > 0 || gatewayProfitControlGateActive(ctx) {
				return
			}
			owner, err := cache.ClaimSessionAccountID(ctx, derefGroupID(groupID), s.openAISessionCacheKey(sessionHash), selection.Account.ID, 30*time.Second, false)
			if err != nil || owner <= 0 || owner == selection.Account.ID {
				return
			}
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			selection, selectionErr = s.selectAccountWithLoadAwareness(context.WithValue(ctx, openAIStickyReconciledKey{}, owner), groupID, platform, sessionHash, requestedModel, excludedIDs, requireCompact, requiredCapability, useUpstreamTokenCost)
		}()
	}
	platform = NormalizeOpenAICompatiblePlatform(platform)
	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		slog.Warn("channel pricing restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
	}

	cfg := s.schedulingConfig()
	preferLowUpstreamRate := useUpstreamTokenCost && s.isOpenAILowUpstreamRatePriorityEnabled(ctx, groupID)
	needsUpstreamCheck := s.needsUpstreamChannelRestrictionCheck(ctx, groupID)
	var stickyAccountID int64
	if sessionHash != "" && s.cache != nil {
		if accountID, err := s.getStickySessionAccountID(ctx, groupID, sessionHash); err == nil {
			stickyAccountID = accountID
		}
	}
	replacementSession := platform == PlatformOpenAI && sessionHash != "" &&
		(stickyAccountID > 0 || len(excludedIDs) > 0) &&
		!s.shareScheduling.shareSchedulingActive(derefGroupID(groupID))
	var migrated *AccountSelectionResult
	originalStickyAccountID := stickyAccountID
	stickyAccountID, migrated = s.migrateOpenAILatencyHeldOwner(ctx, groupID, platform, sessionHash, requestedModel, stickyAccountID, excludedIDs, requireCompact, requiredCapability, preferLowUpstreamRate)
	if migrated != nil {
		resilience.StateFrom(ctx).NoteStickyBypassReason("quality_migration")
		resilience.StateFrom(ctx).NoteStickyBypassBindingChange()
		return migrated, nil
	}
	if originalStickyAccountID > 0 && stickyAccountID != originalStickyAccountID {
		resilience.StateFrom(ctx).NoteStickyBypassReason("latency_migration")
	}
	if s.concurrencyService == nil || !cfg.LoadBatchEnabled {
		account, stickyHit, err := s.selectAccountForModelWithExclusionsStickyHit(ctx, groupID, platform, sessionHash, requestedModel, excludedIDs, requireCompact, stickyAccountID, requiredCapability, preferLowUpstreamRate)
		if err != nil {
			return nil, err
		}
		result, err := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if err == nil && result != nil && result.Acquired {
			selection, selectErr := s.newAcquiredSelectionResult(ctx, account, result.ReleaseFunc)
			return markStickySessionHit(selection, stickyHit), selectErr
		}
		if stickyHit && s.concurrencyService != nil {
			return s.selectOpenAIStickyCapacity(ctx, account, openAILegacyStickyCapacityRequest(ctx, OpenAIAccountScheduleRequest{GroupID: groupID, Platform: platform, SessionHash: sessionHash, RequestedModel: requestedModel, ExcludedIDs: excludedIDs, RequireCompact: requireCompact, RequiredCapability: requiredCapability, UseUpstreamTokenCost: useUpstreamTokenCost, SkipStickyCapacityRetry: err != nil}))
		}

		selection, selectErr := s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
			AccountID:      account.ID,
			MaxConcurrency: account.Concurrency,
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		})
		return markStickySessionHit(selection, stickyHit), selectErr
	}

	accounts, err := s.listSchedulableAccounts(ctx, groupID, platform)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, noAvailableOpenAISelectionError(requestedModel, false, openAISelectionFilterStats{}.summary(""))
	}

	isExcluded := func(accountID int64) bool {
		if excludedIDs == nil {
			return false
		}
		_, excluded := excludedIDs[accountID]
		return excluded
	}

	// ============ Layer 1: Sticky session ============
	if sessionHash != "" {
		accountID := stickyAccountID
		if accountID > 0 {
			account, err := s.getSchedulableAccount(ctx, accountID)
			softOwner := false
			if err != nil {
				resilience.StateFrom(ctx).NoteStickyBypassReason("owner_lookup_failed")
			} else if account == nil {
				resilience.StateFrom(ctx).NoteStickyBypassReason("owner_unavailable")
			}
			if err == nil && account != nil && isExcluded(accountID) {
				resilience.StateFrom(ctx).NoteStickyBypassReason("owner_excluded")
			}
			if err == nil && account != nil && !isExcluded(accountID) {
				softOwner = s.resilienceKeepOpenAISoftOwner(ctx, account, platform, requestedModel)
			}
			if softOwner {
				resilience.StateFrom(ctx).NoteStickyBypassReason("soft_owner_preserved")
			}
			if err == nil && account != nil && !isExcluded(accountID) && !softOwner {
				clearSticky := shouldClearStickySession(account, requestedModel)
				if clearSticky {
					resilience.StateFrom(ctx).NoteStickyBypassReason("owner_should_clear")
					_ = s.deleteStickySessionAccountIDIfOwner(ctx, groupID, sessionHash, accountID)
				}
				eligible := !clearSticky && isOpenAICompatibleAccountEligibleForRequest(ctx, account, platform, requestedModel, false, requiredCapability)
				if !clearSticky && !eligible {
					resilience.StateFrom(ctx).NoteStickyBypassReason("owner_ineligible")
				}
				if eligible {
					var transient bool
					account, transient = s.recheckOpenAIStickyOwnerFromDB(ctx, account, groupID, platform, requestedModel, requireCompact, requiredCapability)
					if account == nil {
						if !transient {
							resilience.StateFrom(ctx).NoteStickyBypassReason("owner_group_mismatch")
							_ = s.deleteStickySessionAccountIDIfOwner(ctx, groupID, sessionHash, accountID)
						} else {
							resilience.StateFrom(ctx).NoteStickyBypassReason("owner_recheck_transient")
						}
					} else if !s.openAIAccountMatchesSchedulingGroup(account, groupID) {
						resilience.StateFrom(ctx).NoteStickyBypassReason("owner_group_mismatch")
						_ = s.deleteStickySessionAccountIDIfOwner(ctx, groupID, sessionHash, accountID)
					} else if s.isOpenAIAccountRequestRuntimeBlocked(account, requestedModel, requireCompact) {
						resilience.StateFrom(ctx).NoteStickyBypassReason("runtime_blocked")
						_ = s.deleteStickySessionAccountIDIfOwner(ctx, groupID, sessionHash, accountID)
					} else if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, account, requestedModel, requireCompact) {
						resilience.StateFrom(ctx).NoteStickyBypassReason("channel_upstream_restricted")
						_ = s.deleteStickySessionAccountIDIfOwner(ctx, groupID, sessionHash, accountID)
					} else if !parentHealthyForShadow(account, s.parentAccountLookup(ctx)) {
						resilience.StateFrom(ctx).NoteStickyBypassReason("shadow_parent_unhealthy")
						_ = s.deleteStickySessionAccountIDIfOwner(ctx, groupID, sessionHash, accountID)
					} else {
						result, err := s.tryAcquireAccountSlot(ctx, accountID, account.Concurrency)
						if err == nil && result != nil && result.Acquired {
							selection, selectErr := s.newAcquiredSelectionResult(ctx, account, result.ReleaseFunc)
							if selectErr != nil {
								return nil, selectErr
							}
							_ = s.refreshStickySessionTTL(ctx, groupID, sessionHash, s.openAIStickyBindingTTL(groupID), stickyAccountID)
							return markStickySessionHit(selection, true), nil
						}

						selection, capacityErr := s.selectOpenAIStickyCapacity(ctx, account, openAILegacyStickyCapacityRequest(ctx, OpenAIAccountScheduleRequest{GroupID: groupID, Platform: platform, SessionHash: sessionHash, RequestedModel: requestedModel, ExcludedIDs: excludedIDs, RequireCompact: requireCompact, RequiredCapability: requiredCapability, UseUpstreamTokenCost: useUpstreamTokenCost, SkipStickyCapacityRetry: err != nil}))
						if selection != nil && selection.StickyCapacitySpillover {
							resilience.StateFrom(ctx).NoteStickyBypassReason("capacity_overflow")
						} else {
							resilience.StateFrom(ctx).NoteStickyBypassReason("concurrency_full")
						}
						return selection, capacityErr
					}
				}
			}
		}
	}

	// ============ Layer 2: Load-aware selection ============
	// Per-pass parent-health cache to avoid repeated DB calls when multiple shadow
	// accounts share the same parent.
	parentCacheL2 := make(map[int64]*Account)
	parentLookupL2 := func(id int64) *Account {
		if a, ok := parentCacheL2[id]; ok {
			return a
		}
		if s.accountRepo == nil {
			return nil
		}
		a, _ := s.accountRepo.GetByID(ctx, id)
		parentCacheL2[id] = a
		return a
	}
	baseCandidateCount := 0
	filterStats := openAISelectionFilterStats{pool: len(accounts)}
	candidates := make([]*Account, 0, len(accounts))
	for i := range accounts {
		acc := &accounts[i]
		if isExcluded(acc.ID) {
			filterStats.exclude("excluded")
			continue
		}
		// Scheduler snapshots can be temporarily stale (bucket rebuild is throttled);
		// re-check schedulability here so recently rate-limited/overloaded accounts
		// are not selected again before the bucket is rebuilt.
		if reason := openAICompatibleAccountEligibilityFailureReason(ctx, acc, platform, requestedModel, false, requiredCapability); reason != "" {
			filterStats.exclude(reason)
			continue
		}
		if !parentHealthyForShadow(acc, parentLookupL2) {
			filterStats.exclude("shadow_parent_unhealthy")
			continue
		}
		if s.isOpenAIAccountRequestRuntimeBlocked(acc, requestedModel, requireCompact) {
			filterStats.exclude("runtime_blocked")
			continue
		}
		if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, acc, requestedModel, requireCompact) {
			filterStats.exclude("channel_upstream_restricted")
			continue
		}
		baseCandidateCount++
		candidates = append(candidates, acc)
	}

	if len(candidates) == 0 {
		return nil, noAvailableOpenAISelectionError(requestedModel, false, filterStats.summary(""), filterStats.capabilityOnly())
	}
	rateOrder := openAILegacyUpstreamRateOrder{}
	if preferLowUpstreamRate {
		rateOrder = newOpenAILegacyUpstreamRateOrder(candidates, time.Now(), s.openAIOAuthSchedulingRateMultiplier(ctx))
		rateOrder.enabled = true
	}

	accountLoads := make([]AccountWithConcurrency, 0, len(candidates))
	for _, acc := range candidates {
		accountLoads = append(accountLoads, AccountWithConcurrency{
			ID:             acc.ID,
			MaxConcurrency: acc.EffectiveLoadFactor(),
		})
	}

	tryAcquireFromLoadMap := func(loadMap map[int64]*AccountLoadInfo) (*AccountSelectionResult, bool, error) {
		var available []accountWithLoad
		for _, acc := range candidates {
			loadInfo := loadMap[acc.ID]
			if loadInfo == nil {
				loadInfo = &AccountLoadInfo{AccountID: acc.ID}
			}
			if loadInfo.LoadRate < 100 {
				available = append(available, accountWithLoad{
					account:  acc,
					loadInfo: loadInfo,
				})
			}
		}

		if len(available) == 0 {
			return nil, false, nil
		}

		availableItems := append([]accountWithLoad(nil), available...)
		order := s.openAICandidateOrderForItems(groupID, rateOrder, availableItems)
		sort.SliceStable(available, func(i, j int) bool { return openAICandidateLess(available[i], available[j], order) })
		if !rateOrder.enabled {
			shuffleOpenAIWithinQualityGroups(available, order.tiers, s.openAIAccountLatencyHoldSnapshotForAccounts(candidates, requestedModel))
		}
		if replacementSession {
			if rateOrder.enabled {
				s.preferOpenAIReplacementSessionWithTiers(ctx, available, groupID, sessionHash, requestedModel, rateOrder, requireCompact, requiredCapability, order.tiers)
			} else {
				s.preferOpenAIReplacementSession(ctx, available, groupID, sessionHash, requestedModel, rateOrder, requireCompact, requiredCapability)
			}
		} else if !rateOrder.enabled && stickyAccountID == 0 {
			s.preferOpenAIQualityCacheCandidates(ctx, available, requestedModel, order.tiers)
		}

		s.orderOpenAIPredictedLoad(available, order, requestedModel, requireCompact)
		selectionOrder := make([]accountWithLoad, 0, len(available))
		if requireCompact {
			appendTier := func(out []accountWithLoad, tier int) []accountWithLoad {
				for _, item := range available {
					if openAICompactSupportTier(item.account) == tier {
						out = append(out, item)
					}
				}
				return out
			}
			selectionOrder = appendTier(selectionOrder, 2)
			selectionOrder = appendTier(selectionOrder, 1)
			// tier 0 候选作为兜底追加：DB recheck 时若发现 cache tier 0 实际
			// 已升级为 1/2（探测刚跑完，cache 尚未刷新），仍可正常命中。
			selectionOrder = appendTier(selectionOrder, 0)
		} else {
			selectionOrder = append(selectionOrder, available...)
		}
		selectionOrder = s.partitionOpenAILatencyHeldLoad(ctx, selectionOrder, requestedModel, requireCompact, groupID)

		for _, item := range selectionOrder {
			fresh := s.resolveFreshSchedulableOpenAIAccount(ctx, item.account, platform, requestedModel, false, requiredCapability)
			if fresh == nil {
				continue
			}
			fresh = s.recheckSelectedOpenAIAccountFromDB(ctx, fresh, groupID, platform, requestedModel, requireCompact, requiredCapability)
			if fresh == nil {
				continue
			}
			if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, fresh, requestedModel, requireCompact) {
				continue
			}
			result, err := s.tryAcquireAccountSlot(ctx, fresh.ID, fresh.Concurrency)
			if err == nil && result != nil && result.Acquired {
				selection, selectErr := s.newAcquiredSelectionResult(ctx, fresh, result.ReleaseFunc)
				if selectErr != nil {
					return nil, true, selectErr
				}
				if sessionHash != "" && !gatewayProfitControlGateActive(ctx) {
					_ = s.setStickySessionAccountID(ctx, groupID, sessionHash, fresh.ID, openaiStickySessionTTL)
				}
				return selection, true, nil
			}
		}
		return nil, true, nil
	}

	loadMap, err := s.concurrencyService.GetAccountsLoadBatch(ctx, accountLoads)
	if err != nil {
		ordered := append([]*Account(nil), candidates...)
		s.sortOpenAIAccounts(ordered, groupID, rateOrder, requestedModel, requireCompact)
		if requireCompact {
			ordered = prioritizeOpenAICompactAccounts(ordered)
		}
		ordered = s.partitionOpenAILatencyHeldAccounts(ctx, ordered, requestedModel, requireCompact, groupID)
		for _, acc := range ordered {
			fresh := s.resolveFreshSchedulableOpenAIAccount(ctx, acc, platform, requestedModel, false, requiredCapability)
			if fresh == nil {
				continue
			}
			fresh = s.recheckSelectedOpenAIAccountFromDB(ctx, fresh, groupID, platform, requestedModel, requireCompact, requiredCapability)
			if fresh == nil {
				continue
			}
			if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, fresh, requestedModel, requireCompact) {
				continue
			}
			result, err := s.tryAcquireAccountSlot(ctx, fresh.ID, fresh.Concurrency)
			if err == nil && result != nil && result.Acquired {
				selection, selectErr := s.newAcquiredSelectionResult(ctx, fresh, result.ReleaseFunc)
				if selectErr != nil {
					return nil, selectErr
				}
				if sessionHash != "" && !gatewayProfitControlGateActive(ctx) {
					_ = s.setStickySessionAccountID(ctx, groupID, sessionHash, fresh.ID, openaiStickySessionTTL)
				}
				return selection, nil
			}
		}
	} else {
		if selection, attempted, selectErr := tryAcquireFromLoadMap(loadMap); selectErr != nil {
			return nil, selectErr
		} else if selection != nil {
			return selection, nil
		} else if attempted {
			if freshLoadMap, loadErr := s.concurrencyService.GetAccountsLoadBatchFresh(ctx, accountLoads); loadErr == nil {
				if selection, _, selectErr := tryAcquireFromLoadMap(freshLoadMap); selectErr != nil {
					return nil, selectErr
				} else if selection != nil {
					return selection, nil
				}
			}
		}
		if selection, selectErr := openAIMonitorProbeFreeSlot(ctx, tryAcquireFromLoadMap); selectErr != nil || selection != nil {
			return selection, selectErr
		}
	}

	// ============ Layer 3: Fallback wait ============
	s.sortOpenAIAccounts(candidates, groupID, rateOrder, requestedModel, requireCompact)
	if requireCompact {
		candidates = prioritizeOpenAICompactAccounts(candidates)
	}
	candidates = s.partitionOpenAILatencyHeldAccounts(ctx, candidates, requestedModel, requireCompact, groupID)
	for _, acc := range candidates {
		fresh := s.resolveFreshSchedulableOpenAIAccount(ctx, acc, platform, requestedModel, false, requiredCapability)
		if fresh == nil {
			continue
		}
		fresh = s.recheckSelectedOpenAIAccountFromDB(ctx, fresh, groupID, platform, requestedModel, requireCompact, requiredCapability)
		if fresh == nil {
			continue
		}
		if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, fresh, requestedModel, requireCompact) {
			continue
		}
		selection, err := s.newSelectionResult(ctx, fresh, false, nil, &AccountWaitPlan{
			AccountID:      fresh.ID,
			MaxConcurrency: fresh.Concurrency,
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		})
		return selection, err
	}

	if requireCompact && baseCandidateCount > 0 {
		return nil, ErrNoAvailableCompactAccounts
	}
	return nil, ErrNoAvailableAccounts
}

func (s *OpenAIGatewayService) preferOpenAIReplacementSessionWithTiers(ctx context.Context, available []accountWithLoad, groupID *int64, session, model string, rateOrder openAILegacyUpstreamRateOrder, compact bool, capability OpenAIEndpointCapability, qualityTiers map[int64]int) {
	if qualityTiers == nil && s.GroupQualityGateView().Enabled(derefGroupID(groupID)) {
		qualityTiers = make(map[int64]int, len(available))
		view := s.GroupQualityGateView()
		for _, item := range available {
			qualityTiers[item.account.ID] = view.OrderTier(derefGroupID(groupID), item.account.ID)
		}
	}
	s.preferOpenAISessionOrder(ctx, available, groupID, session, model, rateOrder, compact, capability, false, qualityTiers)
}

// preferOpenAIReplacementSession handles the rate-neutral path, which has no
// quality tier table to reuse.
func (s *OpenAIGatewayService) preferOpenAIReplacementSession(ctx context.Context, available []accountWithLoad, groupID *int64, session, model string, rateOrder openAILegacyUpstreamRateOrder, compact bool, capability OpenAIEndpointCapability) {
	s.preferOpenAIReplacementSessionWithTiers(ctx, available, groupID, session, model, rateOrder, compact, capability, nil)
}

// preferOpenAISessionOrder extracts the session-stable order for borrowing.
// Existing E2 cache evidence is retained when low-rate priority is off.
func (s *OpenAIGatewayService) preferOpenAISessionOrder(ctx context.Context, available []accountWithLoad, groupID *int64, session, model string, rateOrder openAILegacyUpstreamRateOrder, compact bool, capability OpenAIEndpointCapability, borrowing bool, qualityTiers map[int64]int) {
	protocol, _, _ := resilience.CapabilityRequestFrom(ctx)
	accounts := make([]*Account, 0, len(available))
	for _, item := range available {
		accounts = append(accounts, item.account)
	}
	held := map[int64]openAIAccountLatencyHoldSnapshot(nil)
	if qualityTiers != nil {
		held = s.openAIAccountLatencyHoldSnapshotForAccounts(accounts, model)
	}
	type cohortKey struct {
		priority     int
		rate         float64
		known        bool
		tier         int
		qualityTier  int
		nearCapacity bool
		held         bool
		predicted    int
	}
	keyOf := func(item accountWithLoad) cohortKey {
		key := cohortKey{priority: item.account.Priority, qualityTier: qualityTiers[item.account.ID], held: held[item.account.ID].Held}
		if rateOrder.enabled {
			key.rate, key.known = rateOrder.rates[item.account.ID]
			if ms, ok := s.GroupQualityGateView().ExpectedP90Ms(derefGroupID(groupID), item.account.ID); qualityTiers != nil && ok {
				key.predicted = ms
			}
			key.nearCapacity = openAIAccountNearCapacity(item)
		}
		if compact {
			key.tier = openAICompactSupportTier(item.account)
		}
		return key
	}
	loadKeyOf := func(item accountWithLoad) cohortKey {
		if !borrowing && !rateOrder.enabled {
			key := keyOf(item)
			key.tier = 0
			return key
		}
		return keyOf(item)
	}
	minLoad := make(map[cohortKey]int)
	for _, item := range available {
		key := loadKeyOf(item)
		if old, ok := minLoad[key]; !ok || item.loadInfo.LoadRate < old {
			minLoad[key] = item.loadInfo.LoadRate
		}
	}
	cohorts := make(map[cohortKey][]int)
	for i, item := range available {
		key := keyOf(item)
		if borrowing || item.loadInfo.LoadRate <= minLoad[loadKeyOf(item)]+20 {
			cohorts[key] = append(cohorts[key], i)
		}
	}
	for _, indices := range cohorts {
		if len(indices) < 2 {
			continue
		}
		items := make([]accountWithLoad, len(indices))
		complete := !borrowing
		scores := make(map[int64]float64, len(indices))
		hashes := make(map[int64]uint64, len(indices))
		recent := make(map[int64]time.Time, len(indices))
		now := time.Now()
		for i, index := range indices {
			item := available[index]
			items[i] = item
			if !borrowing {
				value, known := s.cachePreference(ctx, item.account, model)
				complete = complete && known
				scores[item.account.ID] = value
			}
			hashes[item.account.ID] = openAIReplacementSessionHash(derefGroupID(groupID), session, item.account.ID)
			outboundModel := openAIReplacementSessionModel(item.account, model, compact, capability, protocol)
			recent[item.account.ID] = s.recentSessionSuccess().lastSuccess(openAIRecentSessionKey{derefGroupID(groupID), session, outboundModel}, item.account.ID, now)
		}
		sort.SliceStable(items, func(i, j int) bool {
			a, b := items[i].account.ID, items[j].account.ID
			if !recent[a].Equal(recent[b]) {
				return recent[a].After(recent[b])
			}
			if complete && scores[a] != scores[b] {
				return scores[a] < scores[b]
			}
			if hashes[a] != hashes[b] {
				return hashes[a] > hashes[b]
			}
			return a < b
		})
		for i, index := range indices {
			available[index] = items[i]
		}
	}
}

func openAIReplacementSessionModel(account *Account, model string, compact bool, capability OpenAIEndpointCapability, protocol string) string {
	// Text Responses also accepts Chat capability, but keeps Responses model rules.
	if capability == OpenAIEndpointCapabilityChatCompletions && protocol != resilience.CapabilityProtocolResponses && !compact {
		return normalizeOpenAIModelForUpstream(account, resolveOpenAIForwardModel(account, model, ""))
	}
	return resolveOpenAIAccountUpstreamModelForRequest(account, model, compact)
}

// Same fixed xxhash inputs as sharesched.hrwUniform: group salt, session key,
// then account ID. Maximizing the hash is the equal-weight HRW order.
func openAIReplacementSessionHash(groupID int64, session string, accountID int64) uint64 {
	var digest xxhash.Digest
	digest.Reset()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(groupID))
	_, _ = digest.Write(buf[:])
	_, _ = digest.WriteString(session)
	binary.LittleEndian.PutUint64(buf[:], uint64(accountID))
	_, _ = digest.Write(buf[:])
	return digest.Sum64() >> 11
}

func (s *OpenAIGatewayService) listSchedulableAccounts(ctx context.Context, groupID *int64, platform string) ([]Account, error) {
	platform = NormalizeOpenAICompatiblePlatform(platform)
	if s.schedulerSnapshot != nil {
		accounts, _, err := s.schedulerSnapshot.ListSchedulableAccounts(ctx, groupID, platform, false)
		if err != nil {
			return accounts, err
		}
		accounts = s.filterOpenAIAccountsBySchedulingThreshold(ctx, accounts)
		if platform == PlatformGrok {
			accounts = s.filterGrokFreeQuotaAccountsForOpenAI(ctx, accounts)
		}
		return accounts, nil
	}
	var accounts []Account
	var err error
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		accounts, err = s.accountRepo.ListSchedulableByPlatform(ctx, platform)
	} else if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, *groupID, platform)
	} else {
		accounts, err = s.accountRepo.ListSchedulableUngroupedByPlatform(ctx, platform)
	}
	if err != nil {
		return nil, fmt.Errorf("query accounts failed: %w", err)
	}
	accounts = s.filterOpenAIAccountsBySchedulingThreshold(ctx, accounts)
	if platform == PlatformGrok {
		accounts = s.filterGrokFreeQuotaAccountsForOpenAI(ctx, accounts)
	}
	return accounts, nil
}

func (s *OpenAIGatewayService) tryAcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int) (*AcquireResult, error) {
	if s.concurrencyService == nil {
		return &AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
	}
	return s.concurrencyService.AcquireAccountSlot(ctx, accountID, maxConcurrency)
}

func (s *OpenAIGatewayService) resolveFreshSchedulableOpenAIAccount(ctx context.Context, account *Account, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) *Account {
	fresh := s.resolveFreshSchedulableOpenAIAccountBeforeProfit(ctx, account, platform, requestedModel, requireCompact, requiredCapability)
	if fresh == nil {
		return nil
	}
	if vetoed, _ := openAIProfitControlVetoReason(ctx, fresh); vetoed {
		return nil
	}
	return fresh
}

func (s *OpenAIGatewayService) resolveFreshSchedulableOpenAIAccountBeforeProfit(ctx context.Context, account *Account, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) *Account {
	if account == nil {
		return nil
	}
	platform = NormalizeOpenAICompatiblePlatform(platform)

	fresh := account
	if s.schedulerSnapshot != nil {
		current, err := s.getSchedulableAccount(ctx, account.ID)
		if err != nil || current == nil {
			return nil
		}
		fresh = current
	}

	if !isOpenAICompatibleAccountEligibleForRequestBeforeProfit(ctx, fresh, platform, requestedModel, requireCompact, requiredCapability, s) {
		return nil
	}
	if !parentHealthyForShadow(fresh, s.parentAccountLookup(ctx)) {
		return nil
	}
	if s.isOpenAIAccountRequestRuntimeBlocked(fresh, requestedModel, requireCompact) {
		return nil
	}
	if s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, fresh) {
		return nil
	}
	if s.isOpenAIProxyStreamQuarantined(ctx, fresh) {
		return nil
	}
	return fresh
}

// parentAccountLookup 返回供 parentHealthyForShadow 使用的母账号解析闭包:经 accountRepo
// 按 ID 取当前 Account(repo 为空时 fail-closed 返回 nil)。统一调度/粘连各路径的母账号解析,
// 取代各调用点重复内联的同一闭包(历史上 recheck 等路径还漏写过 accountRepo==nil 守卫)。
// L2 候选循环改用带 per-pass 缓存的 parentLookupL2,不走此方法。
func (s *OpenAIGatewayService) parentAccountLookup(ctx context.Context) func(int64) *Account {
	return func(id int64) *Account {
		if s.accountRepo == nil {
			return nil
		}
		a, _ := s.accountRepo.GetByID(ctx, id)
		return a
	}
}

func (s *OpenAIGatewayService) recheckSelectedOpenAIAccountFromDB(ctx context.Context, account *Account, groupID *int64, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) *Account {
	latest := s.recheckSelectedOpenAIAccountFromDBBeforeProfit(ctx, account, groupID, platform, requestedModel, requireCompact, requiredCapability)
	if latest == nil {
		return nil
	}
	if vetoed, _ := openAIProfitControlVetoReason(ctx, latest); vetoed {
		return nil
	}
	return latest
}

func (s *OpenAIGatewayService) recheckSelectedOpenAIAccountFromDBBeforeProfit(ctx context.Context, account *Account, groupID *int64, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) *Account {
	if account == nil {
		return nil
	}
	platform = NormalizeOpenAICompatiblePlatform(platform)
	if s.schedulerSnapshot == nil || s.accountRepo == nil {
		if s.openAIGroupRequiresPrivacySet(ctx, groupID) && !account.IsPrivacySet() {
			return nil
		}
		if !isOpenAICompatibleAccountEligibleForRequestBeforeProfit(ctx, account, platform, requestedModel, requireCompact, requiredCapability, s) {
			return nil
		}
		if s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, account) {
			return nil
		}
		if !parentHealthyForShadow(account, s.parentAccountLookup(ctx)) {
			return nil
		}
		if s.isOpenAIProxyStreamQuarantined(ctx, account) {
			return nil
		}
		return account
	}

	latest, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || latest == nil {
		return nil
	}
	return s.recheckLatestOpenAIAccount(ctx, latest, groupID, platform, requestedModel, requireCompact, requiredCapability)
}

// recheckLatestOpenAIAccount applies the DB recheck to an account just read.
func (s *OpenAIGatewayService) recheckLatestOpenAIAccount(ctx context.Context, latest *Account, groupID *int64, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) *Account {
	if !s.openAIAccountMatchesSchedulingGroup(latest, groupID) {
		return nil
	}
	if s.openAIGroupRequiresPrivacySet(ctx, groupID) && !latest.IsPrivacySet() {
		return nil
	}
	if !isOpenAICompatibleAccountEligibleForRequestBeforeProfit(ctx, latest, platform, requestedModel, requireCompact, requiredCapability, s) {
		return nil
	}
	if !parentHealthyForShadow(latest, s.parentAccountLookup(ctx)) {
		return nil
	}
	if s.isOpenAIAccountRequestRuntimeBlocked(latest, requestedModel, requireCompact) {
		return nil
	}
	if s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, latest) {
		return nil
	}
	if s.isOpenAIProxyStreamQuarantined(ctx, latest) {
		return nil
	}
	return latest
}

func (s *OpenAIGatewayService) openAIAccountMatchesSchedulingGroup(account *Account, groupID *int64) bool {
	if s != nil && s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return account != nil
	}
	return openAIStickyAccountMatchesGroup(account, groupID)
}

func (s *OpenAIGatewayService) getSchedulableAccount(ctx context.Context, accountID int64) (*Account, error) {
	var (
		account *Account
		err     error
	)
	if s.schedulerSnapshot != nil {
		account, err = s.schedulerSnapshot.GetAccount(ctx, accountID)
	} else {
		account, err = s.accountRepo.GetByID(ctx, accountID)
	}
	if err != nil || account == nil {
		return account, err
	}
	state := resilience.StateFrom(ctx)
	if account.ID == state.StickyOwner() {
		if _, _, reason := resiliencePenaltyOf(account, "", time.Now()); reason == "balance_exhausted" {
			state.NoteStickyBypassReason(reason)
		}
	}
	if s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, account) {
		return nil, nil
	}
	// Legacy sticky (advanced scheduler off) must still free-gate Grok OAuth.
	if account.IsGrok() {
		if gated := s.filterGrokFreeQuotaAccountsForOpenAI(ctx, []Account{*account}); len(gated) == 0 {
			return nil, nil
		}
	}
	return account, nil
}

// filterGrokFreeQuotaAccountsForOpenAI applies the same local free soft-gate as
// GatewayService / advanced scheduler, for OpenAI-compatible legacy selection.
func (s *OpenAIGatewayService) filterGrokFreeQuotaAccountsForOpenAI(ctx context.Context, accounts []Account) []Account {
	if s == nil {
		return accounts
	}
	return filterGrokFreeQuotaAccountsCore(ctx, s.cfg, s.usageLogRepo, &openaiGrokFreeQuotaGateCache, accounts)
}

func (s *OpenAIGatewayService) filterOpenAIAccountsBySchedulingThreshold(ctx context.Context, accounts []Account) []Account {
	if len(accounts) == 0 {
		return accounts
	}

	filtered := make([]Account, 0, len(accounts))
	for i := range accounts {
		if s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, &accounts[i]) {
			continue
		}
		filtered = append(filtered, accounts[i])
	}
	return filtered
}

func (s *OpenAIGatewayService) isOpenAIAccountBlockedBySchedulingThreshold(ctx context.Context, account *Account) bool {
	if s == nil || s.rateLimitService == nil || account == nil {
		return false
	}
	return s.rateLimitService.ApplyAccountSchedulingThreshold(ctx, account)
}

func (s *OpenAIGatewayService) hydrateSelectedAccount(ctx context.Context, account *Account) (*Account, error) {
	if account == nil || s.schedulerSnapshot == nil {
		return account, nil
	}
	hydrated, err := s.schedulerSnapshot.GetAccount(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	if hydrated == nil {
		return nil, fmt.Errorf("selected openai account %d not found during hydration", account.ID)
	}
	return hydrated, nil
}

func (s *OpenAIGatewayService) newSelectionResult(ctx context.Context, account *Account, acquired bool, release func(), waitPlan *AccountWaitPlan) (*AccountSelectionResult, error) {
	hydrated, err := s.hydrateSelectedAccount(ctx, account)
	if err != nil {
		return nil, err
	}
	return attachSelectionProfitGate(ctx, &AccountSelectionResult{
		Account:     hydrated,
		Acquired:    acquired,
		ReleaseFunc: release,
		WaitPlan:    waitPlan,
	}), nil
}

func (s *OpenAIGatewayService) newAcquiredSelectionResult(ctx context.Context, account *Account, release func()) (*AccountSelectionResult, error) {
	selection, err := s.newSelectionResult(ctx, account, true, release, nil)
	if err != nil && release != nil {
		release()
	}
	return selection, err
}

// markStickySessionHit 在选号结果上记录账号是否来自会话粘性命中。
func markStickySessionHit(selection *AccountSelectionResult, hit bool) *AccountSelectionResult {
	if selection != nil && hit {
		selection.stickySessionHit = true
	}
	return selection
}

func (s *OpenAIGatewayService) schedulingConfig() config.GatewaySchedulingConfig {
	if s.cfg != nil {
		return s.cfg.Gateway.Scheduling
	}
	return config.GatewaySchedulingConfig{
		StickySessionMaxWaiting:  3,
		StickySessionWaitTimeout: 45 * time.Second,
		FallbackWaitTimeout:      30 * time.Second,
		FallbackMaxWaiting:       100,
		LoadBatchEnabled:         true,
		SlotCleanupInterval:      30 * time.Second,
	}
}

// openAIGroupQualityGate carries the effective scheduling group, independently
// of whether its profit gate is enabled, plus continuation/migration guards.
type openAIGroupQualityGate struct {
	service *OpenAIGatewayService
	groupID int64
	exempt  bool
}
type groupQualityGateContextKey struct{}
type groupQualityGateExemptContextKey struct{}

func (s *OpenAIGatewayService) withGroupQualityGate(ctx context.Context, groupID *int64) context.Context {
	if _, suppressed := ctx.Value(openAIProfitControlSuppressCtxKey{}).(struct{}); suppressed {
		return ctx
	}
	var gate *openAIGroupQualityGate
	if s != nil && groupID != nil && *groupID > 0 {
		gate = &openAIGroupQualityGate{service: s, groupID: *groupID}
	}
	return context.WithValue(ctx, groupQualityGateContextKey{}, gate)
}
func withGroupQualityGateExempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, groupQualityGateExemptContextKey{}, true)
}
func groupQualityGateExempt(ctx context.Context) bool {
	exempt, _ := ctx.Value(groupQualityGateExemptContextKey{}).(bool)
	return exempt
}
func attachSelectionGroupQualityGate(ctx context.Context, selection *AccountSelectionResult) {
	gate, _ := ctx.Value(groupQualityGateContextKey{}).(*openAIGroupQualityGate)
	if selection == nil || gate == nil {
		return
	}
	copy := *gate
	copy.exempt = groupQualityGateExempt(ctx)
	selection.groupQualityGate = &copy
}
func contextWithSelectionGroupQualityGate(ctx context.Context, selection *AccountSelectionResult) context.Context {
	if selection == nil || selection.groupQualityGate == nil {
		return ctx
	}
	return context.WithValue(ctx, groupQualityGateContextKey{}, selection.groupQualityGate)
}

// SelectionAdmissionVetoLatest shares the profit-gate refresh and checks the
// current account after acquiring a slot, including after a wait plan.
// Quality is an ordering signal and never vetoes an already selected account.
func (s *OpenAIGatewayService) SelectionAdmissionVetoLatest(ctx context.Context, account *Account) (*Account, bool, string) {
	latest, vetoed, reason := s.ProfitControlVetoLatest(ctx, account)
	if vetoed {
		return latest, vetoed, reason
	}
	return latest, false, ""
}

// OpenAIQualityTurnPinned uses the same chain ownership predicate as
// AdvanceOpenAIQualityTurn, before that method can update routing state.
func OpenAIQualityTurnPinned(payload []byte) bool {
	return gjson.GetBytes(payload, "previous_response_id").String() != "" || qualityIncompleteTools(payload)
}

// WithOpenAIQualityTurnExemption scopes the continuation exemption to one turn.
func WithOpenAIQualityTurnExemption(ctx context.Context, pinned bool) context.Context {
	gate, _ := ctx.Value(groupQualityGateContextKey{}).(*openAIGroupQualityGate)
	if gate != nil {
		copy := *gate
		copy.exempt = false
		ctx = context.WithValue(ctx, groupQualityGateContextKey{}, &copy)
	}
	return context.WithValue(ctx, groupQualityGateExemptContextKey{}, pinned)
}

type openAICandidateOrder struct {
	rates       openAILegacyUpstreamRateOrder
	groupID     int64
	tiers       map[int64]int
	expectedP90 map[int64]int
}

var (
	openAIStickyCapacityRetryDuration = 2 * time.Second
	openAIStickyCapacityRetryInitial  = 50 * time.Millisecond
)

func openAIAccountNearCapacity(item accountWithLoad) bool {
	a := item.account
	return item.loadInfo != nil && item.loadInfo.LoadRate >= 80 && (a.Concurrency > 0 || a.LoadFactor != nil && *a.LoadFactor > 0)
}

// openAICandidateLess is the sole legacy comparator. Without low-rate priority
// it keeps the previous priority/load/LRU order and its stable ties.
func openAICandidateLess(a, b accountWithLoad, order openAICandidateOrder) bool {
	if order.tiers != nil {
		at, bt := order.tiers[a.account.ID], order.tiers[b.account.ID]
		if at != bt {
			return at < bt
		}
	}
	if a.account.Priority != b.account.Priority {
		return a.account.Priority < b.account.Priority
	}
	if order.rates.enabled {
		an, bn := openAIAccountNearCapacity(a), openAIAccountNearCapacity(b)
		if an != bn {
			return !an
		}
		if cmp := order.rates.compare(a.account, b.account); cmp != 0 {
			return cmp < 0
		}
	}
	if a.loadInfo != nil && b.loadInfo != nil && a.loadInfo.LoadRate != b.loadInfo.LoadRate {
		return a.loadInfo.LoadRate < b.loadInfo.LoadRate
	}
	switch {
	case a.account.LastUsedAt == nil && b.account.LastUsedAt != nil:
		return true
	case a.account.LastUsedAt != nil && b.account.LastUsedAt == nil:
		return false
	case a.account.LastUsedAt != nil && b.account.LastUsedAt != nil:
		if !a.account.LastUsedAt.Equal(*b.account.LastUsedAt) {
			return a.account.LastUsedAt.Before(*b.account.LastUsedAt)
		}
	}
	return order.rates.enabled && a.account.ID < b.account.ID
}
func (s *OpenAIGatewayService) openAICandidateOrder(groupID *int64, rates openAILegacyUpstreamRateOrder) openAICandidateOrder {
	return openAICandidateOrder{rates: rates, groupID: derefGroupID(groupID)}
}
func (s *OpenAIGatewayService) openAICandidateOrderForItems(groupID *int64, rates openAILegacyUpstreamRateOrder, items []accountWithLoad) openAICandidateOrder {
	order := s.openAICandidateOrder(groupID, rates)
	view := s.GroupQualityGateView()
	if view.Enabled(order.groupID) {
		order.tiers = make(map[int64]int, len(items))
		order.expectedP90 = make(map[int64]int, len(items))
		for _, item := range items {
			order.tiers[item.account.ID] = view.OrderTier(order.groupID, item.account.ID)
			if ms, ok := view.ExpectedP90Ms(order.groupID, item.account.ID); ok {
				order.expectedP90[item.account.ID] = ms
			}
		}
	}
	return order
}
func (s *OpenAIGatewayService) sortOpenAIAccounts(accounts []*Account, groupID *int64, rates openAILegacyUpstreamRateOrder, requestedModel string, compact bool) {
	items := make([]accountWithLoad, len(accounts))
	for i, account := range accounts {
		items[i] = accountWithLoad{account: account}
	}
	order := s.openAICandidateOrderForItems(groupID, rates, items)
	sort.SliceStable(accounts, func(i, j int) bool {
		return openAICandidateLess(accountWithLoad{account: accounts[i]}, accountWithLoad{account: accounts[j]}, order)
	})
	if !rates.enabled {
		var held map[int64]openAIAccountLatencyHoldSnapshot
		if order.tiers != nil {
			held = s.openAIAccountLatencyHoldSnapshotForAccounts(accounts, requestedModel)
		}
		for start := 0; start < len(accounts); {
			end := start + 1
			for end < len(accounts) && order.tiers[accounts[end].ID] == order.tiers[accounts[start].ID] && held[accounts[end].ID].Held == held[accounts[start].ID].Held {
				end++
			}
			shuffleWithinPriorityAndLastUsed(accounts[start:end], false)
			start = end
		}
	}
	s.orderOpenAIPredictedAccounts(accounts, order, requestedModel, compact)
}

// selectOpenAIStickyCapacity is shared by all three sticky saturation paths.
// The short retry never joins a queue; only the returned final wait plan does.
func (s *OpenAIGatewayService) selectOpenAIStickyCapacity(ctx context.Context, account *Account, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, error) {
	cfg := s.schedulingConfig()
	wait := func() (*AccountSelectionResult, error) {
		if s.concurrencyService != nil {
			waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, account.ID)
			if waitingCount >= cfg.StickySessionMaxWaiting {
				if (cfg.LoadBatchEnabled || req.AdvancedScheduler) && !groupQualityGateExempt(ctx) && !req.DisableStickyEscape && req.PreviousResponseID == "" {
					// Non-OpenAI legacy selection reaches Layer 2 only after the
					// sticky queue fills; try its immediate slots before Layer 3.
					if NormalizeOpenAICompatiblePlatform(req.Platform) != PlatformOpenAI {
						selection, err := s.selectOpenAIStickyCapacityBorrow(ctx, account, req)
						if err != nil || selection != nil {
							return selection, err
						}
					}
					selection, err := s.selectOpenAIStickyCapacityFallbackWait(ctx, account, req)
					if err != nil || selection != nil {
						return selection, err
					}
				}
				selection, err := s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
					AccountID: account.ID, MaxConcurrency: account.Concurrency,
					Timeout: cfg.FallbackWaitTimeout, MaxWaiting: cfg.FallbackMaxWaiting,
				})
				return markStickySessionHit(selection, true), err
			}
		}
		selection, err := s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{AccountID: account.ID, MaxConcurrency: account.Concurrency, Timeout: cfg.StickySessionWaitTimeout, MaxWaiting: cfg.StickySessionMaxWaiting})
		return markStickySessionHit(selection, true), err
	}
	if groupQualityGateExempt(ctx) || req.DisableStickyEscape || req.PreviousResponseID != "" || req.SkipStickyCapacityRetry || NormalizeOpenAICompatiblePlatform(req.Platform) != PlatformOpenAI {
		return wait()
	}
	retryUntil := time.Now().Add(openAIStickyCapacityRetryDuration)
	retryDelay := openAIStickyCapacityRetryInitial
	for {
		delay := retryDelay
		if remaining := time.Until(retryUntil); remaining < delay {
			delay = remaining
		}
		if delay <= 0 {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			result, err := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return wait()
			}
			if result != nil && result.Acquired {
				selection, err := s.newAcquiredSelectionResult(ctx, account, result.ReleaseFunc)
				return markStickySessionHit(selection, true), err
			}
		}
		if retryDelay < 200*time.Millisecond {
			retryDelay *= 2
			if retryDelay > 200*time.Millisecond {
				retryDelay = 200 * time.Millisecond
			}
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	selection, err := s.selectOpenAIStickyCapacityBorrow(ctx, account, req)
	if err != nil || selection != nil {
		return selection, err
	}
	return wait()
}

// selectOpenAIStickyCapacityBorrow tries only immediately available spillover
// accounts. It deliberately does not wait on the sticky owner or create a wait
// plan; callers that layer additional admission tiers can finish those tiers
// before deciding whether the owner should receive the final wait plan.
func (s *OpenAIGatewayService) selectOpenAIStickyCapacityBorrow(ctx context.Context, account *Account, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, error) {
	return s.selectOpenAIStickyCapacityResult(ctx, account, req, false)
}

func (s *OpenAIGatewayService) selectOpenAIStickyCapacityFallbackWait(ctx context.Context, account *Account, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, error) {
	return s.selectOpenAIStickyCapacityResult(ctx, account, req, true)
}

func (s *OpenAIGatewayService) selectOpenAIStickyCapacityResult(ctx context.Context, account *Account, req OpenAIAccountScheduleRequest, fallbackWait bool) (*AccountSelectionResult, error) {
	if account == nil {
		return nil, nil
	}
	cfg := s.schedulingConfig()
	accounts, err := s.listSchedulableAccounts(ctx, req.GroupID, req.Platform)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Like a slot error, a failed listing leaves the owner's wait plan.
		slog.Warn("openai_sticky_capacity_list_failed", "group_id", derefGroupID(req.GroupID), "account_id", account.ID, "error", err)
		return nil, nil
	}
	gate := &defaultOpenAIAccountScheduler{service: s, stats: newOpenAIAccountRuntimeStats()}
	candidates := make([]accountWithLoad, 0, len(accounts))
	loadRequests := make([]AccountWithConcurrency, 0, len(accounts))
	for i := range accounts {
		acc := &accounts[i]
		if !fallbackWait && acc.ID == account.ID {
			continue
		}
		if _, excluded := req.ExcludedIDs[acc.ID]; excluded {
			continue
		}
		if !isOpenAICompatibleAccountEligibleForRequest(ctx, acc, req.Platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability, s) || !s.openAIAccountMatchesSchedulingGroup(acc, req.GroupID) || !gate.isAccountRequestCompatible(ctx, acc, req) || !gate.isAccountTransportCompatible(acc, req.RequiredTransport) {
			continue
		}
		candidates = append(candidates, accountWithLoad{account: acc, loadInfo: &AccountLoadInfo{AccountID: acc.ID}})
		loadRequests = append(loadRequests, AccountWithConcurrency{ID: acc.ID, MaxConcurrency: acc.EffectiveLoadFactor()})
	}
	var loadMap map[int64]*AccountLoadInfo
	if !fallbackWait && (cfg.LoadBatchEnabled || req.AdvancedScheduler) && s.concurrencyService != nil && len(loadRequests) > 0 {
		if loads, err := s.concurrencyService.GetAccountsLoadBatch(ctx, loadRequests); err == nil {
			loadMap = loads
			for i := range candidates {
				if load := loads[candidates[i].account.ID]; load != nil {
					candidates[i].loadInfo = load
				}
			}
		}
	}
	rates := openAILegacyUpstreamRateOrder{}
	if !req.AdvancedScheduler && req.UseUpstreamTokenCost && s.isOpenAILowUpstreamRatePriorityEnabled(ctx, req.GroupID) {
		ptrs := make([]*Account, 0, len(candidates))
		for _, item := range candidates {
			ptrs = append(ptrs, item.account)
		}
		rates = newOpenAILegacyUpstreamRateOrder(ptrs, time.Now(), s.openAIOAuthSchedulingRateMultiplier(ctx))
		rates.enabled = true
	}
	order := s.openAICandidateOrderForItems(req.GroupID, rates, candidates)
	if req.AdvancedScheduler && !fallbackWait {
		filtered := make([]*Account, 0, len(candidates))
		for _, item := range candidates {
			filtered = append(filtered, item.account)
		}
		advanced := &defaultOpenAIAccountScheduler{service: s, stats: s.openaiAccountStats}
		plan := advanced.buildOpenAIAccountLoadPlan(ctx, req, filtered, loadMap)
		positions := make(map[int64]int, len(plan.selectionOrder))
		for i, candidate := range plan.selectionOrder {
			positions[candidate.account.ID] = i
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			left, leftOK := positions[candidates[i].account.ID]
			right, rightOK := positions[candidates[j].account.ID]
			if leftOK != rightOK {
				return leftOK
			}
			if leftOK && left != right {
				return left < right
			}
			return openAICandidateLess(candidates[i], candidates[j], order)
		})
	} else {
		sort.SliceStable(candidates, func(i, j int) bool {
			if req.RequireCompact {
				left, right := openAICompactSupportTier(candidates[i].account), openAICompactSupportTier(candidates[j].account)
				if left != right {
					return left > right
				}
			}
			return openAICandidateLess(candidates[i], candidates[j], order)
		})
	}
	if !fallbackWait {
		// Reuse the same recent-success/hash ordering in all saturation paths.
		// The advanced scheduler's cost cohorts follow its own cost weight.
		sessionRates := rates
		if req.AdvancedScheduler && req.UseUpstreamTokenCost && s.openAIWSSchedulerWeightsForRequest(ctx).UpstreamCost > 0 {
			ptrs := make([]*Account, 0, len(candidates))
			for _, item := range candidates {
				ptrs = append(ptrs, item.account)
			}
			sessionRates = newOpenAILegacyUpstreamRateOrder(ptrs, time.Now(), s.openAIOAuthSchedulingRateMultiplier(ctx))
			sessionRates.enabled = true
		}
		s.preferOpenAISessionOrder(ctx, candidates, req.GroupID, req.SessionHash, req.RequestedModel, sessionRates, req.RequireCompact, req.RequiredCapability, true, order.tiers)
	}
	s.orderOpenAIPredictedLoad(candidates, order, req.RequestedModel, req.RequireCompact)
	candidates = s.partitionOpenAILatencyHeldLoad(ctx, candidates, req.RequestedModel, req.RequireCompact, req.GroupID)
	if fallbackWait {
		for _, item := range candidates {
			fresh := s.recheckSelectedOpenAIAccountFromDB(ctx, item.account, req.GroupID, req.Platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability)
			if fresh == nil || !gate.isAccountRequestCompatible(ctx, fresh, req) || !gate.isAccountTransportCompatible(fresh, req.RequiredTransport) {
				continue
			}
			selection, err := s.newSelectionResult(ctx, fresh, false, nil, &AccountWaitPlan{
				AccountID: fresh.ID, MaxConcurrency: fresh.Concurrency,
				Timeout: cfg.FallbackWaitTimeout, MaxWaiting: cfg.FallbackMaxWaiting,
			})
			spillover := fresh.ID != account.ID
			return markStickySessionHit(markOpenAIStickySpillover(selection, spillover), !spillover), err
		}
		return nil, nil
	}
	// Borrowing acquires before the DB recheck, as Layer 2 of the advanced
	// scheduler does, and shares its probe limit.
	budget := newOpenAISelectionProbeBudget()
	budget.enableLimit()
	for _, item := range candidates {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// A known full or queued account is skipped without probing.
		if loadMap[item.account.ID] != nil && (item.loadInfo.LoadRate >= 100 || item.account.Concurrency > 0 && item.loadInfo.CurrentConcurrency >= item.account.Concurrency) {
			continue
		}
		// An exhausted probe budget or a slot error leaves the owner's wait plan.
		result, attempted, err := gate.tryAcquireOpenAIAccountSlot(ctx, item.account.ID, item.account.Concurrency, budget)
		if !attempted || err != nil {
			return nil, ctx.Err()
		}
		if result == nil || !result.Acquired {
			continue
		}
		if !gate.consumeOpenAISelectionDBRecheck(budget) {
			result.ReleaseFunc()
			return nil, nil
		}
		fresh := s.recheckSelectedOpenAIAccountFromDB(ctx, item.account, req.GroupID, req.Platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability)
		if fresh != nil && fresh.Concurrency != item.account.Concurrency {
			// The slot was taken under a stale limit; take it under the current one.
			result.ReleaseFunc()
			result, attempted, err = gate.tryAcquireOpenAIAccountSlot(ctx, fresh.ID, fresh.Concurrency, budget)
			if !attempted || err != nil {
				return nil, ctx.Err()
			}
			if result == nil || !result.Acquired {
				continue
			}
		}
		if fresh == nil || !gate.isAccountRequestCompatible(ctx, fresh, req) || !gate.isAccountTransportCompatible(fresh, req.RequiredTransport) {
			result.ReleaseFunc()
			continue
		}
		selection, err := s.newAcquiredSelectionResult(ctx, fresh, result.ReleaseFunc)
		return markOpenAIStickySpillover(selection, true), err
	}
	return nil, nil
}
