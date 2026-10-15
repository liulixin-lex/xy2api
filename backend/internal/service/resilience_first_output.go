package service

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// First-output timeouts under the request budget (design §7.4).

// resilienceLastCandidate reports whether accountID is the request's last
// usable candidate: served as last resort, or the budget allows no further
// attempt after this one. The last candidate only answers to the pre-output
// deadline, never to a per-attempt timeout.
func resilienceLastCandidate(ctx context.Context, accountID int64) bool {
	st := resilience.StateFrom(ctx)
	if st.LastResort() == accountID && accountID > 0 {
		return true
	}
	b := st.Budget()
	return b != nil && b.Attempts()+1 >= b.MaxAttempts()
}

// resilienceTextBudget returns the request budget when it governs a text
// request, otherwise nil (switch off, images, WebSocket, internal calls).
func resilienceTextBudget(ctx context.Context) *resilience.Budget {
	if !resilience.Current().BudgetOn() {
		return nil
	}
	b := resilience.StateFrom(ctx).Budget()
	if b == nil || b.Kind() != resilience.KindText {
		return nil
	}
	return b
}

// openAIFirstOutputTimeoutFor returns the first-output timeout of an OpenAI
// attempt: under the request budget the effort tier (or model override)
// bounded by the remaining budget, otherwise the 0.3.0 configuration.
func (s *OpenAIGatewayService) openAIFirstOutputTimeoutFor(ctx context.Context, accountID int64, model, reasoningEffort string) time.Duration {
	b := resilienceTextBudget(ctx)
	if b == nil {
		return s.openAIFirstOutputTimeout(reasoningEffort)
	}
	tier := resilience.FirstOutputTier(resilience.Current(), model, reasoningEffort)
	return b.AttemptTimeout(tier, resilienceLastCandidate(ctx, accountID), time.Now())
}

// resilienceFirstEventTimeout returns the first-upstream-event timeout of a
// streaming Anthropic or Gemini attempt, or zero when none applies.
func resilienceFirstEventTimeout(ctx context.Context, accountID int64, platform string) time.Duration {
	b := resilienceTextBudget(ctx)
	if b == nil {
		return 0
	}
	cfg := resilience.Current()
	tier := time.Duration(cfg.Budget.AnthropicFirstEventTimeoutSeconds) * time.Second
	if platform == PlatformGemini || platform == PlatformAntigravity {
		tier = time.Duration(cfg.Budget.GeminiFirstEventTimeoutSeconds) * time.Second
	}
	return b.AttemptTimeout(tier, resilienceLastCandidate(ctx, accountID), time.Now())
}

// firstEventGuard cancels an upstream attempt that produced no first event
// before its deadline. The stream handler disarms it on the first line.
type firstEventGuard struct {
	timer   *time.Timer
	cancel  context.CancelFunc
	fired   atomic.Bool
	timeout time.Duration
}

type firstEventGuardKey struct{}

// newFirstEventGuard derives the attempt context; timeout <= 0 returns ctx
// unchanged and a nil (inert) guard. The caller must call Release when the
// attempt is over.
func newFirstEventGuard(ctx context.Context, timeout time.Duration) (context.Context, *firstEventGuard) {
	if timeout <= 0 {
		return ctx, nil
	}
	guarded, cancel := context.WithCancel(ctx)
	g := &firstEventGuard{cancel: cancel, timeout: timeout}
	g.timer = time.AfterFunc(timeout, func() {
		g.fired.Store(true)
		cancel()
	})
	return context.WithValue(guarded, firstEventGuardKey{}, g), g
}

func firstEventGuardFrom(ctx context.Context) *firstEventGuard {
	if ctx == nil {
		return nil
	}
	g, _ := ctx.Value(firstEventGuardKey{}).(*firstEventGuard)
	return g
}

// Disarm stops the deadline once the first upstream event arrived.
func (g *firstEventGuard) Disarm() {
	if g != nil {
		g.timer.Stop()
	}
}

// Fired reports whether the deadline cancelled the attempt.
func (g *firstEventGuard) Fired() bool { return g != nil && g.fired.Load() }

// Release frees the guard at the end of the attempt.
func (g *firstEventGuard) Release() {
	if g != nil {
		g.timer.Stop()
		g.cancel()
	}
}

// failoverError is the failover error of a fired guard; it records the
// timeout as an upstream attempt for the ops views.
func (g *firstEventGuard) failoverError(c *gin.Context, account *Account, phase string) *UpstreamFailoverError {
	if account != nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			UpstreamStatusCode: http.StatusGatewayTimeout,
			Kind:               "first_output_timeout",
			Message:            "upstream produced no first event before the deadline",
			Detail:             "phase=" + phase + " timeout=" + g.timeout.String(),
		})
	}
	return &UpstreamFailoverError{
		StatusCode:   http.StatusGatewayTimeout,
		ResponseBody: []byte(`{"error":{"type":"first_output_timeout","message":"Upstream produced no output before the deadline"}}`),
	}
}
