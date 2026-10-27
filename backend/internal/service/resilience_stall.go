package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Stall detection of text streams (design §7.5).

// resilienceStallCheckBase returns the smallest threshold a text stream's
// stall detector must observe: the configured data interval and, when stall
// detection is on, the text stall threshold. Zero disables the detector.
func resilienceStallCheckBase(legacy time.Duration) time.Duration {
	cfg := resilience.Current()
	if !cfg.StallOn() {
		return legacy
	}
	text := time.Duration(cfg.Stall.TextStallSeconds) * time.Second
	if text > 0 && (legacy <= 0 || text < legacy) {
		return text
	}
	return legacy
}

// resilienceStallCheckPeriod is the tick period of a detector with the given
// base threshold. The 0.3.0 detector ticked once per threshold, so a stall
// was noticed up to twice the threshold late (G2); checking every
// min(T/4, 5 s) bounds the delay to 5 s.
func resilienceStallCheckPeriod(base time.Duration) time.Duration {
	if base <= 0 || !resilience.Current().StallOn() {
		return base
	}
	period := base / 4
	if period > 5*time.Second {
		period = 5 * time.Second
	}
	if period < 100*time.Millisecond {
		period = 100 * time.Millisecond
	}
	return period
}

// resilienceStallThreshold returns the idle time after which a text stream
// is stalled: once semantic output reached the client the text stall
// threshold applies (when smaller), before that the configured interval.
// Zero means no limit.
func resilienceStallThreshold(legacy time.Duration, outputStarted bool) time.Duration {
	if !outputStarted {
		return legacy
	}
	return resilienceStallCheckBase(legacy)
}

// noteResilienceStall puts a soft STALL cooldown on the account and counts
// the stall against the session (design §4.1). A stalled stream ends the
// request without reaching a failover loop; the one path that fails over
// on a stall before output (Grok) does not count the attempt again.
func noteResilienceStall(ctx context.Context, rl *RateLimitService, account *Account) {
	if account == nil || !resilience.Current().StallOn() {
		return
	}
	st := resilience.StateFrom(ctx)
	st.MarkAccountFailed(account.ID)
	// The 200 of a stalled stream must not end the conversation's failed-turn
	// streak (design §9.7, review R10).
	st.MarkTurnFailed()
	if !st.StreamFailureReported(account.ID) && st.ReportStreamFailure(account.ID, nil) {
		st.SessionFailure(ctx, account.ID, resilience.CategoryStall)
	}
	groupID := st.GroupID()
	resilience.Count(resilience.MetricStall, groupID, account.ID, account.Platform)
	slog.Warn("resilience.stall", "group_id", groupID, "account_id", account.ID, "platform", account.Platform)
	if rl == nil {
		return
	}
	rl.ApplyResilienceOutcome(ctx, account, resilience.Classify(resilience.Outcome{Source: resilience.SourceStall}, nil), "stream stalled")
}

// resilienceFirstOutputGuardOwnsStall reports whether a stalled OpenAI
// stream is left to its first-output guard. Before output that guard owns
// the attempt deadline and failover; stall detection takes over after
// output starts. With stall handling off the inherited order stays.
func resilienceFirstOutputGuardOwnsStall(guardArmed, outputStarted bool) bool {
	return guardArmed && !outputStarted && resilience.Current().StallOn()
}
