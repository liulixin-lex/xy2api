package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// SetResilienceService attaches the resilience runtime (optional).
func (s *RateLimitService) SetResilienceService(r *ResilienceService) {
	if s != nil {
		s.resilience = r
	}
}

// resilienceBalanceEligible reports whether a balance failure pauses the
// account (design §3.2): API-key style accounts outside the providers that
// already have their own recoverable balance path.
func resilienceBalanceEligible(account *Account) bool {
	if account == nil {
		return false
	}
	if account.Type != AccountTypeAPIKey && account.Type != AccountTypeUpstream {
		return false
	}
	if account.IsCNProvider() || account.IsOpenCodeZen() || account.IsCommandCode() || account.IsCline() || account.IsOpenCodeGo() {
		return false
	}
	return account.Platform != PlatformAntigravity
}

// resilienceUniform403Eligible reports whether a non-balance 403 uses the
// counted soft cooldown instead of an immediate error (design §3.3).
func resilienceUniform403Eligible(account *Account) bool {
	if account == nil || !resilience.Current().FailureOn() {
		return false
	}
	if account.Type != AccountTypeAPIKey && account.Type != AccountTypeUpstream {
		return false
	}
	return account.Platform != PlatformAntigravity
}

// classifyUpstreamHTTP classifies an upstream HTTP error response.
func classifyUpstreamHTTP(statusCode int, headers http.Header, body []byte, upstreamMsg string) resilience.Result {
	cfg := resilience.Current()
	return resilience.Classify(resilience.Outcome{
		Source:     resilience.SourceHTTP,
		StatusCode: statusCode,
		Code:       extractUpstreamErrorCode(body),
		Type:       strings.TrimSpace(gjson.GetBytes(body, "error.type").String()),
		Message:    upstreamMsg,
		Body:       body,
		Headers:    headers,
	}, cfg.Failure.BalanceKeywordsExtra)
}

// resilienceHandleUpstreamError applies the resilience penalties to an
// upstream error response. It returns handled=true when the default status
// handling must be skipped. Rate limits, capacity shedding, credentials and
// client errors keep their existing handling, except content-safety refusals,
// which leave the account untouched.
func (s *RateLimitService) resilienceHandleUpstreamError(ctx context.Context, account *Account, statusCode int, headers http.Header, responseBody []byte, upstreamMsg string) (handled bool, shouldDisable bool) {
	cfg := resilience.Current()
	if !cfg.FailureOn() || account == nil || statusCode == 529 {
		return false, false
	}
	res := classifyUpstreamHTTP(statusCode, headers, responseBody, upstreamMsg)
	logUnclassified(account, statusCode, responseBody, upstreamMsg, res)
	switch res.Category {
	case resilience.CategoryBalance:
		if !cfg.BalanceOn() || !resilienceBalanceEligible(account) {
			return false, false
		}
		evidence := fmt.Sprintf("HTTP %d: %s", statusCode, firstNonEmpty(upstreamMsg, truncateForLog(responseBody, 200)))
		s.applyBalancePause(ctx, account, evidence)
		return true, true
	case resilience.CategoryTransient:
		if statusCode < 500 && statusCode != http.StatusRequestTimeout {
			return false, false
		}
		// OpenAI-family accounts keep their model-scoped transient block
		// (openai_account_model_transient.go). A relay that states how long
		// it is down (retry_after) is down for every model, so only then is
		// the account cooled down as a whole.
		if isOpenAIAccount(account) && (account.Type != AccountTypeAPIKey || res.RetryAfter <= 0) {
			return false, false
		}
		s.applyTransientCooldown(ctx, account, res, fmt.Sprintf("HTTP %d", statusCode))
		return false, false
	case resilience.CategoryClientError:
		// A content-safety refusal judges the request, not the account: it
		// takes no 403 strike, cooldown or error state.
		if res.ContentPolicy() {
			slog.Info("resilience.content_policy_refusal",
				"account_id", account.ID,
				"platform", account.Platform,
				"status", statusCode,
				"message", truncateString(upstreamMsg, 200),
			)
			return true, false
		}
	}
	return false, false
}

// resilienceHandleGrokUpstreamError routes balance-worded failures of API-key
// Grok accounts to the balance pause (design §3.2) before the Grok handler
// installs a quota-window rate limit from the relay's headers. In production
// one relay 429 "insufficient balance" locked a single-account Grok group for
// about 28 minutes. OAuth and pool-mode accounts keep the Grok handling. It
// reports whether the Grok handling must be skipped.
func (s *RateLimitService) resilienceHandleGrokUpstreamError(ctx context.Context, account *Account, statusCode int, headers http.Header, body []byte) bool {
	if s == nil || account == nil || account.IsGrokOAuth() || account.IsPoolMode() {
		return false
	}
	upstreamMsg := sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(body))
	if classifyUpstreamHTTP(statusCode, headers, body, upstreamMsg).Category != resilience.CategoryBalance {
		return false
	}
	handled, _ := s.resilienceHandleUpstreamError(ctx, account, statusCode, headers, body, upstreamMsg)
	return handled
}

// ApplyResilienceOutcome applies the account side of an outcome observed
// outside an HTTP error response: in-stream failures, first-output timeouts
// and stalls. It never changes failover or the client response.
func (s *RateLimitService) ApplyResilienceOutcome(ctx context.Context, account *Account, res resilience.Result, evidence string) {
	cfg := resilience.Current()
	if s == nil || account == nil || !cfg.FailureOn() {
		return
	}
	switch {
	case res.Category == resilience.CategoryBalance:
		if cfg.BalanceOn() && resilienceBalanceEligible(account) {
			s.applyBalancePause(ctx, account, evidence)
		}
	case res.Category.SoftCooldown():
		s.applyTransientCooldown(ctx, account, res, evidence)
	}
}

// applyTransientCooldown writes the soft transient cooldown: the configured
// default, or the upstream retry_after bounded by the cap (design §3.4).
func (s *RateLimitService) applyTransientCooldown(ctx context.Context, account *Account, res resilience.Result, evidence string) {
	s.applyTransientCooldownAs(ctx, account, res, string(res.Category), evidence)
}

// applyTransientCooldownAs writes the transient soft cooldown with the
// reason "transient:<label> <evidence>" and reports whether it was written.
func (s *RateLimitService) applyTransientCooldownAs(ctx context.Context, account *Account, res resilience.Result, label, evidence string) bool {
	if s == nil || s.accountRepo == nil || account == nil || account.IsPoolMode() {
		return false
	}
	cfg := resilience.Current()
	cooldown := time.Duration(cfg.Failure.TransientCooldownSeconds) * time.Second
	if res.RetryAfter > 0 {
		cooldown = res.RetryAfter
		if limit := time.Duration(cfg.Failure.RetryAfterCapSeconds) * time.Second; cooldown > limit {
			cooldown = limit
		}
	}
	if cooldown <= 0 {
		return false
	}
	until := time.Now().Add(cooldown)
	reason := resilience.ReasonPrefixTransient + label + " " + truncateString(evidence, 160)
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	written, err := s.writeTempPenalty(stateCtx, account.ID, until, reason)
	if err != nil {
		slog.Warn("resilience.transient_cooldown_failed", "account_id", account.ID, "error", err)
		return false
	}
	if !written {
		// An active hard pause or a longer soft cooldown stays.
		return false
	}
	// The next request's fallback decision and Retry-After must see the
	// cooldown, not a group snapshot taken before it.
	s.resilience.invalidateAccountGroups(account)
	slog.Info("resilience.transient_cooldown",
		"account_id", account.ID,
		"platform", account.Platform,
		"category", string(res.Category),
		"reason", res.Reason,
		"cooldown", cooldown.String(),
	)
	return true
}

// applyBalancePause pauses an account whose relay reported an exhausted
// balance for balance.pause_seconds (design §3.2). It is a hard penalty while
// it lasts; real traffic retries the account afterwards, and a balance
// failure then pauses it again for the same time. It replaces an active soft
// cooldown even when that one lasts longer (keeping the later end, review
// R01); a longer hard pause of another kind stays.
func (s *RateLimitService) applyBalancePause(ctx context.Context, account *Account, evidence string) {
	if s == nil || s.accountRepo == nil || account == nil {
		return
	}
	pause := time.Duration(resilience.Current().Balance.PauseSeconds) * time.Second
	until := time.Now().Add(pause)
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	s.notifyAccountSchedulingBlocked(account, until, "balance_exhausted")
	if _, err := s.writeTempPenalty(stateCtx, account.ID, until, resilience.BalancePauseReason(evidence)); err != nil {
		slog.Warn("resilience.balance_pause_failed", "account_id", account.ID, "error", err)
		return
	}
	s.resilience.invalidateAccountGroups(account)
	slog.Warn("resilience.balance_paused",
		"account_id", account.ID,
		"account_name", account.Name,
		"platform", account.Platform,
		"pause", pause.String(),
		"evidence", truncateString(evidence, 300),
	)
}

// handleCounted403 applies the OpenAI 403 policy (HTML/Cloudflare pages
// exempt, counted soft cooldowns, error on the third strike) with a
// platform-neutral reason.
func (s *RateLimitService) handleCounted403(ctx context.Context, account *Account, upstreamMsg string, responseBody []byte) bool {
	return s.handleCounted403WithReason(ctx, account, upstreamMsg, responseBody, resilience.Reason403Cooldown)
}

// ResiliencePenaltyStore is the tiered temp_unschedulable storage of the
// account repository (review R01/R02). Repositories without it (test stubs)
// keep the legacy writes: SetTempUnschedulable only extends, and the lift
// clears unconditionally.
type ResiliencePenaltyStore interface {
	SetTempUnschedulableTiered(ctx context.Context, id int64, until time.Time, reason string, hard bool, softPrefixes []string) (bool, error)
	ClearTempUnschedulableIf(ctx context.Context, id int64, reason string, until time.Time) (bool, error)
}

// writeTempPenalty writes a temp_unschedulable penalty by tier: a hard
// reason replaces an active soft cooldown of any length, a soft reason never
// replaces an active hard pause. It reports whether the pause was written.
func (s *RateLimitService) writeTempPenalty(ctx context.Context, accountID int64, until time.Time, reason string) (bool, error) {
	if store, ok := s.accountRepo.(ResiliencePenaltyStore); ok {
		hard := resilience.TempUnschedTier(reason) == resilience.TierHard
		return store.SetTempUnschedulableTiered(ctx, accountID, until, reason, hard, resilience.SoftReasonPrefixes())
	}
	return true, s.accountRepo.SetTempUnschedulable(ctx, accountID, until, reason)
}

// writeCounted403Cooldown writes the counted 403 cooldown (a soft penalty).
// Under the resilience layer it must not replace an active balance or other
// hard pause (review R01); with failure handling off it is the 0.3.0 write.
func (s *RateLimitService) writeCounted403Cooldown(ctx context.Context, accountID int64, until time.Time, reason string) error {
	if !resilience.Current().FailureOn() {
		return s.accountRepo.SetTempUnschedulable(ctx, accountID, until, reason)
	}
	_, err := s.writeTempPenalty(ctx, accountID, until, reason)
	return err
}

func resilienceStateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, openAIAccountStateUpdateTimeout)
}

// NoteAccountSuccess records a successful upstream request: it ends the
// session's failure streak and lifts the transient cooldown of an account
// that served as a last resort.
func (r *ResilienceService) NoteAccountSuccess(ctx context.Context, account *Account) {
	if r == nil || account == nil {
		return
	}
	// Usage of a stream that broke (stall, in-stream failure) is recorded
	// too; it must not lift cooldowns.
	if resilience.StateFrom(ctx).AccountFailed(account.ID) {
		return
	}
	now := time.Now()
	resilience.StateFrom(ctx).SessionSuccess(ctx, account.ID)
	if r.rateLimit == nil {
		return
	}
	// A last-resort account that served successfully has recovered: lift its
	// transient cooldown so normal selection uses it again (design §4.3).
	if v, ok := r.lastResortUsed.LoadAndDelete(account.ID); ok {
		if at, _ := v.(time.Time); now.Sub(at) < 10*time.Minute {
			r.rateLimit.liftTransientCooldown(ctx, account.ID)
		}
	}
}

// liftTransientCooldown clears a soft transient cooldown after the account
// served a request successfully; other pauses stay. Only the cooldown that
// was read is cleared: a pause written in between (balance, credentials, an
// administrator) stays (review R02).
func (s *RateLimitService) liftTransientCooldown(ctx context.Context, accountID int64) {
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	fresh, err := s.accountRepo.GetByID(stateCtx, accountID)
	if err != nil || fresh == nil || fresh.TempUnschedulableUntil == nil ||
		!strings.HasPrefix(fresh.TempUnschedulableReason, resilience.ReasonPrefixTransient) {
		return
	}
	if store, ok := s.accountRepo.(ResiliencePenaltyStore); ok {
		cleared, err := store.ClearTempUnschedulableIf(stateCtx, accountID, fresh.TempUnschedulableReason, *fresh.TempUnschedulableUntil)
		if err != nil {
			slog.Warn("resilience.transient_lift_failed", "account_id", accountID, "error", err)
			return
		}
		if !cleared {
			return
		}
	} else if err := s.accountRepo.ClearTempUnschedulable(stateCtx, accountID); err != nil {
		slog.Warn("resilience.transient_lift_failed", "account_id", accountID, "error", err)
		return
	}
	s.resilience.invalidateAccountGroups(fresh)
	slog.Info("resilience.transient_lifted", "account_id", accountID)
}

// invalidateAccountGroups drops the negative-cache entries and the cached
// account lists of the account's groups after its availability changed.
func (r *ResilienceService) invalidateAccountGroups(account *Account) {
	if r == nil || account == nil {
		return
	}
	if len(account.GroupIDs) == 0 {
		r.negCache.InvalidateGroup(0)
		r.lastResortGroups.drop(0)
		return
	}
	for _, gid := range account.GroupIDs {
		r.negCache.InvalidateGroup(gid)
		r.lastResortGroups.drop(gid)
	}
}

// ClassifyFailoverError classifies a failed upstream attempt reported to the
// handler failover loop.
func ClassifyFailoverError(e *UpstreamFailoverError) resilience.Result {
	if e == nil {
		return resilience.Result{Category: resilience.CategorySuccess}
	}
	if e.RequestScopedTransient {
		return resilience.Result{Category: resilience.CategoryCapacityShed, Reason: "request_scoped"}
	}
	if e.IsCredentialFailure() {
		return resilience.Result{Category: resilience.CategoryAuth, Reason: "credential_failure"}
	}
	msg := strings.TrimSpace(extractUpstreamErrorMessage(e.ResponseBody))
	return classifyUpstreamHTTP(e.StatusCode, e.ResponseHeaders, e.ResponseBody, msg)
}

// opsResilienceCategory classifies an upstream error event for the ops
// record (design §2, tasks 1.2): HTTP answers by status and body, failed
// stream events (status 200) by their code and message.
func opsResilienceCategory(ev *OpsUpstreamErrorEvent) string {
	if ev == nil || !resilience.Current().FailureOn() {
		return ""
	}
	body := []byte(ev.UpstreamResponseBody)
	switch {
	case ev.UpstreamStatusCode >= 400:
		return string(classifyUpstreamHTTP(ev.UpstreamStatusCode, nil, body, ev.Message).Category)
	case ev.UpstreamStatusCode == http.StatusOK && (len(body) > 0 || ev.Message != ""):
		// A Responses response.failed carries its error under "response".
		code := strings.TrimSpace(gjson.GetBytes(body, "response.error.code").String())
		if code == "" {
			code = extractUpstreamErrorCode(body)
		}
		return string(resilience.Classify(resilience.Outcome{
			Source:     resilience.SourceStreamEvent,
			StatusCode: http.StatusOK,
			Code:       code,
			Message:    ev.Message,
			Body:       body,
		}, resilience.Current().Failure.BalanceKeywordsExtra).Category)
	}
	return ""
}

// unclassifiedSampler limits the sampled log of upstream 4xx answers that no
// rule recognised (tasks 1.3) to about ten lines a minute per process.
var unclassifiedSampler = struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}{tokens: unclassifiedSampleBurst}

const (
	unclassifiedSampleBurst  = 10
	unclassifiedSamplePerSec = 10.0 / 60
)

func sampleUnclassified(now time.Time) bool {
	unclassifiedSampler.mu.Lock()
	defer unclassifiedSampler.mu.Unlock()
	if !unclassifiedSampler.last.IsZero() {
		unclassifiedSampler.tokens = min(unclassifiedSampleBurst,
			unclassifiedSampler.tokens+now.Sub(unclassifiedSampler.last).Seconds()*unclassifiedSamplePerSec)
	}
	unclassifiedSampler.last = now
	if unclassifiedSampler.tokens < 1 {
		return false
	}
	unclassifiedSampler.tokens--
	return true
}

// logUnclassified writes a scrubbed, length-limited sample of an upstream 4xx
// answer that only its status code classified, so new relay wordings can be
// added to failure.balance_keywords_extra or the built-in rules.
func logUnclassified(account *Account, statusCode int, body []byte, upstreamMsg string, res resilience.Result) {
	// 401 and 402 mean what their status says; a 403 classified by status
	// alone may be a balance answer in unknown words, so it is sampled.
	if account == nil || statusCode < 400 || statusCode >= 500 || statusCode == http.StatusUnauthorized ||
		statusCode == http.StatusPaymentRequired || !res.StatusOnly() || !sampleUnclassified(time.Now()) {
		return
	}
	message := SanitizeUpstreamErrorMessage(firstNonEmpty(upstreamMsg, ExtractUpstreamErrorMessage(body)))
	slog.Info("resilience.unclassified",
		"account_id", account.ID,
		"platform", account.Platform,
		"status", statusCode,
		"category", string(res.Category),
		"error_type", truncateString(strings.TrimSpace(gjson.GetBytes(body, "error.type").String()), 64),
		"error_code", truncateString(extractUpstreamErrorCode(body), 64),
		"message", truncateString(message, 300),
	)
}
