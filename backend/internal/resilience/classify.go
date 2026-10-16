package resilience

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Category is the single classification of one upstream attempt.
type Category string

// Categories (design §2.2). Values are persisted in logs and statistics.
const (
	CategorySuccess            Category = "SUCCESS"
	CategoryBalance            Category = "BALANCE"
	CategoryAuth               Category = "AUTH"
	CategoryRateLimitReset     Category = "RATE_LIMIT_RESET"
	CategoryRateLimit          Category = "RATE_LIMIT"
	CategoryCapacityShed       Category = "CAPACITY_SHED"
	CategoryTransient          Category = "TRANSIENT"
	CategoryFirstOutputTimeout Category = "FIRST_OUTPUT_TIMEOUT"
	CategoryStall              Category = "STALL"
	CategoryClientCancel       Category = "CLIENT_CANCEL"
	CategoryClientError        Category = "CLIENT_ERROR"
	CategoryModelUnsupported   Category = "MODEL_UNSUPPORTED"
)

// HardRateLimitThreshold is the reset distance beyond which a rate limit is a
// hard state (design §3.1).
const HardRateLimitThreshold = 300 * time.Second

// Source is where an attempt outcome was observed.
type Source int

// Outcome sources.
const (
	SourceHTTP Source = iota
	SourceStreamEvent
	SourceNetwork
	SourceFirstOutputTimeout
	SourceStall
	SourceClientCancel
)

// Outcome describes how one upstream attempt ended.
type Outcome struct {
	Source Source
	// StatusCode is the upstream HTTP status. For in-stream failures it is the
	// HTTP status of the stream (normally 200).
	StatusCode int
	// Code, Type and Message are the structured upstream error fields when
	// known (error.code / error.type / error.message).
	Code    string
	Type    string
	Message string
	// Body is the raw error body or event payload. Only a bounded prefix is
	// inspected.
	Body    []byte
	Headers http.Header
	// Duration is how long the attempt took.
	Duration time.Duration
	// Err is the transport error for SourceNetwork.
	Err error
}

// Result is the classification of an Outcome.
type Result struct {
	Category Category
	// RetryAfter is the delay the upstream asked for, zero when none.
	RetryAfter time.Duration
	// Reason is a short stable machine reason such as "balance_keyword".
	Reason string
}

// StatusOnly reports whether no text, code or type rule matched and the
// category comes from the status code alone (design §2: such 4xx bodies are
// sampled so that new relay wordings can be added).
func (r Result) StatusOnly() bool { return strings.HasPrefix(r.Reason, "status_") }

// ReasonContentPolicy is the reason of a CLIENT_ERROR that is an upstream
// content-safety refusal of the request.
const ReasonContentPolicy = "content_policy"

// ContentPolicy reports an upstream content-safety refusal: the request
// content is refused, so the same request fails on this and every other
// account, and the account is not at fault.
func (r Result) ContentPolicy() bool { return r.Reason == ReasonContentPolicy }

// Hard reports whether the category puts the account in a hard state.
func (r Result) Hard() bool {
	switch r.Category {
	case CategoryBalance, CategoryAuth:
		return true
	case CategoryRateLimitReset:
		return r.RetryAfter > HardRateLimitThreshold
	}
	return false
}

// SwitchAccount reports whether another account may be tried.
func (c Category) SwitchAccount() bool {
	switch c {
	case CategoryBalance, CategoryAuth, CategoryRateLimitReset, CategoryRateLimit,
		CategoryCapacityShed, CategoryTransient, CategoryFirstOutputTimeout, CategoryModelUnsupported:
		return true
	}
	return false
}

// CountsForSession reports whether the category increments the session
// failure streak (design §4.1).
func (c Category) CountsForSession() bool {
	switch c {
	case CategoryRateLimit, CategoryTransient, CategoryFirstOutputTimeout, CategoryStall:
		return true
	}
	return false
}

// AccountScoped reports whether the failure is attributable to the account
// (and may therefore change account or session state).
func (c Category) AccountScoped() bool {
	switch c {
	case CategoryBalance, CategoryAuth, CategoryRateLimitReset, CategoryRateLimit,
		CategoryTransient, CategoryFirstOutputTimeout, CategoryStall, CategoryModelUnsupported:
		return true
	}
	return false
}

// SoftCooldown reports whether the category applies the transient soft
// cooldown (design §3.4).
func (c Category) SoftCooldown() bool {
	switch c {
	case CategoryTransient, CategoryFirstOutputTimeout, CategoryStall:
		return true
	}
	return false
}

// inspectLimit bounds the body prefix used for keyword and retry_after scans.
const inspectLimit = 8 << 10

var defaultBalanceCodes = map[string]struct{}{
	"insufficient_quota":             {},
	"insufficient_user_quota":        {},
	"pre_consume_token_quota_failed": {},
	"credit_balance_exhausted":       {},
	"insufficient_balance":           {},
	"insufficient_credit":            {},
	"insufficient_credits":           {},
	"billing_hard_limit_reached":     {},
}

// defaultBalanceKeywords are matched case-insensitively against the error
// message, code and type. They are deliberately specific: generic "quota
// exceeded" texts are per-minute rate limits on several platforms.
var defaultBalanceKeywords = []string{
	"insufficient balance",
	"insufficient account balance",
	"insufficient user balance",
	"insufficient_balance",
	"insufficient credit",
	"insufficient_credit",
	"insufficient funds",
	"credit balance",
	"usage balance exhausted",
	"balance is not enough",
	"no enough balance",
	"not enough balance",
	"balance exhausted",
	// new-api / one-api relays (token or user quota used up, pre-charge
	// refused); without these a 403 reads as an auth failure.
	"quota is not enough",
	"预扣费额度失败",
	"额度已用尽",
	"额度用尽",
	"余额不足",
	"额度不足",
	"账户余额不足",
	"账号余额不足",
}

var authCodes = map[string]struct{}{
	"invalid_api_key":        {},
	"token_revoked":          {},
	"token_invalidated":      {},
	"account_deactivated":    {},
	"deactivated_workspace":  {},
	"authentication_error":   {},
	"invalid_authentication": {},
	"usage_not_included":     {},
}

var clientErrorCodes = map[string]struct{}{
	"context_length_exceeded":  {},
	"string_above_max_length":  {},
	"invalid_prompt":           {},
	"cyber_policy":             {},
	"bio_policy":               {},
	"content_policy_violation": {},
	"content_filter":           {},
	"invalid_request_error":    {},
	"request_too_large":        {},
}

// contentPolicyCodes are the codes (or types) of an upstream content-safety
// refusal; they are checked before clientErrorCodes. cyber_policy and
// bio_policy keep their own handling.
var contentPolicyCodes = map[string]struct{}{
	"content_policy_violation": {},
	"content_filter":           {},
	"request_check_blocked":    {}, // Zhipu preflight safety check (403)
	"data_inspection_failed":   {}, // Qwen
}

// contentPolicyKeywords recognise a content-safety refusal by its wording on
// 400, 403 and 451 answers. They name a safety check or the content; words
// such as a bare "violation" or "敏感" also describe account bans or
// parameter errors ("大小写敏感") and are left out.
var contentPolicyKeywords = []string{
	"preflight safety",
	"safety check",
	"safety system",
	"content policy",
	"content_policy",
	"content filtering",
	"安全策略",
	"内容安全",
	"敏感内容",
	"敏感词",
	"敏感话题",
}

// contentPolicyAccountWords mark a refusal aimed at the account, such as a
// ban for policy violations; that stays an account failure.
var contentPolicyAccountWords = []string{
	"account", "organization", "suspend", "banned", "deactivat", "disabled",
	"账号", "账户", "帐号", "帐户", "封禁", "封号", "停用", "禁用",
}

var rateLimitCodes = map[string]struct{}{
	"rate_limit_exceeded": {},
	"rate_limit_error":    {},
	"slow_down":           {},
	"too_many_requests":   {},
	"usage_limit_reached": {},
}

var capacityCodes = map[string]struct{}{
	"server_is_overloaded": {},
	"overloaded_error":     {},
	"overloaded":           {},
}

var modelUnsupportedCodes = map[string]struct{}{
	"model_not_found":     {},
	"model_not_supported": {},
	"unsupported_model":   {},
}

var (
	reRetryAfterJSON = regexp.MustCompile(`(?i)"?retry[_-]after"?\s*[:=]\s*"?(\d+(?:\.\d+)?)`)
	reTryAgainIn     = regexp.MustCompile(`(?i)try again in\s+(\d+(?:\.\d+)?)\s*(ms|milliseconds?|s|secs?|seconds?|m|mins?|minutes?|h|hours?)\b`)
	// reTryAgainInCompound matches Go-style compound hints such as
	// "try again in 1m30.5s" or "1h2m" (OpenAI rate limits).
	reTryAgainInCompound = regexp.MustCompile(`(?i)try again in\s+((?:\d+(?:\.\d+)?(?:ms|h|m|s)){2,})\b`)
	reResetsInSeconds    = regexp.MustCompile(`(?i)"resets_in_seconds"\s*:\s*(\d+)`)
)

// Classify maps an outcome to one category. Text and structured codes are
// evaluated before the HTTP status (design §2.2), so balance signals wrapped
// in 400/401/403/429 responses are recognised. extraBalanceKeywords comes
// from failure.balance_keywords_extra.
// geminiQuotaLimit recognises Gemini's RESOURCE_EXHAUSTED quota errors.
func geminiQuotaLimit(text, lowerBody string) bool {
	for _, s := range []string{"resource_exhausted", "quotafailure", "rate-limits", "generativelanguage.googleapis.com"} {
		if strings.Contains(text, s) || strings.Contains(lowerBody, s) {
			return true
		}
	}
	return false
}

func Classify(o Outcome, extraBalanceKeywords []string) Result {
	switch o.Source {
	case SourceClientCancel:
		return Result{Category: CategoryClientCancel, Reason: "client_cancel"}
	case SourceFirstOutputTimeout:
		return Result{Category: CategoryFirstOutputTimeout, Reason: "first_output_timeout"}
	case SourceStall:
		return Result{Category: CategoryStall, Reason: "stall"}
	case SourceNetwork:
		if o.Err != nil && (errors.Is(o.Err, context.Canceled)) {
			return Result{Category: CategoryClientCancel, Reason: "context_canceled"}
		}
		return Result{Category: CategoryTransient, Reason: networkReason(o.Err)}
	}

	code := strings.ToLower(strings.TrimSpace(o.Code))
	typ := strings.ToLower(strings.TrimSpace(o.Type))
	msg := strings.ToLower(strings.TrimSpace(o.Message))
	body := o.Body
	if len(body) > inspectLimit {
		body = body[:inspectLimit]
	}
	lowerBody := strings.ToLower(string(body))
	// Keyword text: the structured fields, or the body when nothing was
	// parsed (HTML pages, plain-text relays).
	text := strings.Join([]string{code, typ, msg}, " ")
	if strings.TrimSpace(text) == "" {
		text = lowerBody
	}
	retryAfter := RetryAfterFrom(o.Headers, body)
	if retryAfter == 0 && o.Message != "" {
		retryAfter = RetryAfterFrom(nil, []byte(o.Message))
	}
	status := o.StatusCode
	isEvent := o.Source == SourceStreamEvent

	if status >= 200 && status < 300 && !isEvent && code == "" && typ == "" && msg == "" {
		return Result{Category: CategorySuccess}
	}

	// 1. Balance (text and codes first, then 402).
	if _, ok := defaultBalanceCodes[code]; ok {
		return Result{Category: CategoryBalance, Reason: "balance_code"}
	}
	if _, ok := defaultBalanceCodes[typ]; ok {
		return Result{Category: CategoryBalance, Reason: "balance_code"}
	}
	if containsAny(text, defaultBalanceKeywords) || containsAnyFold(text, extraBalanceKeywords) {
		return Result{Category: CategoryBalance, Reason: "balance_keyword"}
	}
	// OpenAI's balance wording; Gemini uses the same sentence for per-minute
	// RESOURCE_EXHAUSTED limits, which are rate limits.
	if strings.Contains(text, "exceeded your current quota") && !geminiQuotaLimit(text, lowerBody) {
		return Result{Category: CategoryBalance, Reason: "balance_keyword"}
	}
	if status == http.StatusPaymentRequired {
		if code == "deactivated_workspace" || strings.Contains(lowerBody, "deactivated_workspace") {
			return Result{Category: CategoryAuth, Reason: "deactivated_workspace"}
		}
		return Result{Category: CategoryBalance, Reason: "status_402"}
	}

	// The gateway's own first-event deadline (a 504 it synthesises).
	if code == "first_output_timeout" || typ == "first_output_timeout" {
		return Result{Category: CategoryFirstOutputTimeout, Reason: "first_output_timeout"}
	}

	// 2. Client errors and unsupported models carried by codes (also inside
	// streams).
	if contentPolicyCode(code, typ, lowerBody) {
		return Result{Category: CategoryClientError, Reason: ReasonContentPolicy}
	}
	if _, ok := clientErrorCodes[code]; ok {
		return Result{Category: CategoryClientError, Reason: "client_code"}
	}
	if _, ok := modelUnsupportedCodes[code]; ok {
		return Result{Category: CategoryModelUnsupported, Reason: "model_code"}
	}

	// 3. Authentication.
	if _, ok := authCodes[code]; ok {
		return Result{Category: CategoryAuth, Reason: "auth_code"}
	}
	if _, ok := authCodes[typ]; ok {
		return Result{Category: CategoryAuth, Reason: "auth_type"}
	}
	if status == http.StatusUnauthorized {
		return Result{Category: CategoryAuth, Reason: "status_401"}
	}
	if !isEvent && contentPolicyText(status, text, lowerBody) {
		return Result{Category: CategoryClientError, Reason: ReasonContentPolicy}
	}

	// 4. Capacity shedding is request scoped.
	if _, ok := capacityCodes[code]; ok {
		return Result{Category: CategoryCapacityShed, RetryAfter: retryAfter, Reason: "capacity_code"}
	}
	if _, ok := capacityCodes[typ]; ok {
		return Result{Category: CategoryCapacityShed, RetryAfter: retryAfter, Reason: "capacity_type"}
	}
	if status == 529 || strings.Contains(text, "overloaded") || strings.Contains(text, "at capacity") {
		return Result{Category: CategoryCapacityShed, RetryAfter: retryAfter, Reason: "capacity"}
	}

	// 5. Rate limits, with or without a reset time.
	rateLimited := status == http.StatusTooManyRequests
	if _, ok := rateLimitCodes[code]; ok {
		rateLimited = true
	}
	if _, ok := rateLimitCodes[typ]; ok {
		rateLimited = true
	}
	if !rateLimited && (strings.Contains(text, "rate limit") || strings.Contains(text, "too many requests") ||
		strings.Contains(text, "service is busy") || strings.Contains(text, "server is busy") || strings.Contains(text, "系统繁忙")) {
		rateLimited = true
	}
	if rateLimited {
		reset := resetFrom(o.Headers, body)
		if reset == 0 {
			reset = retryAfter
		}
		if reset > 0 {
			return Result{Category: CategoryRateLimitReset, RetryAfter: reset, Reason: "rate_limit_reset"}
		}
		return Result{Category: CategoryRateLimit, Reason: "rate_limit"}
	}

	// 6. Remaining client errors by status or type.
	if status == http.StatusNotFound {
		if strings.Contains(text, "model") {
			return Result{Category: CategoryModelUnsupported, Reason: "status_404_model"}
		}
		return Result{Category: CategoryTransient, RetryAfter: retryAfter, Reason: "status_404"}
	}
	if _, ok := clientErrorCodes[typ]; ok {
		return Result{Category: CategoryClientError, Reason: "client_type"}
	}
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return Result{Category: CategoryClientError, Reason: "status_" + strconv.Itoa(status)}
	case http.StatusForbidden:
		if looksLikeHTMLBlock(lowerBody) {
			return Result{Category: CategoryTransient, RetryAfter: retryAfter, Reason: "status_403_block_page"}
		}
		return Result{Category: CategoryAuth, Reason: "status_403"}
	}

	// 7. Server and relay errors.
	if status >= 500 || status == 408 {
		return Result{Category: CategoryTransient, RetryAfter: retryAfter, Reason: "status_" + strconv.Itoa(status)}
	}
	if isEvent {
		// A failed event without a recognised code is an upstream fault.
		return Result{Category: CategoryTransient, RetryAfter: retryAfter, Reason: "stream_failed"}
	}
	if status >= 400 {
		return Result{Category: CategoryClientError, Reason: "status_" + strconv.Itoa(status)}
	}
	return Result{Category: CategorySuccess}
}

func networkReason(err error) string {
	if err == nil {
		return "network"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "network_timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "network_timeout"
	}
	return "network"
}

func containsAny(text string, keywords []string) bool {
	for _, kw := range keywords {
		if kw != "" && strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

func containsAnyFold(text string, keywords []string) bool {
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// contentPolicyCode reports a content-safety refusal by its code or type.
// Relays that put the code at the top level ({"code":...}) are matched on
// the quoted value in the body.
func contentPolicyCode(code, typ, lowerBody string) bool {
	if _, ok := contentPolicyCodes[code]; ok {
		return true
	}
	if _, ok := contentPolicyCodes[typ]; ok {
		return true
	}
	return strings.Contains(lowerBody, `"request_check_blocked"`)
}

// contentPolicyText reports a content-safety refusal by its wording on a
// 400, 403 or 451 answer, unless the text speaks about the account.
func contentPolicyText(status int, text, lowerBody string) bool {
	switch status {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusUnavailableForLegalReasons:
	default:
		return false
	}
	return containsAny(text, contentPolicyKeywords) && !containsAny(text, contentPolicyAccountWords) &&
		!looksLikeHTMLBlock(lowerBody)
}

func looksLikeHTMLBlock(lowerBody string) bool {
	trimmed := strings.TrimSpace(lowerBody)
	return strings.HasPrefix(trimmed, "<!doctype") || strings.HasPrefix(trimmed, "<html") ||
		strings.Contains(lowerBody, "cf-ray") || strings.Contains(lowerBody, "attention required! | cloudflare") ||
		strings.Contains(lowerBody, "just a moment...")
}

// RetryAfterFrom extracts the delay an upstream asked for from response
// headers (Retry-After seconds or HTTP date, retry-after-ms) or the body
// (`"retry_after":60` in JSON or relay error pages, "try again in Ns").
// It returns zero when nothing was found.
func RetryAfterFrom(h http.Header, body []byte) time.Duration {
	if d := retryAfterHeader(h, time.Now()); d > 0 {
		return d
	}
	if len(body) == 0 {
		return 0
	}
	if len(body) > inspectLimit {
		body = body[:inspectLimit]
	}
	s := string(body)
	if m := reRetryAfterJSON.FindStringSubmatch(s); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > 0 {
			return secondsDuration(v)
		}
	}
	if m := reTryAgainInCompound.FindStringSubmatch(s); m != nil {
		if d, err := time.ParseDuration(strings.ToLower(m[1])); err == nil && d > 0 {
			return d
		}
	}
	if m := reTryAgainIn.FindStringSubmatch(s); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > 0 {
			return unitDuration(v, m[2])
		}
	}
	return 0
}

// resetFrom extracts a rate-limit reset distance: Retry-After, the body
// retry hints, or OpenAI resets_in_seconds.
func resetFrom(h http.Header, body []byte) time.Duration {
	if d := RetryAfterFrom(h, body); d > 0 {
		return d
	}
	if m := reResetsInSeconds.FindSubmatch(body); m != nil {
		if v, err := strconv.Atoi(string(m[1])); err == nil && v > 0 {
			return time.Duration(v) * time.Second
		}
	}
	return 0
}

func retryAfterHeader(h http.Header, now time.Time) time.Duration {
	if h == nil {
		return 0
	}
	if v := strings.TrimSpace(h.Get("retry-after-ms")); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs > 0 {
			return secondsDuration(secs)
		}
		return 0
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

func secondsDuration(v float64) time.Duration {
	return time.Duration(v * float64(time.Second))
}

func unitDuration(v float64, unit string) time.Duration {
	unit = strings.ToLower(unit)
	switch {
	case strings.HasPrefix(unit, "ms") || strings.HasPrefix(unit, "milli"):
		return time.Duration(v * float64(time.Millisecond))
	case strings.HasPrefix(unit, "h"):
		return time.Duration(v * float64(time.Hour))
	case strings.HasPrefix(unit, "m"):
		return time.Duration(v * float64(time.Minute))
	default:
		return secondsDuration(v)
	}
}
