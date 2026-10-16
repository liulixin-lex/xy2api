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

// noteResilienceStall puts a soft STALL cooldown on the account.
func noteResilienceStall(ctx context.Context, rl *RateLimitService, account *Account) {
	if account == nil || !resilience.Current().StallOn() {
		return
	}
	st := resilience.StateFrom(ctx)
	st.MarkAccountFailed(account.ID)
	groupID := st.GroupID()
	resilience.Count(resilience.MetricStall, groupID, account.ID, account.Platform)
	slog.Warn("resilience.stall", "group_id", groupID, "account_id", account.ID, "platform", account.Platform)
	if rl == nil {
		return
	}
	rl.ApplyResilienceOutcome(ctx, account, resilience.Classify(resilience.Outcome{Source: resilience.SourceStall}, nil), "stream stalled")
}
