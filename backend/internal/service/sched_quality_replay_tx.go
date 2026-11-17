package service

import (
	"context"
	"log/slog"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type schedReplayBatch struct {
	previous, states []q.LineState
	overrides        []SchedOverride
	resets           map[int64]time.Time
	decisions        []q.Decision
	horizon          time.Time
	stats            schedReplayStats
}

// replayTx is shared by periodic evaluation and both configuration saves. All
// reads and writes use the caller's locked transaction and its old configuration.
// Only the caller's successful commit may publish buckets, watermarks or logs.
func (s *SchedQualityEngineService) replayTx(ctx context.Context, tx SchedQualityTx, d *schedQualityData) (*schedReplayBatch, error) {
	policies, err := tx.Policies()
	if err != nil {
		return nil, err
	}
	rule, err := tx.TTFTRule()
	if err != nil {
		return nil, err
	}
	s.policies, s.rule = policies, rule
	b := &schedReplayBatch{horizon: d.now.Add(-10 * time.Second)}
	if b.previous, err = tx.States(); err != nil {
		return nil, err
	}
	if b.overrides, err = tx.Overrides(); err != nil {
		return nil, err
	}
	if b.resets, err = tx.ResetTimes(); err != nil {
		return nil, err
	}
	after := s.processedAt
	if after.IsZero() {
		after = d.now.Add(-q.RegularInterval)
	} else if late := s.window.newFinishedAt; !late.IsZero() && !late.After(after) {
		after = schedQualitySince(late.Add(-time.Microsecond), d.now.Add(-q.RegularInterval))
	}
	var changed []q.LineState
	var deleted []q.LineKey
	b.states, changed, deleted, b.decisions, err = s.replay(ctx, d, b.previous, b.resets, after, b.horizon)
	if err != nil {
		return nil, err
	}
	b.stats = d.replayStats
	if err = tx.Upsert(changed); err != nil {
		return nil, err
	}
	if err = tx.Delete(deleted); err != nil {
		return nil, err
	}
	if err = tx.InsertDecisions(b.decisions); err != nil {
		return nil, err
	}
	var expired, active []SchedOverride
	for _, o := range b.overrides {
		if (o.ExpiresAt != nil && !o.ExpiresAt.After(d.now)) || o.GroupID != 0 && !s.isMember(o.GroupID, o.AccountID) {
			expired = append(expired, o)
		} else {
			active = append(active, o)
		}
	}
	if err = tx.DeleteOverrides(expired); err != nil {
		return nil, err
	}
	b.overrides = active
	return b, nil
}

func (s *SchedQualityEngineService) commitReplay(ctx context.Context, b *schedReplayBatch, final []q.LineState) {
	slog.DebugContext(ctx, "sched_quality.replay", "component", "sched_quality", "horizon", b.horizon, "batches", b.stats.batches, "assessments", b.stats.assessments)
	s.processedAt = b.horizon
	s.window.newFinishedAt = time.Time{}
	s.releaseEndedTrials(ctx, b.previous, final)
	for _, decision := range b.decisions {
		LogSchedQualityDecision(ctx, decision)
		s.countDecision(decision.Action)
	}
}
