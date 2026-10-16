package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// Upstream failures after output (design §7.6).
//
// Once semantic output reached the client an attempt cannot move to another
// account, so no failover loop sees it and, before this, nothing counted it:
// a session stayed bound to an account that kept failing mid-stream, and an
// account failing one stream in five kept all new traffic. Every stream
// branch that recognises an upstream failure after output reports it here,
// once per attempt:
//
//   - the session counts it as TRANSIENT toward its failure streak (§4.1),
//     which rebinds the session and avoids the account (§4.2). Wording that
//     is request scoped before output (overloaded, CAPACITY_SHED) says after
//     output that this account is unreliable right now;
//   - the account counts it in a per-process sliding window; three failures
//     within 60 s put it into the transient soft cooldown (§3.4), which only
//     skips the account and keeps it usable as a last resort (§4.3);
//   - the request is no success of the account (MarkAccountFailed), whatever
//     usage it reports.
//
// Client errors, content-policy refusals and client cancellations are no
// failures of the account and are not reported. Pool-mode accounts get no
// cooldown, as in HandleUpstreamError. With the master switch off nothing
// happens; with session handling off only the account side applies.

const (
	// postOutputCooldownThreshold failures after output within
	// postOutputCooldownWindow put the account into the transient cooldown.
	postOutputCooldownThreshold = 3
	postOutputCooldownWindow    = 60 * time.Second
	// postOutputTrackedAccounts bounds the per-process counters.
	postOutputTrackedAccounts = 4096
	// postOutputCooldownLabel completes the "transient:" cooldown reason.
	postOutputCooldownLabel = "POST_OUTPUT"
)

// Kinds of failures after output (metric label and log reason).
const (
	postOutputEvent      = "stream_failed"
	postOutputReadError  = "stream_read_error"
	postOutputNoTerminal = "missing_terminal"
)

// noteResiliencePostOutputFailure reports an upstream failure of account
// after output reached the client. attempt identifies the upstream attempt
// (see resilience.RequestState.ReportStreamFailure); later signals of the
// same attempt are ignored.
func noteResiliencePostOutputFailure(ctx context.Context, rl *RateLimitService, account *Account, attempt any, kind string) {
	if account == nil || !resilience.Current().FailureOn() {
		return
	}
	st := resilience.StateFrom(ctx)
	st.MarkAccountFailed(account.ID)
	if !st.ReportStreamFailure(account.ID, attempt) {
		return
	}
	st.SessionFailure(ctx, account.ID, resilience.CategoryTransient)
	// The 200 that carried the failure must not end the conversation's
	// failed-turn streak (design §9.7).
	st.MarkTurnFailed()
	groupID := st.GroupID()
	resilience.Count(resilience.MetricPostOutputFailure, groupID, account.ID, kind)
	failures, cooled := rl.notePostOutputFailure(ctx, account)
	slog.Warn("resilience.post_output_failure",
		"group_id", groupID,
		"account_id", account.ID,
		"platform", account.Platform,
		"reason", kind,
		"failures", failures,
		"cooldown", cooled,
	)
}

// notePostOutputFailure counts a failure after output of the account and
// applies the threshold cooldown. It returns the failures within the window
// (the new one included) and whether the cooldown was written.
func (s *RateLimitService) notePostOutputFailure(ctx context.Context, account *Account) (int, bool) {
	if s == nil || s.resilience == nil || account == nil || account.IsPoolMode() {
		return 0, false
	}
	n := s.resilience.postOutput.add(account.ID, time.Now())
	if n < postOutputCooldownThreshold {
		return n, false
	}
	res := resilience.Result{Category: resilience.CategoryTransient, Reason: "post_output"}
	evidence := fmt.Sprintf("%d failures after output within %s", n, postOutputCooldownWindow)
	return n, s.applyTransientCooldownAs(ctx, account, res, postOutputCooldownLabel, evidence)
}

// resiliencePostOutputCounts reports whether a classified failure after
// output is a failure of the account.
func resiliencePostOutputCounts(res resilience.Result) bool {
	return res.Category != resilience.CategoryClientError && res.Category != resilience.CategoryClientCancel
}

// openAIPostOutputEventCounts decides whether an OpenAI failure event
// (response.failed, error) after output is a failure of the account: it
// must be what would have switched accounts before output (not a cyber or
// content-policy refusal, an exceeded context or another request error) and
// carry no structured client error code.
func openAIPostOutputEventCounts(payload []byte, message string) bool {
	if !openAIStreamFailedEventShouldFailover(payload, message) {
		return false
	}
	if isOpenAITransientProcessingError(http.StatusBadRequest, message, payload) {
		return true
	}
	return resiliencePostOutputCounts(resilience.Classify(resilience.Outcome{
		Source:     resilience.SourceStreamEvent,
		StatusCode: http.StatusOK,
		Code:       openAIStreamFailedEventErrorCode(payload),
		Message:    message,
	}, nil))
}

// resilienceOpenAIFailedAfterOutput reports an OpenAI failure event that
// arrived after output (outputStarted) on one upstream attempt.
func (s *OpenAIGatewayService) resilienceOpenAIFailedAfterOutput(ctx context.Context, account *Account, attempt any, payload []byte, message string, outputStarted bool) {
	if s == nil || !outputStarted || account == nil || !openAIPostOutputEventCounts(payload, message) ||
		(account.Platform == PlatformGrok && isGrokContentPolicyRejection(http.StatusForbidden, payload)) {
		return
	}
	noteResiliencePostOutputFailure(ctx, s.rateLimitService, account, attempt, postOutputEvent)
}

// resilienceOpenAIWSFailedAfterOutput is resilienceOpenAIFailedAfterOutput
// for WebSocket turns, which see every upstream event; attempt is the turn's
// upstream response or, on an upstream WebSocket, the turn number. A bare
// error of an account whose upstream may still answer with
// response.completed or response.failed (deferBareError) waits for that
// terminal event.
func (s *OpenAIGatewayService) resilienceOpenAIWSFailedAfterOutput(ctx context.Context, account *Account, attempt any, eventType string, payload []byte, outputStarted, deferBareError bool) {
	switch eventType {
	case "response.failed":
	case "error":
		if deferBareError {
			return
		}
	default:
		return
	}
	s.resilienceOpenAIFailedAfterOutput(ctx, account, attempt, payload, extractOpenAISSEErrorMessage(payload), outputStarted)
}

// resilienceStreamBrokenAfterOutput reports an upstream stream that broke
// after output: a read error (cause) or its end without a terminal event
// (cause nil). Cancellation (the client left, the request context ended) is
// no failure of the account.
func resilienceStreamBrokenAfterOutput(ctx context.Context, rl *RateLimitService, account *Account, attempt any, cause error, outputStarted bool) {
	if !outputStarted || account == nil || (ctx != nil && ctx.Err() != nil) ||
		errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return
	}
	kind := postOutputNoTerminal
	if cause != nil {
		kind = postOutputReadError
	}
	noteResiliencePostOutputFailure(ctx, rl, account, attempt, kind)
}

// resilienceAnthropicFailedAfterOutput reports an Anthropic error event
// (event: error) that ended the stream after output reached the client; err
// is the stream handler's error and anything but such an event is ignored.
func (s *GatewayService) resilienceAnthropicFailedAfterOutput(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, err error) {
	var eventErr *sseStreamErrorEventError
	if s == nil || account == nil || !errors.As(err, &eventErr) || !openAIStreamClientOutputStarted(c, false) {
		return
	}
	body := []byte(eventErr.RawData)
	res := resilience.Classify(resilience.Outcome{
		Source:     resilience.SourceStreamEvent,
		StatusCode: http.StatusOK,
		Code:       extractUpstreamErrorCode(body),
		Type:       strings.TrimSpace(gjson.GetBytes(body, "error.type").String()),
		Message:    extractUpstreamErrorMessage(body),
		Body:       body,
	}, resilience.Current().Failure.BalanceKeywordsExtra)
	if !resiliencePostOutputCounts(res) {
		return
	}
	noteResiliencePostOutputFailure(ctx, s.rateLimitService, account, resp, postOutputEvent)
}

// postOutputTracker keeps the recent failures after output per account
// (process local, bounded).
type postOutputTracker struct {
	mu       sync.Mutex
	failures map[int64][]time.Time
}

// add records a failure of the account and returns the failures within the
// window, the new one included. Reaching the threshold resets the account.
func (t *postOutputTracker) add(accountID int64, now time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	recent, tracked := t.failures[accountID]
	if !tracked {
		if t.failures == nil {
			t.failures = map[int64][]time.Time{}
		}
		if len(t.failures) >= postOutputTrackedAccounts {
			t.sweepLocked(now)
		}
		if len(t.failures) >= postOutputTrackedAccounts {
			// Bounded memory: an account that finds no room counts alone.
			return 1
		}
	}
	kept := make([]time.Time, 0, postOutputCooldownThreshold)
	for _, at := range recent {
		if now.Sub(at) < postOutputCooldownWindow {
			kept = append(kept, at)
		}
	}
	kept = append(kept, now)
	if len(kept) >= postOutputCooldownThreshold {
		delete(t.failures, accountID)
		return len(kept)
	}
	t.failures[accountID] = kept
	return len(kept)
}

// sweep drops accounts without a failure inside the window.
func (t *postOutputTracker) sweep(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked(now)
}

func (t *postOutputTracker) sweepLocked(now time.Time) {
	for id, recent := range t.failures {
		if len(recent) == 0 || now.Sub(recent[len(recent)-1]) >= postOutputCooldownWindow {
			delete(t.failures, id)
		}
	}
}
