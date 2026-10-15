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

// weakBalanceSuccessWindow is how recent a success must be for balance
// wording on a 429 to count only as a weak signal (design §3.2).
const weakBalanceSuccessWindow = 30 * time.Second

// SetResilienceService attaches the resilience runtime (optional).
func (s *RateLimitService) SetResilienceService(r *ResilienceService) {
	if s != nil {
		s.resilience = r
	}
}

// resilienceBalanceEligible reports whether an account uses the recoverable
// balance state machine: API-key style accounts outside the providers that
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
// client errors keep their existing handling.
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
		weak := statusCode == http.StatusTooManyRequests && s.resilience.recentSuccess(account.ID, weakBalanceSuccessWindow)
		evidence := fmt.Sprintf("HTTP %d: %s", statusCode, firstNonEmpty(upstreamMsg, truncateForLog(responseBody, 200)))
		s.applyBalanceSignal(ctx, account, weak, evidence)
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
	}
	return false, false
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
			s.applyBalanceSignal(ctx, account, false, evidence)
		}
	case res.Category.SoftCooldown():
		s.applyTransientCooldown(ctx, account, res, evidence)
	}
}

// applyTransientCooldown writes the soft transient cooldown: the configured
// default, or the upstream retry_after bounded by the cap (design §3.4).
func (s *RateLimitService) applyTransientCooldown(ctx context.Context, account *Account, res resilience.Result, evidence string) {
	if s == nil || s.accountRepo == nil || account == nil || account.IsPoolMode() {
		return
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
		return
	}
	until := time.Now().Add(cooldown)
	reason := resilience.ReasonPrefixTransient + string(res.Category) + " " + truncateString(evidence, 160)
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	if err := s.accountRepo.SetTempUnschedulable(stateCtx, account.ID, until, reason); err != nil {
		slog.Warn("resilience.transient_cooldown_failed", "account_id", account.ID, "error", err)
		return
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
}

// applyBalanceSignal moves an account through the balance state machine
// after a balance failure of a real request.
func (s *RateLimitService) applyBalanceSignal(ctx context.Context, account *Account, weak bool, evidence string) {
	if s == nil || s.accountRepo == nil || account == nil {
		return
	}
	cfg := resilience.Current()
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	current := account
	if fresh, err := s.accountRepo.GetByID(stateCtx, account.ID); err == nil && fresh != nil {
		current = fresh
	}
	before := resilience.BalanceRecordFrom(current.Extra)
	now := time.Now()
	var after resilience.BalanceRecord
	if weak {
		after = resilience.OnWeakSignal(before, cfg, evidence, now)
	} else {
		after = resilience.OnStrongSignal(before, cfg, evidence, now)
	}
	s.persistBalanceRecord(stateCtx, current, before, after, "request")
}

// persistBalanceRecord stores a balance record and applies its scheduling
// effect: exclusion through temp_unschedulable for SUSPECT and EXHAUSTED,
// the concurrency cap for RECOVERING, and clearing only exclusions the
// balance machine wrote itself.
func (s *RateLimitService) persistBalanceRecord(ctx context.Context, account *Account, before, after resilience.BalanceRecord, source string) {
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, after.Extra()); err != nil {
		slog.Warn("resilience.balance_persist_failed", "account_id", account.ID, "error", err)
		return
	}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	for k, v := range after.Extra() {
		account.Extra[k] = v
	}
	cfg := resilience.Current()
	switch after.State {
	case resilience.BalanceExhausted, resilience.BalanceSuspect:
		until := after.ExclusionUntil(cfg)
		if after.State == resilience.BalanceExhausted {
			s.notifyAccountSchedulingBlocked(account, until, "balance_exhausted")
		}
		if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, after.TempUnschedReason()); err != nil {
			slog.Warn("resilience.balance_exclusion_failed", "account_id", account.ID, "error", err)
		}
		s.resilience.setRecovering(ctx, account.ID, time.Time{})
	case resilience.BalanceRecovering:
		s.clearBalanceExclusion(ctx, account)
		s.resilience.setRecovering(ctx, account.ID, after.RecoveringUntil)
	case resilience.BalanceOK:
		s.clearBalanceExclusion(ctx, account)
		s.resilience.setRecovering(ctx, account.ID, time.Time{})
	}
	if before.State != after.State {
		s.resilience.invalidateAccountGroups(account)
		slog.Warn("resilience.balance_transition",
			"account_id", account.ID,
			"account_name", account.Name,
			"platform", account.Platform,
			"from", string(before.State),
			"to", string(after.State),
			"level", after.Level,
			"next_check_at", after.NextCheckAt,
			"source", source,
			"evidence", truncateString(after.LastEvidence, 300),
		)
	}
}

// clearBalanceExclusion removes a temp_unschedulable written by the balance
// machine and leaves any other pause in place.
func (s *RateLimitService) clearBalanceExclusion(ctx context.Context, account *Account) {
	fresh, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || fresh == nil || fresh.TempUnschedulableUntil == nil {
		return
	}
	if !strings.HasPrefix(fresh.TempUnschedulableReason, "balance:") {
		return
	}
	if err := s.accountRepo.ClearTempUnschedulable(ctx, account.ID); err != nil {
		slog.Warn("resilience.balance_clear_failed", "account_id", account.ID, "error", err)
		return
	}
	s.notifyAccountSchedulingBlockCleared(account.ID)
}

// handleCounted403 applies the OpenAI 403 policy (HTML/Cloudflare pages
// exempt, counted soft cooldowns, error on the third strike) with a
// platform-neutral reason.
func (s *RateLimitService) handleCounted403(ctx context.Context, account *Account, upstreamMsg string, responseBody []byte) bool {
	return s.handleCounted403WithReason(ctx, account, upstreamMsg, responseBody, resilience.Reason403Cooldown)
}

func resilienceStateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, openAIAccountStateUpdateTimeout)
}

// resilienceSuccessTracker remembers the latest success per account for the
// weak balance signal. It is process local and best effort.
type resilienceSuccessTracker struct {
	last sync.Map // int64 -> time.Time
}

// NoteAccountSuccess records a successful upstream request. It feeds the
// weak balance signal and counts RECOVERING successes.
func (r *ResilienceService) NoteAccountSuccess(ctx context.Context, account *Account) {
	if r == nil || account == nil {
		return
	}
	now := time.Now()
	r.successes.last.Store(account.ID, now)
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
	if r.RecoveringCap(account.ID, now) == 0 {
		return
	}
	r.rateLimit.noteRecoveringSuccess(ctx, account.ID, now)
}

// liftTransientCooldown clears a soft transient cooldown after the account
// served a request successfully; other pauses stay.
func (s *RateLimitService) liftTransientCooldown(ctx context.Context, accountID int64) {
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	fresh, err := s.accountRepo.GetByID(stateCtx, accountID)
	if err != nil || fresh == nil || fresh.TempUnschedulableUntil == nil ||
		!strings.HasPrefix(fresh.TempUnschedulableReason, resilience.ReasonPrefixTransient) {
		return
	}
	if err := s.accountRepo.ClearTempUnschedulable(stateCtx, accountID); err != nil {
		slog.Warn("resilience.transient_lift_failed", "account_id", accountID, "error", err)
		return
	}
	s.resilience.invalidateAccountGroups(fresh)
	slog.Info("resilience.transient_lifted", "account_id", accountID)
}

func (r *ResilienceService) recentSuccess(accountID int64, within time.Duration) bool {
	if r == nil {
		return false
	}
	v, ok := r.successes.last.Load(accountID)
	if !ok {
		return false
	}
	t, _ := v.(time.Time)
	return time.Since(t) <= within
}

// noteRecoveringSuccess advances a RECOVERING account toward OK.
func (s *RateLimitService) noteRecoveringSuccess(ctx context.Context, accountID int64, now time.Time) {
	stateCtx, cancel := resilienceStateContext(ctx)
	defer cancel()
	account, err := s.accountRepo.GetByID(stateCtx, accountID)
	if err != nil || account == nil {
		return
	}
	before := resilience.BalanceRecordFrom(account.Extra)
	after, changed := resilience.OnSuccess(before, resilience.Current(), now)
	if !changed {
		return
	}
	s.persistBalanceRecord(stateCtx, account, before, after, "success")
}

// setRecovering publishes or clears the RECOVERING window of an account.
func (r *ResilienceService) setRecovering(ctx context.Context, accountID int64, until time.Time) {
	if r == nil {
		return
	}
	r.markRecoveringLocal(accountID, until)
	if r.store == nil {
		return
	}
	var err error
	if until.IsZero() {
		err = r.store.ClearRecovering(ctx, accountID)
	} else {
		err = r.store.SetRecovering(ctx, accountID, until)
	}
	if err != nil {
		slog.Warn("resilience.recovering_publish_failed", "account_id", accountID, "error", err)
	}
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
