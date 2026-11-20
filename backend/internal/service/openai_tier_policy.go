package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAITierDecision is the immutable dispatch contract for one credential and turn.
type OpenAITierDecision struct {
	preserveWire        bool
	prices              *openAITierPrices
	forwardedEffort     *string
	ClientPresence      string
	ClientTier          string
	OutboundTier        string
	PolicyVersion       int
	Source              string
	Reason              string
	OriginGroupID       int64
	ServingGroupID      int64
	FastDisabled        bool
	UltrafastDisabled   bool
	LegacyForceFast     bool
	FreeFast            bool
	FreeUltrafast       bool
	CredentialAccountID int64
	Endpoint            string
	CapabilitySource    string
	CapabilityExpiresAt time.Time
}

type openAITierAttemptKey struct{}
type openAITierAttempt struct {
	requested string
	client    gjson.Result
	decision  *OpenAITierDecision
}

func withOpenAITierAttempt(ctx context.Context, body []byte) (context.Context, *openAITierAttempt) {
	a := &openAITierAttempt{requested: gjson.GetBytes(body, "model").String(), client: gjson.GetBytes(body, "service_tier")}
	return context.WithValue(ctx, openAITierAttemptKey{}, a), a
}

func (a *openAITierAttempt) finish(result *OpenAIForwardResult) {
	if a != nil && result != nil && a.decision != nil {
		result.TierDecision = a.decision
		result.ServiceTier = normalizeOpenAIServiceTier(a.decision.OutboundTier)
	}
}

func openAITierGroups(ctx context.Context) (origin, serving *Group) {
	serving = ServingPricingGroup(ctx)
	origin = serving
	if ctx == nil {
		return
	}
	key, _ := ctx.Value(billingAPIKeySnapshotKey{}).(*APIKey)
	if key == nil {
		key, _ = resilience.StateFrom(ctx).FallbackOrigin().(*APIKey)
	}
	if key != nil && IsGroupContextValid(key.Group) {
		origin = key.Group
	}
	return
}

func openAITierTextRoute(account *Account, model string) bool {
	return account != nil && account.IsOpenAI() && openAITierTextModel(model)
}

func openAITierTextModel(model string) bool {
	if !codexModelKeepsGPTIdentity(model) || isCodexDedicatedMediaModel(model) {
		return false
	}
	model = strings.ToLower(model)
	for _, media := range []string{"audio", "realtime", "transcribe", "-tts", "image", "embedd"} {
		if strings.Contains(model, media) {
			return false
		}
	}
	return true
}

type openAITierConstraints struct {
	OpenAITierDecision
	forced   string
	conflict bool
}

func openAITierLimits(origin, serving *Group, account *Account, model string) openAITierConstraints {
	c := openAITierConstraints{OpenAITierDecision: OpenAITierDecision{PolicyVersion: OpenAITierPolicyLegacy}}
	if origin != nil {
		c.OriginGroupID = origin.ID
	}
	if serving != nil {
		c.ServingGroupID = serving.ID
		if groupSupportsOpenAIFast(serving.Platform) && account != nil && account.IsOpenAI() {
			if serving.OpenAITierPolicyVersion < OpenAITierPolicyExplicit || openAITierTextRoute(account, model) {
				c.FreeFast = serving.FreeOpenAIFast
			}
			if serving.OpenAITierPolicyVersion >= OpenAITierPolicyExplicit && openAITierTextRoute(account, model) {
				c.FreeUltrafast = serving.FreeOpenAIUltrafast
			}
		}
		c.LegacyForceFast = serving.OpenAITierPolicyVersion < OpenAITierPolicyExplicit && groupForcesOpenAIFast(serving, account)
	}
	if !openAITierTextRoute(account, model) {
		return c
	}
	apply := func(g *Group) {
		if g == nil || !groupSupportsOpenAIFast(g.Platform) || g.OpenAITierPolicyVersion < OpenAITierPolicyExplicit {
			return
		}
		c.PolicyVersion = g.OpenAITierPolicyVersion
		c.FastDisabled = c.FastDisabled || g.OpenAIFastMode == OpenAITierModeForceOff
		c.UltrafastDisabled = c.UltrafastDisabled || g.OpenAIUltrafastMode == OpenAITierModeForceOff
		for _, row := range []struct{ mode, tier string }{{g.OpenAIFastMode, OpenAIFastTierPriority}, {g.OpenAIUltrafastMode, OpenAIFastTierUltrafast}} {
			if row.mode != OpenAITierModeForceOn {
				continue
			}
			if c.forced != "" && c.forced != row.tier {
				c.conflict = true
			}
			c.forced = row.tier
		}
	}
	apply(serving)
	if origin != serving {
		apply(origin)
	}
	if c.LegacyForceFast && c.PolicyVersion >= OpenAITierPolicyExplicit {
		c.conflict = c.conflict || (c.forced != "" && c.forced != OpenAIFastTierPriority)
		c.forced = OpenAIFastTierPriority
	}
	c.conflict = c.conflict || c.disabled(c.forced)
	return c
}

func (c openAITierConstraints) disabled(tier string) bool {
	return tier == OpenAIFastTierPriority && c.FastDisabled || tier == OpenAIFastTierUltrafast && c.UltrafastDisabled
}

func (c openAITierConstraints) capabilityTier() string {
	if c.forced != "" {
		return c.forced
	}
	if c.FastDisabled || c.UltrafastDisabled {
		return "default"
	}
	return ""
}

func officialOpenAITierEndpoint(account *Account) bool {
	u, err := url.Parse(account.GetOpenAIBaseURL())
	if err != nil || u.Scheme != "https" || (u.Port() != "" && u.Port() != "443") || u.User != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "api.openai.com") && (strings.Trim(u.Path, "/") == "" || strings.Trim(u.Path, "/") == "v1")
}

type openAITierCapability struct {
	state     string // supported, unsupported, unknown
	source    string
	expiresAt time.Time
}

func officialOpenAIUltraModel(model string) bool {
	switch model {
	case "gpt-6", "gpt-6-astra", "gpt-6.2-astra", "gpt-6.2-sol", "gpt-6.1-sol":
		return true
	}
	return false
}

func officialOpenAIFastModel(model string) bool {
	if officialOpenAIUltraModel(model) {
		return true
	}
	switch model {
	case "gpt-6.1-terra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.6", "gpt-5.5", "gpt-5.5-pro", "gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano":
		return true
	}
	return false
}

func resolveOpenAITierCapability(account *Account, model, tier string, now time.Time, endpoint ...string) openAITierCapability {
	result := openAITierCapability{state: "unknown", source: "no_account_evidence"}
	if account == nil || !openAITierTextRoute(account, model) {
		return result
	}
	protocol := resilience.CapabilityProtocolResponses
	if len(endpoint) > 0 {
		protocol = normalizeCapabilityProtocol(endpoint[0])
	}
	if mark := account.CapabilityMarked(model, protocol, "tier:"+tier, now); mark != nil {
		return openAITierCapability{state: "unsupported", source: "upstream_refusal", expiresAt: mark.ExpiresAt}
	}
	// A synchronized service_tiers list belongs to this credential and final model.
	// Its absence supplies no evidence for a third-party endpoint.
	if snapshot := account.GetUpstreamModelMetadataSnapshot(); snapshot != nil {
		entry, found := snapshot.Models[model]
		raw, hasTiers := entry.CodexToolCapabilities["service_tiers"]
		if specific, ok := entry.ServiceTiersByEndpoint[protocol]; ok {
			raw = specific
			hasTiers = true
		} else if protocol != resilience.CapabilityProtocolResponses {
			hasTiers = false
		}
		if found && hasTiers {
			var available []configuredCodexServiceTier
			if json.Unmarshal(raw, &available) == nil && available != nil {
				result.source = "account_manifest"
				syncedAt := entry.ServiceTiersSyncedAt
				if syncedAt == "" && snapshot.Source != "models.dev" {
					syncedAt = snapshot.SyncedAt
				}
				if entry.TierCredentialFingerprint != capabilityFingerprint(account) {
					return result
				}
				synced, err := time.Parse(time.RFC3339, syncedAt)
				if err != nil {
					return result
				}
				result.expiresAt = synced.Add(24 * time.Hour)
				if !now.Before(result.expiresAt) {
					return result
				}
				result.state = "unsupported"
				for _, option := range available {
					if normalizedOpenAIServiceTierValue(option.ID) == tier {
						result.state = "supported"
						break
					}
				}
				if tier != "default" || result.state == "supported" {
					return result
				}
				// Official Codex and OpenAI API contracts accept the explicit default sentinel.
				if account.IsOpenAIOAuthLike() || account.IsOpenAIApiKey() && officialOpenAITierEndpoint(account) {
					result.state = "supported"
				}
				return result
			}
		}
	}
	if account.IsOpenAIApiKey() && officialOpenAITierEndpoint(account) {
		result.source = "official_api"
		if tier == "default" || tier == OpenAIFastTierPriority && officialOpenAIFastModel(model) || tier == OpenAIFastTierUltrafast && officialOpenAIUltraModel(model) {
			result.state = "supported"
		}
	} else if account.IsOpenAIOAuthLike() && tier == "default" {
		result.source, result.state = "codex_standard_contract", "supported"
	}
	return result
}

const openAITierUnavailable = "OPENAI_TIER_CAPABILITY_UNAVAILABLE"
const openAITierRouteConflict = "OPENAI_TIER_ROUTE_CONFLICT"

func openAITierUnavailableError(reason string) error {
	return &UpstreamFailoverError{PreDispatchSelectionInvalidated: true, StatusCode: http.StatusServiceUnavailable, ClientStatusCode: http.StatusServiceUnavailable, Reason: GatewayFailureReason(reason), ClientMessage: reason, NextAccountAction: NextAccountRetry}
}

func decideOpenAITier(c openAITierConstraints, account *Account, model, candidate, action, source, msg string) (OpenAITierDecision, error) {
	d := c.OpenAITierDecision
	d.OutboundTier, d.Source, d.Reason = candidate, source, action
	switch {
	case c.conflict:
		return d, openAITierUnavailableError(openAITierRouteConflict)
	case c.forced != "":
		d.OutboundTier, d.Source, d.Reason = c.forced, "group", "force_on"
	case c.LegacyForceFast:
		d.OutboundTier, d.Source, d.Reason = OpenAIFastTierPriority, "legacy_group", "force_on"
	case c.disabled(candidate):
		d.OutboundTier, d.Source, d.Reason = "default", "group", "explicit_disabled"
	default:
		switch action {
		case BetaPolicyActionBlock:
			if msg == "" {
				msg = fmt.Sprintf("openai service_tier=%s is not allowed for model %s", candidate, model)
			}
			return d, &OpenAIFastBlockedError{Message: msg}
		case BetaPolicyActionFilter:
			d.OutboundTier = ""
		case OpenAIFastPolicyActionForcePriority:
			d.OutboundTier = OpenAIFastTierPriority
		}
		if (c.FastDisabled || c.UltrafastDisabled) && (c.disabled(d.OutboundTier) || d.OutboundTier == "" || d.OutboundTier == "auto" || d.OutboundTier == "scale") {
			d.OutboundTier, d.Source, d.Reason = "default", "group", "prevent_implicit_acceleration"
		}
	}
	return d, nil
}

type openAITierEndpointKey struct{}
type openAITierInputKey struct{}
type openAITierInput struct {
	requestedModel string
	field          gjson.Result
	candidate      []byte
	protocol       string
}

func WithOpenAITierInput(ctx context.Context, body []byte, protocol, anthropicBeta string) context.Context {
	ctx = withOpenAITierProfitInput(ctx, body)
	field := gjson.GetBytes(body, "service_tier")
	candidate := []byte("{}")
	if field.Exists() {
		candidate, _ = sjson.SetRawBytes(candidate, "service_tier", []byte(field.Raw))
	} else if protocol == APIProtocolAnthropic && containsBetaToken(anthropicBeta, claude.BetaFastMode) {
		candidate = []byte(`{"service_tier":"priority"}`)
	}
	return context.WithValue(ctx, openAITierInputKey{}, openAITierInput{requestedModel: gjson.GetBytes(body, "model").String(), field: field, candidate: candidate, protocol: protocol})
}

func (s *OpenAIGatewayService) resolveOpenAITier(ctx context.Context, account *Account, model string, body []byte) (*OpenAITierDecision, error) {
	d, err := s.openAITierDecisionForBody(ctx, account, model, body)
	if err != nil {
		return nil, err
	}
	if openAITierTextRoute(account, model) && s != nil {
		effort := optionalStringValue(CanonicalRequestedReasoningEffort(body, model))
		d.forwardedEffort = &effort
		if err := s.prepareOpenAITierDispatch(ctx, account, model, d); err != nil {
			return nil, err
		}
	}
	if attempt, _ := ctx.Value(openAITierAttemptKey{}).(*openAITierAttempt); attempt != nil {
		attempt.decision = d
	}
	return d, nil
}

type openAITierOriginRouteKey struct{}

func openAITierGroupHasHardLimit(g *Group) bool {
	return g != nil && g.OpenAITierPolicyVersion >= OpenAITierPolicyExplicit &&
		(g.OpenAIFastMode == OpenAITierModeForceOn || g.OpenAIFastMode == OpenAITierModeForceOff || g.OpenAIUltrafastMode == OpenAITierModeForceOn || g.OpenAIUltrafastMode == OpenAITierModeForceOff)
}

// Composite fallback routing happens before protocol dispatch. Resolve the public
// model in the origin group so a serving group's native route cannot escape its limits.
func OpenAITierCompositeFallback(ctx context.Context, resolver *CompositeRouteResolver, key *APIKey, serving CompositeRouteDecision) (context.Context, error) {
	origin := BillingAPIKey(ctx, key)
	if key == nil || origin == nil || origin.Group == nil || origin.Group.Platform != PlatformComposite || key.Group == nil || key.Group.ID == origin.Group.ID || !openAITierGroupHasHardLimit(origin.Group) {
		return ctx, nil
	}
	switch serving.Endpoint {
	case CompositeRouteEndpointResponses, CompositeRouteEndpointMessages, CompositeRouteEndpointChatCompletions:
	default:
		return ctx, nil
	}
	from, err := resolver.Resolve(ctx, origin.Group.ID, serving.PublicModel, serving.Endpoint)
	if err != nil {
		return ctx, err
	}
	if !from.Matched || from.TargetPlatform != PlatformOpenAI || (!openAITierTextModel(from.UpstreamModel) && from.Source != CompositeRouteSourceAccount) {
		return ctx, nil
	}
	if !serving.Matched || serving.TargetPlatform != PlatformOpenAI {
		return ctx, openAITierUnavailableError(openAITierRouteConflict)
	}
	return context.WithValue(ctx, openAITierOriginRouteKey{}, true), nil
}

func openAITierFallbackNeedsGPTRoute(ctx context.Context, account *Account, model string) bool {
	origin, serving := openAITierGroups(ctx)
	if origin == nil || serving == nil || origin.ID == serving.ID || !openAITierGroupHasHardLimit(origin) {
		return false
	}
	if marked, _ := ctx.Value(openAITierOriginRouteKey{}).(bool); marked {
		return true
	}
	in, _ := ctx.Value(openAITierInputKey{}).(openAITierInput)
	requested := in.requestedModel
	if requested == "" {
		if a, _ := ctx.Value(openAITierAttemptKey{}).(*openAITierAttempt); a != nil {
			requested = a.requested
		}
	}
	if requested == "" {
		requested = model
	}
	return openAITierTextModel(requested)
}

func (s *OpenAIGatewayService) openAITierSelectionReason(ctx context.Context, account *Account, requestedModel string, compact bool) string {
	if _, ok := ctx.Value(openAITierInputKey{}).(openAITierInput); !ok {
		return ""
	}
	_, mapped := resolveOpenAIForwardMappedModels(account, requestedModel, compact)
	if compact {
		ctx = context.WithValue(ctx, openAITierEndpointKey{}, "compact")
	}
	return s.openAITierCandidateFailure(ctx, account, mapped)
}
func (s *OpenAIGatewayService) openAITierCandidateFailure(ctx context.Context, account *Account, model string) string {
	if !openAITierTextRoute(account, model) {
		if openAITierFallbackNeedsGPTRoute(ctx, account, model) {
			return openAITierRouteConflict
		}
		return ""
	}
	in, ok := ctx.Value(openAITierInputKey{}).(openAITierInput)
	if !ok {
		return ""
	}
	origin, serving := openAITierGroups(ctx)
	if ctx.Value(openAIProfitControlGateCtxKey{}) == nil && (origin == nil || origin.OpenAITierPolicyVersion < OpenAITierPolicyExplicit) && (serving == nil || serving.OpenAITierPolicyVersion < OpenAITierPolicyExplicit) {
		return ""
	}
	if s != nil && s.resolveUpstreamProtocolFor(ctx, account, in.protocol, model) == APIProtocolAnthropic {
		if openAITierFallbackNeedsGPTRoute(ctx, account, model) {
			return openAITierRouteConflict
		}
		return ""
	}
	d, err := s.openAITierDecisionForBody(ctx, account, model, in.candidate)
	if failure, ok := err.(*UpstreamFailoverError); ok {
		return string(failure.Reason)
	}
	// rule blocks and invalid input are reported by the dispatch adapter (403/400).
	if d != nil && s != nil {
		return s.openAITierCandidateProfitReason(ctx, account, model, d)
	}
	return ""
}

func (s *OpenAIGatewayService) openAITierDecisionForBody(ctx context.Context, account *Account, model string, body []byte) (*OpenAITierDecision, error) {
	origin, serving := openAITierGroups(ctx)
	if !openAITierTextRoute(account, model) && openAITierFallbackNeedsGPTRoute(ctx, account, model) {
		return nil, openAITierUnavailableError(openAITierRouteConflict)
	}
	limits := openAITierLimits(origin, serving, account, model)
	field := gjson.GetBytes(body, "service_tier")
	client := field
	attempt, _ := ctx.Value(openAITierAttemptKey{}).(*openAITierAttempt)
	if attempt != nil {
		client = attempt.client
	}
	// Legacy internal callers supplied already validated requests and forced Fast
	// for every payload. Public HTTP and WS adapters validate the original input.
	validate := limits.PolicyVersion >= OpenAITierPolicyExplicit || attempt != nil
	limits.ClientPresence = "missing"
	if client.Exists() {
		limits.ClientPresence = "null"
		if client.Type != gjson.Null {
			limits.ClientPresence, limits.ClientTier = "explicit", client.String()
			if validate && (client.Type != gjson.String || normalizedOpenAIServiceTierValue(limits.ClientTier) == "") {
				return nil, &ErrInvalidOpenAIServiceTier{Value: boundInvalidOpenAIServiceTierValue(limits.ClientTier)}
			}
		}
	}
	candidate := normalizedOpenAIServiceTierValue(field.String())
	if limits.ClientPresence == "explicit" {
		candidate = normalizedOpenAIServiceTierValue(limits.ClientTier)
	}
	if !validate && !limits.LegacyForceFast && field.Exists() && field.Type != gjson.Null && candidate == "" {
		return &OpenAITierDecision{preserveWire: true}, nil
	}
	if validate && field.Exists() && field.Type != gjson.Null && candidate == "" {
		return nil, &ErrInvalidOpenAIServiceTier{Value: boundInvalidOpenAIServiceTierValue(field.String())}
	}
	action, source, msg := BetaPolicyActionPass, "request", ""
	if !limits.conflict && limits.forced == "" && !limits.LegacyForceFast && !limits.disabled(candidate) {
		matcher := candidate
		if matcher == "" {
			matcher = OpenAIFastTierMissing
		}
		settings := openAIFastPolicySettingsFromContext(ctx)
		if settings == nil && s != nil && s.settingService != nil {
			settings, _ = s.settingService.GetOpenAIFastPolicySettings(ctx)
		}
		action, msg, source = openAIFastRuleDecision(settings, openAIFastPolicyUserID(ctx), account, model, matcher)
		if matcher == OpenAIFastTierMissing && (action != OpenAIFastPolicyActionForcePriority || account == nil || !account.IsOpenAI()) {
			action, source = BetaPolicyActionPass, "request"
		}
	}
	d, err := decideOpenAITier(limits, account, model, candidate, action, source, msg)
	if err != nil {
		return nil, err
	}
	if tier := limits.capabilityTier(); tier != "" {
		credential := account
		if account.IsShadow() {
			if s == nil || s.accountRepo == nil {
				return nil, openAITierUnavailableError(openAITierUnavailable)
			}
			credential, err = resolveCredentialAccount(ctx, s.accountRepo, account)
			if err != nil {
				return nil, openAITierUnavailableError(openAITierUnavailable)
			}
		}
		protocol := APIProtocolResponses
		if in, ok := ctx.Value(openAITierInputKey{}).(openAITierInput); ok {
			protocol = s.resolveUpstreamProtocolFor(ctx, account, in.protocol, model)
		}
		if p, ok := ctx.Value(openAITierEndpointKey{}).(string); ok {
			protocol = p
		}
		d.Endpoint = normalizeCapabilityProtocol(protocol)
		d.CredentialAccountID = credential.ID
		cap := resolveOpenAITierCapability(credential, model, tier, time.Now(), d.Endpoint)
		if cap.state != "supported" {
			return nil, openAITierUnavailableError(openAITierUnavailable)
		}
		d.CapabilitySource, d.CapabilityExpiresAt = cap.source, cap.expiresAt
	}
	return &d, nil
}

func writeOpenAITier(body []byte, d *OpenAITierDecision) ([]byte, error) {
	if d.preserveWire {
		return body, nil
	}
	field := gjson.GetBytes(body, "service_tier")
	if d.OutboundTier == "" {
		if !field.Exists() || field.Type == gjson.Null {
			return body, nil
		}
		return sjson.DeleteBytes(body, "service_tier")
	}
	if field.Type == gjson.String && field.String() == d.OutboundTier {
		return body, nil
	}
	return sjson.SetBytes(body, "service_tier", d.OutboundTier)
}

func openAIWSTierTurnContext(ctx context.Context, hooks *OpenAIWSIngressHooks, turn int, raw []byte) (context.Context, *openAITierAttempt) {
	if hooks != nil && hooks.PolicyContext != nil {
		groupCtx := hooks.PolicyContext(ctx, turn)
		// Preserve the connection-lifetime global-rule snapshot even when BeforeTurn
		// initialized billing from the outer handshake context first.
		ctx = withOpenAIFastPolicyContext(groupCtx, openAIFastPolicySettingsFromContext(ctx))
	}
	return withOpenAITierAttempt(WithOpenAITierInput(ctx, raw, APIProtocolResponses, ""), raw)
}

func openAIWSRawTierEvidence(raw string) []string {
	if raw == "" {
		return nil
	}
	return []string{boundInvalidOpenAIServiceTierValue(raw)}
}

func (a *openAITierAttempt) bindsRoutingHint() bool {
	return a != nil && a.decision != nil && a.decision.PolicyVersion >= OpenAITierPolicyExplicit
}

// Only an explicit rejection of this field/value is evidence against a tier.
// Rate limits, access-token failures and generic unsupported-model messages
// continue through their existing classifiers.
func isOpenAITierRefusal(status int, payload []byte, tier string) bool {
	if status != 400 && status != 422 {
		return false
	}
	value := gjson.GetBytes(payload, "error")
	if !value.Exists() {
		value = gjson.GetBytes(payload, "response.error")
	}
	param := strings.ToLower(value.Get("param").String())
	message := strings.ToLower(value.Get("message").String())
	if param != "service_tier" && !strings.Contains(message, "service_tier") {
		return false
	}
	if !strings.Contains(message, tier) && !strings.Contains(message, "unsupported parameter") {
		return false
	}
	switch value.Get("code").String() {
	case "unsupported_value", "unsupported_parameter", "service_tier_not_supported":
		return true
	}
	return strings.Contains(message, "not supported") || strings.Contains(message, "unsupported") || strings.Contains(message, "does not support")
}

func (s *OpenAIGatewayService) learnOpenAITierRefusal(ctx context.Context, account *Account, model, tier, protocol string, status int, refusal []byte) bool {
	if !openAITierTextRoute(account, model) || (tier != "default" && tier != OpenAIFastTierPriority && tier != OpenAIFastTierUltrafast) || !isOpenAITierRefusal(status, refusal, tier) {
		return false
	}
	if s != nil && s.accountRepo != nil {
		credential, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err == nil {
			err = s.LearnCapabilityMark(ctx, credential, CapabilityMark{Model: model, Protocol: protocol, Shape: "tier:" + tier, Reason: "tier_unsupported"})
		}
		if err != nil {
			slog.Warn("openai tier capability refusal could not be saved", "account_id", account.ID, "model", model, "tier", tier, "err", err)
		}
	}
	return true
}
