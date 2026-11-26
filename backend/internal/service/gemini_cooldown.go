package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

const (
	GeminiRecoveryPendingReason GatewayFailureReason = "gemini_recovery_pending"
	GeminiCooldownExtraKey                           = "gemini_cooldowns"
	GeminiAccountCooldownScope                       = "gemini:account"
	geminiCooldownReasonPrefix                       = "gemini_429:"
	geminiRecoveryLeaseDuration                      = 90 * time.Second
)

// GeminiCooldownRepository serializes decisions with account edits and publishes
// their scheduler event in the same transaction. The callback performs no I/O.
type GeminiCooldownRepository interface {
	MutateGeminiCooldown(context.Context, int64, func(*Account, time.Time) (bool, error)) error
}

type geminiCooldown struct {
	Generation string    `json:"generation"`
	Target     string    `json:"target"`
	ObservedAt time.Time `json:"observed_at"`
	Until      time.Time `json:"until"`
	Streak     int       `json:"streak"`
	RequestID  string    `json:"request_id"`
	Source     string    `json:"source"`
	Reason     string    `json:"reason"`
	Lease      string    `json:"lease,omitempty"`
	LeaseUntil time.Time `json:"lease_until,omitempty"`
}

func (a *Account) IsGeminiThirdPartyAPIKey() bool {
	return a != nil && a.Platform == PlatformGemini && a.Type == AccountTypeAPIKey && !isOfficialGeminiAPIBaseURL(a.GetCredential("base_url"))
}

func isOfficialGeminiAPIBaseURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(u.Scheme, "https") && strings.EqualFold(u.Hostname(), "generativelanguage.googleapis.com") &&
		(u.Port() == "" || u.Port() == "443") && strings.Trim(u.EscapedPath(), "/") == "" && !u.ForceQuery && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}

func geminiCooldownTarget(a *Account) string {
	if a == nil {
		return ""
	}
	proxy := int64(0)
	if a.ProxyID != nil {
		proxy = *a.ProxyID
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", strings.TrimRight(a.GetCredential("base_url"), "/"), a.GetCredential("api_key"), proxy)))
	return hex.EncodeToString(digest[:])
}

func geminiCooldownState(a *Account, scope string) (geminiCooldown, bool) {
	var state geminiCooldown
	if a == nil {
		return state, false
	}
	entries, _ := a.Extra[GeminiCooldownExtraKey].(map[string]any)
	raw, err := json.Marshal(entries[scope])
	if err != nil || json.Unmarshal(raw, &state) != nil || state.Generation == "" {
		return state, false
	}
	return state, true
}

func setGeminiCooldownState(a *Account, scope string, state geminiCooldown) {
	if a.Extra == nil {
		a.Extra = make(map[string]any)
	}
	entries, _ := a.Extra[GeminiCooldownExtraKey].(map[string]any)
	if entries == nil {
		entries = make(map[string]any)
		a.Extra[GeminiCooldownExtraKey] = entries
	}
	raw, _ := json.Marshal(state)
	var entry map[string]any
	_ = json.Unmarshal(raw, &entry)
	entries[scope] = entry
}

func geminiCooldownLimitMatches(a *Account, scope string, state geminiCooldown) bool {
	return a.modelRateLimitReason(scope) == geminiCooldownReasonPrefix+state.Generation
}

func removeGeminiCooldownState(a *Account, scope string, state geminiCooldown) {
	if geminiCooldownLimitMatches(a, scope, state) {
		limits, _ := a.Extra[modelRateLimitsKey].(map[string]any)
		delete(limits, scope)
	}
	entries, _ := a.Extra[GeminiCooldownExtraKey].(map[string]any)
	delete(entries, scope)
}

func geminiCooldownLimitObsolete(a *Account, scope string) bool {
	state, ok := geminiCooldownState(a, scope)
	return ok && geminiCooldownLimitMatches(a, scope, state) && state.Target != geminiCooldownTarget(a)
}

func geminiCooldownScopes(a *Account, model string) []string {
	mapped := strings.TrimSpace(a.GetMappedModel(model))
	if mapped == "" || mapped == GeminiAccountCooldownScope {
		return []string{GeminiAccountCooldownScope}
	}
	return []string{GeminiAccountCooldownScope, mapped}
}

func gemini429Scope(a *Account, model string, body []byte) (string, bool) {
	message := strings.ToLower(geminiErrorObject(body).Get("message").String())
	accountLimit := strings.Contains(message, "concurrency limit exceeded for account") || strings.Contains(message, "too many pending requests")
	if accountLimit || strings.TrimSpace(model) == "" {
		return GeminiAccountCooldownScope, accountLimit
	}
	return strings.TrimSpace(a.GetMappedModel(model)), false
}

func (s *GeminiMessagesCompatService) handleGeminiRelay429(ctx context.Context, account *Account, headers http.Header, body []byte, model string) {
	if !resilience.CollabAttemptFrom(ctx).AccountEffectsAllowed() {
		return
	}
	now := time.Now()
	hint, hinted := geminiResponseRecoveryHint(ctx, headers, body, now)
	scope, accountLimit := gemini429Scope(account, model, body)
	bare := !hinted && !accountLimit && !gemini429HasQuotaEvidence(body)
	request := resilience.StateFrom(ctx)
	if bare && !request.ClaimGeminiBare429Cooldown(account.ID) {
		return
	}
	cooldown := time.Duration(defaultRateLimit429CooldownSeconds) * time.Second
	if s.rateLimitService != nil {
		var enabled bool
		cooldown, enabled = s.rateLimitService.get429FallbackCooldown(ctx, account)
		if !hinted && !enabled {
			return
		}
	}
	target := geminiCooldownTarget(account)
	requestID := request.GatewayRequestID()
	if requestID == "" {
		requestID = uuid.NewString()
	}
	source := "fallback"
	if hinted {
		source = hint.Source
	}
	reason := "rate_limit"
	if accountLimit {
		reason = "account_concurrency"
	}
	var decision *geminiCooldown
	mutation := func(current *Account, clock time.Time) (bool, error) {
		if !current.IsGeminiThirdPartyAPIKey() || geminiCooldownTarget(current) != target || !current.IsActive() || !current.Schedulable || !current.ShouldHandleErrorCode(429) || (current.IsPoolMode() && !current.IsCustomErrorCodesEnabled()) {
			return false, nil
		}
		previous, exists := geminiCooldownState(current, scope)
		if exists && previous.Target == target && previous.RequestID == requestID && geminiCooldownLimitMatches(current, scope, previous) {
			return false, nil
		}
		streak := 1
		if exists && previous.Target == target && geminiCooldownLimitMatches(current, scope, previous) && clock.Sub(previous.ObservedAt) < 5*time.Minute {
			streak = min(previous.Streak+1, 7)
		}
		delay := cooldown
		if !hinted {
			limit := time.Minute
			if cooldown > limit {
				limit = cooldown
			}
			delay = min(cooldown*time.Duration(1<<(streak-1)), limit)
		}
		// Jitter applies only to subsequent failures and never advances a retry.
		if !hinted && streak > 1 {
			delay += time.Duration(rand.Float64() * 0.2 * float64(delay))
		}
		until := clock.Add(delay)
		if hinted {
			until = hint.NotBefore
		}
		if exists && previous.Target == target && geminiCooldownLimitMatches(current, scope, previous) && previous.Until.After(until) {
			until = previous.Until
		}
		// Active restrictions owned by another policy are never replaced.
		if old := current.modelRateLimitResetAt(scope); old != nil && old.After(clock) && (!exists || !geminiCooldownLimitMatches(current, scope, previous)) {
			return false, nil
		}
		state := geminiCooldown{Generation: uuid.NewString(), Target: target, ObservedAt: clock, Until: until, Streak: streak, RequestID: requestID, Source: source, Reason: reason}
		setGeminiCooldownState(current, scope, state)
		setGeminiCooldownLimit(current, scope, state, clock)
		decision = &state
		return true, nil
	}
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	if store, ok := s.accountRepo.(GeminiCooldownRepository); ok {
		if err := store.MutateGeminiCooldown(stateCtx, account.ID, mutation); err != nil {
			slog.Warn("gemini_cooldown_write_failed", "account_id", account.ID, "scope", scope, "error", err)
		} else if decision != nil {
			slog.Info("gemini_429_cooldown", "account_id", account.ID, "scope", scope, "reason", reason, "source", source, "until", decision.Until, "streak", decision.Streak, "generation", decision.Generation, "request_id", requestID)
		}
		return
	}
	// Repository adapters retain the same short, model-scoped fallback.
	until := now.Add(cooldown)
	if hinted {
		until = hint.NotBefore
	}
	if s.accountRepo != nil {
		if err := s.accountRepo.SetModelRateLimit(stateCtx, account.ID, scope, until, source); err != nil {
			slog.Warn("gemini_cooldown_write_failed", "account_id", account.ID, "error", err)
		}
	}
}

func gemini429HasQuotaEvidence(body []byte) bool {
	for _, detail := range geminiErrorObject(body).Get("details").Array() {
		if detail.Get("@type").String() == "type.googleapis.com/google.rpc.QuotaFailure" || detail.Get("reason").String() != "" {
			return true
		}
	}
	return false
}

// beginGeminiRecovery admits one post-cooldown request across all gateway nodes.
// Healthy accounts only read their row. Leases expire after an interrupted send.
func (s *GeminiMessagesCompatService) beginGeminiRecovery(ctx context.Context, a *Account, model string, inference bool) (func(bool), error) {
	noop := func(bool) {}
	store, ok := s.accountRepo.(GeminiCooldownRepository)
	if !ok || !a.IsGeminiThirdPartyAPIKey() || !inference {
		return noop, nil
	}
	target := geminiCooldownTarget(a)
	token := uuid.NewString()
	leased := false
	blocked := func() error {
		return &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests, RequestScopedTransient: true, Reason: GeminiRecoveryPendingReason, ResponseBody: []byte(`{"error":{"code":429,"message":"Upstream recovery is pending","status":"RESOURCE_EXHAUSTED"}}`)}
	}
	err := store.MutateGeminiCooldown(ctx, a.ID, func(current *Account, now time.Time) (bool, error) {
		if !current.IsGeminiThirdPartyAPIKey() || geminiCooldownTarget(current) != target || current.GetMappedModel(model) != a.GetMappedModel(model) || !current.IsSchedulableForModel(model) {
			return false, blocked()
		}
		changed := false
		for _, scope := range geminiCooldownScopes(current, model) {
			state, exists := geminiCooldownState(current, scope)
			if !exists {
				continue
			}
			if state.Target != target || !geminiCooldownLimitMatches(current, scope, state) {
				removeGeminiCooldownState(current, scope, state)
				changed = true
				continue
			}
			if state.Until.After(now) || state.LeaseUntil.After(now) {
				return false, blocked()
			}
			state.Lease, state.LeaseUntil = token, now.Add(geminiRecoveryLeaseDuration)
			leased = true
			setGeminiCooldownState(current, scope, state)
			changed = true
		}
		return changed, nil
	})
	if err != nil {
		return noop, err
	}
	if !leased {
		return noop, nil
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		defer close(done)
		ticker := time.NewTicker(geminiRecoveryLeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				renewCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				_ = store.MutateGeminiCooldown(renewCtx, a.ID, func(current *Account, now time.Time) (bool, error) {
					if geminiCooldownTarget(current) != target || !current.IsActive() || !current.Schedulable {
						return false, nil
					}
					changed := false
					for _, scope := range geminiCooldownScopes(current, model) {
						state, exists := geminiCooldownState(current, scope)
						if exists && state.Lease == token && state.LeaseUntil.After(now) && geminiCooldownLimitMatches(current, scope, state) {
							state.LeaseUntil = now.Add(geminiRecoveryLeaseDuration)
							setGeminiCooldownState(current, scope, state)
							changed = true
						}
					}
					return changed, nil
				})
				cancel()
			}
		}
	}()
	return func(success bool) {
		stopOnce.Do(func() { close(stop) })
		<-done
		finishCtx, cancel := resilienceStateContext(ctx)
		defer cancel()
		recovered := false
		err := store.MutateGeminiCooldown(finishCtx, a.ID, func(current *Account, now time.Time) (bool, error) {
			if geminiCooldownTarget(current) != target || !current.IsActive() || !current.Schedulable {
				return false, nil
			}
			changed := false
			for _, scope := range geminiCooldownScopes(current, model) {
				state, exists := geminiCooldownState(current, scope)
				if !exists || state.Target != target || state.Lease != token || !geminiCooldownLimitMatches(current, scope, state) {
					continue
				}
				if success && state.LeaseUntil.After(now) {
					removeGeminiCooldownState(current, scope, state)
					recovered = true
					changed = true
				} else if state.LeaseUntil.After(now) {
					// An interrupted probe retains a short pause instead of admitting a burst.
					state.Lease, state.LeaseUntil = "", time.Time{}
					state.Until = now.Add(10 * time.Second)
					setGeminiCooldownState(current, scope, state)
					setGeminiCooldownLimit(current, scope, state, now)
					changed = true
				}
			}
			return changed, nil
		})
		if err != nil {
			slog.Warn("gemini_recovery_finish_failed", "account_id", a.ID, "error", err)
		} else if recovered {
			slog.Info("gemini_cooldown_recovered", "account_id", a.ID, "model", model, "request_id", resilience.StateFrom(ctx).GatewayRequestID())
		}
	}, nil
}

// Each forwarding call owns its last response observation, even without a
// resilience RequestState. Re-parsing relative hints must not move their deadline.
type geminiRetryObservationKey struct{}
type geminiRetryObservation struct {
	body    string
	headers http.Header
	hint    resilience.UpstreamRecoveryHint
	valid   bool
}

func withGeminiRetryObservation(ctx context.Context) context.Context {
	return context.WithValue(ctx, geminiRetryObservationKey{}, &geminiRetryObservation{})
}

func geminiResponseRecoveryHint(ctx context.Context, headers http.Header, body []byte, now time.Time) (resilience.UpstreamRecoveryHint, bool) {
	if observation, _ := ctx.Value(geminiRetryObservationKey{}).(*geminiRetryObservation); observation != nil && observation.body == string(body) && reflect.DeepEqual(observation.headers, headers) {
		return observation.hint, observation.valid
	}
	return resilience.ParseUpstreamRecoveryHint(headers, body, now)
}

func geminiRetryFits(ctx context.Context, account *Account, headers http.Header, body []byte) bool {
	now := time.Now()
	hint, ok := resilience.ParseUpstreamRecoveryHint(headers, body, now)
	if observation, _ := ctx.Value(geminiRetryObservationKey{}).(*geminiRetryObservation); observation != nil {
		*observation = geminiRetryObservation{body: string(body), headers: headers.Clone(), hint: hint, valid: ok}
	}
	if !ok {
		return true
	}
	state := resilience.StateFrom(ctx)
	state.NoteAccountRetryNotBefore(account.ID, hint)
	const attemptReserve = 2 * time.Second
	if deadline, ok := ctx.Deadline(); ok && hint.NotBefore.Add(attemptReserve).After(deadline) {
		return false
	}
	if budget := state.Budget(); budget != nil {
		if remaining := budget.AdmissionRemaining(now, geminiRetryHasTimer(ctx)); remaining > 0 && hint.Delay+attemptReserve > remaining {
			return false
		}
	}
	// Long hints belong to the next request; the wait cap never truncates them.
	return hint.Delay <= time.Duration(resilience.Current().Budget.UpstreamRetryAfterCapSeconds)*time.Second
}

func sleepGeminiRetryCtx(ctx context.Context, account *Account, attempt int) bool {
	observation, _ := ctx.Value(geminiRetryObservationKey{}).(*geminiRetryObservation)
	if observation == nil || !observation.valid {
		return sleepGeminiBackoffCtx(ctx, attempt)
	}
	delay := time.Until(observation.hint.NotBefore)
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func geminiErrorObject(body []byte) gjson.Result {
	root := gjson.ParseBytes(body)
	if root.IsArray() && len(root.Array()) == 1 {
		root = root.Get("0")
	}
	if nested := root.Get("response.error"); nested.IsObject() {
		return nested
	}
	return root.Get("error")
}

func setGeminiCooldownLimit(a *Account, scope string, state geminiCooldown, now time.Time) {
	setAccountModelRateLimitSnapshot(a, scope, state.Until, geminiCooldownReasonPrefix+state.Generation, now)
	limits, _ := a.Extra[modelRateLimitsKey].(map[string]any)
	limit, _ := limits[scope].(map[string]any)
	if limit != nil {
		limit["rate_limit_reset_at"] = state.Until.UTC().Format(time.RFC3339Nano)
	}
}

// MergeGeminiCooldownExtra preserves runtime state from the locked database row
// during account edits. A stale edit cannot resurrect a cleared generation.
func MergeGeminiCooldownExtra(incoming, current map[string]any) {
	limits, _ := incoming[modelRateLimitsKey].(map[string]any)
	currentLimits, _ := current[modelRateLimitsKey].(map[string]any)
	scopes := make(map[string]bool)
	for _, extra := range []map[string]any{incoming, current} {
		states, _ := extra[GeminiCooldownExtraKey].(map[string]any)
		for scope := range states {
			scopes[scope] = true
		}
		entries, _ := extra[modelRateLimitsKey].(map[string]any)
		for scope, raw := range entries {
			limit, _ := raw.(map[string]any)
			reason, _ := limit["reason"].(string)
			if strings.HasPrefix(reason, geminiCooldownReasonPrefix) {
				scopes[scope] = true
			}
		}
	}
	delete(incoming, GeminiCooldownExtraKey)
	if states, ok := current[GeminiCooldownExtraKey]; ok {
		incoming[GeminiCooldownExtraKey] = states
	}
	for scope := range scopes {
		delete(limits, scope)
		if raw, ok := currentLimits[scope]; ok {
			if limits == nil {
				limits = make(map[string]any)
				incoming[modelRateLimitsKey] = limits
			}
			limits[scope] = raw
		}
	}
}
