package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

const quotaCooldownEvidenceKey = "quota_cooldown_evidence"

// QuotaCooldownEvidence separates an upstream quota window from the time at
// which this key should be reconsidered. RetryNotBefore is never capped.
type QuotaCooldownEvidence struct {
	Owner, Source, Protocol, OriginDigest    string
	RawReset, RetryNotBefore, EffectiveUntil time.Time
}

// cooldownAttemptTarget consumes the actual send target. Only fixed routes can
// recover a missing target from the same address accessor used by their builder.
func cooldownAttemptTarget(ctx context.Context, account *Account) (*resilience.UpstreamAttemptTarget, bool) {
	if account == nil || account.Type != AccountTypeAPIKey {
		return nil, false
	}
	if target := resilience.StateFrom(ctx).UpstreamAttemptTarget(); target != nil && target.AccountID == account.ID {
		if origin, err := upstreamBalanceOrigin(target.Origin); err == nil && target.Protocol != "" {
			parsed, _ := url.Parse(origin)
			if parsed.Scheme == "http" || parsed.Scheme == "https" {
				target.Origin = origin
				return target, true
			}
		}
		return nil, false
	}
	if account.IsAdaptiveAPIProtocol() {
		return nil, false
	}
	var protocol, base string
	switch {
	case account.Platform == PlatformAnthropic:
		protocol, base = APIProtocolAnthropic, account.GetBaseURL()
	case account.Platform == PlatformOpenAI || account.IsMultiProtocolAPIKey():
		protocol = resolveUpstreamProtocol(account, APIProtocolChatCompletions, "", nil)
		base = account.GetOpenAIBaseURL()
		if protocol == APIProtocolAnthropic {
			base = account.GetAnthropicProtocolBaseURL()
		}
	default:
		return nil, false
	}
	origin, err := upstreamBalanceOrigin(base)
	if err != nil {
		return nil, false
	}
	return &resilience.UpstreamAttemptTarget{AccountID: account.ID, Protocol: protocol, Origin: origin}, true
}

func cooldownTargetOfficial(account *Account, target *resilience.UpstreamAttemptTarget) bool {
	if target == nil || target.Origin == "" {
		return false
	}
	if account.Platform == PlatformKimi || account.Platform == PlatformDeepseek {
		actual, err := url.Parse(target.Origin)
		official, officialErr := url.Parse(account.defaultProviderBaseURL(target.Protocol))
		return err == nil && officialErr == nil && actual.Scheme == "https" &&
			strings.EqualFold(actual.Hostname(), official.Hostname())
	}
	return upstreamBalanceTargetIsOfficialAPI(target.Origin)
}

func cooldownOriginDigest(origin string) string {
	digest := sha256.Sum256([]byte(origin))
	return hex.EncodeToString(digest[:])
}

func (e QuotaCooldownEvidence) extra() map[string]any {
	value := map[string]any{
		"owner": e.Owner, "source": e.Source, "protocol": e.Protocol,
		"origin_digest": e.OriginDigest,
	}
	for key, at := range map[string]time.Time{"raw_reset": e.RawReset, "retry_not_before": e.RetryNotBefore, "effective_until": e.EffectiveUntil} {
		if !at.IsZero() {
			value[key] = at.UTC().Format(time.RFC3339Nano)
		}
	}
	return value
}

func (s *RateLimitService) applyThirdParty429(ctx context.Context, account *Account, headers http.Header, body []byte) bool {
	target, known := cooldownAttemptTarget(ctx, account)
	if !known || cooldownTargetOfficial(account, target) || account.IsShadow() ||
		classifyUpstreamHTTP(http.StatusTooManyRequests, headers, body, "").Category == resilience.CategoryBalance {
		return false
	}
	now := time.Now()
	evidence := QuotaCooldownEvidence{Owner: "unknown", Protocol: target.Protocol, OriginDigest: cooldownOriginDigest(target.Origin)}
	if reset := calculateOpenAI429ResetTime(headers); reset != nil {
		evidence.RawReset = *reset
	}
	if reset := parseOpenAIRateLimitResetTime(body); reset != nil && time.Unix(*reset, 0).After(evidence.RawReset) {
		evidence.RawReset = time.Unix(*reset, 0)
	}
	if reset := calculateAnthropic429ResetTime(headers); reset != nil && reset.resetAt.After(evidence.RawReset) {
		evidence.RawReset = reset.resetAt
	}
	hint, hinted := resilience.ParseUpstreamRecoveryHint(headers, body, now)
	state := resilience.StateFrom(ctx)
	if actual := state.UpstreamAttemptTarget(); actual != nil && actual.AccountID == account.ID {
		if current := state.UpstreamRecoveryHint(); current != nil && current.NotBefore.After(hint.NotBefore) {
			hint, hinted = *current, true
		}
	}
	switch {
	case hinted && hint.NotBefore.After(now):
		evidence.RetryNotBefore, evidence.EffectiveUntil, evidence.Source = hint.NotBefore, hint.NotBefore, hint.Source
	case evidence.RawReset.After(now):
		evidence.EffectiveUntil, evidence.Source = evidence.RawReset, "exhausted_reset"
	default:
		cooldown, enabled := s.get429FallbackCooldown(ctx, account)
		if !enabled {
			return true
		}
		evidence.EffectiveUntil, evidence.Source = now.Add(cooldown), "fallback"
	}
	capUntil := now.Add(time.Duration(resilience.Current().Failure.RetryAfterCapSeconds) * time.Second)
	if evidence.EffectiveUntil.After(capUntil) {
		evidence.EffectiveUntil = capUntil
	}
	updates := map[string]any{quotaCooldownEvidenceKey: evidence.extra()}
	if snapshot := ParseCodexRateLimitHeaders(headers); snapshot != nil {
		for key, value := range buildCodexUsageExtraUpdates(snapshot, now) {
			updates[key] = value
		}
	}
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		slog.Warn("third_party_quota_evidence_write_failed", "account_id", account.ID, "error", err)
	}
	s.notifyAccountSchedulingBlocked(account, evidence.EffectiveUntil, "429")
	if err := s.accountRepo.SetRateLimited(ctx, account.ID, evidence.EffectiveUntil); err != nil {
		slog.Warn("third_party_rate_limit_write_failed", "account_id", account.ID, "error", err)
	}
	return true
}

// A raw window is authoritative only for the same official target. Relay and
// unresolved adaptive windows remain evidence and cannot re-pause the key.
func rawQuotaAppliesToTarget(ctx context.Context, account *Account) bool {
	if account == nil || account.Type != AccountTypeAPIKey {
		return true
	}
	if account.Platform != PlatformOpenAI && account.Platform != PlatformAnthropic && !account.IsMultiProtocolAPIKey() {
		return true
	}
	target, known := cooldownAttemptTarget(ctx, account)
	if !known || !cooldownTargetOfficial(account, target) {
		return false
	}
	if evidence, ok := account.Extra[quotaCooldownEvidenceKey].(map[string]any); ok {
		return evidence["owner"] == "key" && evidence["protocol"] == target.Protocol &&
			evidence["origin_digest"] == cooldownOriginDigest(target.Origin)
	}
	return true
}
