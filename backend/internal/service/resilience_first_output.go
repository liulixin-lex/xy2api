package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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
	if b != nil && b.Attempts()+1 >= b.MaxAttempts() {
		return true
	}
	return Resilience().soleCandidate(ctx, st.GroupID(), accountID)
}

// soleCandidate reports whether the request's group has no other account
// that could take it after this one: every other account is inactive,
// unschedulable, of another platform, under a hard penalty (for the
// request's model too), does not serve the request's model or already
// failed in this request (review R07). Soft-penalised accounts still count,
// last-resort selection may use them. Cutting the only account short only
// turns a slow answer into an error (staging KA-6: a single-account
// Anthropic group lost a 150 s answer at the 90 s guard). Unknown (no
// group, lookup failed) counts as "not sole".
func (r *ResilienceService) soleCandidate(ctx context.Context, groupID, accountID int64) bool {
	if r == nil || r.fallbackAccounts == nil || groupID <= 0 || accountID <= 0 {
		return false
	}
	accounts, err := r.groupAccountsForLastResort(ctx, r.fallbackAccounts, groupID)
	if err != nil {
		return false
	}
	platform := ""
	for i := range accounts {
		if accounts[i].ID == accountID {
			platform = accounts[i].Platform
		}
	}
	if platform == "" {
		return false
	}
	st := resilience.StateFrom(ctx)
	model := st.Model()
	gate := resilienceOpenAIEligibility(ctx, groupID, model)
	now := time.Now()
	for i := range accounts {
		a := &accounts[i]
		if a.ID == accountID || a.Platform != platform || st.AccountFailed(a.ID) {
			continue
		}
		if model != "" && !a.IsModelSupported(model) {
			continue
		}
		if gate != nil && a.IsOpenAICompatible() && !gate.allows(a, true, false, now) {
			continue
		}
		if tier, _, _ := resiliencePenaltyOf(a, model, now); tier != resilience.TierHard {
			return false
		}
	}
	return true
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
// attempt, measured from attemptStart: under the request budget the effort
// tier (or model override) bounded by the budget left at attemptStart,
// otherwise the 0.3.0 configuration. The stream handler asks again when the
// upstream headers arrive; measuring the budget from that moment instead
// cut a slow-header attempt the instant its headers came (staging KA-1).
func (s *OpenAIGatewayService) openAIFirstOutputTimeoutFor(ctx context.Context, accountID int64, model, reasoningEffort string, attemptStart time.Time) time.Duration {
	b := resilienceTextBudget(ctx)
	if b == nil {
		return s.openAIFirstOutputTimeout(reasoningEffort)
	}
	profile := resilience.StateFrom(ctx).AttemptProfile()
	profileMatches := profile.AccountID == accountID && accountID > 0
	if profileMatches {
		model = profile.Model
	}
	tier := resilience.FirstOutputTier(resilience.Current(), model, reasoningEffort)
	// A missing or mismatched profile is cold, but has no size floor.
	if profileMatches {
		if profile.Warm {
			tier = warmFirstOutputTier(tier, profile.BodyBytes)
		} else {
			tier = coldFirstOutputTier(tier, profile.BodyBytes)
		}
	}
	if attemptStart.IsZero() {
		attemptStart = time.Now()
	}
	return b.AttemptTimeout(tier, resilienceLastCandidate(ctx, accountID), attemptStart)
}

func warmFirstOutputTier(tier time.Duration, bodyBytes int) time.Duration {
	if bodyBytes >= 512*1024 && tier < 40*time.Second {
		return 40 * time.Second
	}
	if bodyBytes >= 256*1024 && tier < 30*time.Second {
		return 30 * time.Second
	}
	return tier
}

func coldFirstOutputTier(tier time.Duration, bodyBytes int) time.Duration {
	if bodyBytes >= 256*1024 && tier < 75*time.Second {
		return 75 * time.Second
	}
	if bodyBytes >= 128*1024 && tier < 45*time.Second {
		return 45 * time.Second
	}
	return tier
}

// ReportOpenAIAttemptSuccess clears transient failure state using the current
// attempt start and samples the delivered first-content delay of warm streams.
// model is already the actual outbound canonical model.
func (s *OpenAIGatewayService) ReportOpenAIAttemptSuccess(ctx context.Context, account *Account, model string) {
	if s == nil || account == nil {
		return
	}
	canonical := strings.TrimSpace(model)
	start := time.Time{}
	if st := resilience.StateFrom(ctx); st != nil {
		profile := st.AttemptProfile()
		if profile.AccountID == account.ID {
			start = profile.AttemptStart
			if profile.Warm && profile.Stream {
				first := st.FirstOutput()
				if !first.IsZero() && first.After(profile.AttemptStart) {
					s.recordOpenAIAccountLatencySample(account, canonical, first.Sub(profile.AttemptStart), time.Now())
				}
			}
		}
	}
	s.clearOpenAIAccountModelTransientStateAt(account.ID, openAIAccountModelTransientModel(canonical), start)
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
	if platform == PlatformGemini {
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
	if account != nil && c != nil && c.Request != nil {
		resilience.Count(resilience.MetricFirstOutputTimeout, resilience.StateFrom(c.Request.Context()).GroupID(), account.ID, phase)
	}
	if account != nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			UpstreamStatusCode: http.StatusGatewayTimeout,
			Kind:               "first_output_timeout",
			Reason:             "first_output_timeout",
			Message:            "upstream produced no first event before the deadline",
			Detail:             fmt.Sprintf("phase=%s timeout=%s timeout_seconds=%g", phase, g.timeout, g.timeout.Seconds()),
		})
	}
	return &UpstreamFailoverError{
		StatusCode:   http.StatusGatewayTimeout,
		ResponseBody: []byte(`{"error":{"type":"first_output_timeout","message":"Upstream produced no output before the deadline"}}`),
	}
}
