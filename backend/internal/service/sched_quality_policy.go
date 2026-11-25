package service

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
)

// ValidateSchedQualityPolicy validates the persisted v4 fields without normalizing them.
func ValidateSchedQualityPolicy(p schedquality.Policy) error {
	valid := func(v, lo, hi float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= lo && v <= hi }
	if math.IsNaN(p.TTFTSeconds) || math.IsInf(p.TTFTSeconds, 0) || p.TTFTSeconds <= 0 {
		return infraerrors.BadRequest("INVALID_SCHED_POLICY", "ttft_seconds must be positive and finite")
	}
	if math.IsNaN(p.MaxErrorRate) || math.IsInf(p.MaxErrorRate, 0) || p.MaxErrorRate > 1 {
		return infraerrors.BadRequest("INVALID_SCHED_POLICY", "max_error_rate must be finite and no greater than 1")
	}
	if p.MaxErrorRate < schedquality.ErrorRateFloor {
		return infraerrors.BadRequest("SCHED_ERROR_RATE_TOO_LOW", "max_error_rate must be at least 0.01")
	}
	if !valid(p.MinCacheRatio, 0, 1) {
		return infraerrors.BadRequest("INVALID_SCHED_POLICY", "min_cache_ratio must be between 0 and 1")
	}
	if !slices.Contains(schedquality.TakeoverIdleChoices[:], p.TakeoverIdleSeconds) {
		return infraerrors.BadRequest("INVALID_SCHED_POLICY", "invalid takeover_idle_seconds")
	}
	return nil
}

func (s *SchedQualityEngineService) Policy(ctx context.Context, groupID int64) (SchedPolicyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshConfig(ctx, s.now()); err != nil {
		return SchedPolicyRecord{}, err
	}
	if _, ok := s.members[groupID]; !ok {
		return SchedPolicyRecord{}, infraerrors.BadRequest("INVALID_SCHED_GROUP", "quality policies require an OpenAI group")
	}
	p, ok := s.policies[groupID]
	if !ok {
		p.Policy = schedquality.DefaultPolicy()
	}
	p.GroupID = groupID
	return p, nil
}

func relaxedPolicyLines(old, next schedquality.Policy) []schedquality.Dimension {
	var lines []schedquality.Dimension
	if next.TTFTSeconds > old.TTFTSeconds {
		lines = append(lines, schedquality.DimensionTTFO)
	}
	if next.MaxErrorRate > old.MaxErrorRate {
		lines = append(lines, schedquality.DimensionError)
	}
	if next.MinCacheRatio < old.MinCacheRatio {
		lines = append(lines, schedquality.DimensionCache)
	}
	return lines
}
func (s *SchedQualityEngineService) UpdatePolicy(ctx context.Context, groupID, expectedVersion int64, p schedquality.Policy) (result SchedPolicyUpdate, err error) {
	if err = ValidateSchedQualityPolicy(p); err != nil {
		return
	}
	s.mu.Lock()
	var notifications map[int64]time.Time
	defer func() { s.mu.Unlock(); s.notify(notifications) }()
	now := s.now()
	s.window.take(now)
	if err = s.loadEvidence(ctx, now); err != nil {
		return
	}
	s.configAt = time.Time{}
	d, e := s.inputs(ctx, now, s.window.samples)
	if e != nil {
		return result, e
	}
	oldPolicies := cloneSchedPolicies(s.policies)
	oldRule := s.rule
	var batch *schedReplayBatch
	var states []schedquality.LineState
	err = s.store.RunExclusive(ctx, func(tx SchedQualityTx) error {
		var e error
		batch, e = s.replayTx(ctx, tx, d)
		if e != nil {
			return e
		}
		states = batch.states
		result.RelaxedLines = relaxedPolicyLines(s.policies[groupID].Policy, p)
		result.SchedPolicyRecord, e = tx.SavePolicy(groupID, expectedVersion, p)
		if e != nil {
			return e
		}
		for group, record := range s.policies {
			record.Version = result.Version
			s.policies[group] = record
		}
		s.policies[groupID] = result.SchedPolicyRecord
		var rejudgeDecisions []schedquality.Decision
		states, result.Cleared, rejudgeDecisions, e = s.rejudge(tx, d, states, batch.resets, func(k schedquality.LineKey) bool {
			return k.GroupID == groupID && slices.Contains(result.RelaxedLines, k.Line)
		}, func(k schedquality.LineKey) bool { return k.GroupID == groupID && !p.Enabled })
		batch.decisions = append(batch.decisions, rejudgeDecisions...)
		return e
	})
	if err != nil {
		s.policies = oldPolicies
		s.rule = oldRule
		return
	}
	s.configAt = time.Time{}
	s.commitReplay(ctx, batch, states)
	notifications = s.publish(d, states, batch.overrides, batch.resets, now)
	return
}

func (s *SchedQualityEngineService) TTFTRule(ctx context.Context) (SchedTTFTRuleRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.store.LoadTTFTRule(ctx)
	return r, err
}
func (s *SchedQualityEngineService) UpdateTTFTRule(ctx context.Context, expectedVersion int64, p schedquality.RuleBParams) (result SchedTTFTRuleUpdate, err error) {
	if e := p.Validate(); e != nil {
		return result, infraerrors.BadRequest("INVALID_TTFT_RULE", e.Error())
	}
	s.mu.Lock()
	var notifications map[int64]time.Time
	defer func() { s.mu.Unlock(); s.notify(notifications) }()
	now := s.now()
	s.window.take(now)
	if err = s.loadEvidence(ctx, now); err != nil {
		return
	}
	s.configAt = time.Time{}
	d, e := s.inputs(ctx, now, s.window.samples)
	if e != nil {
		return result, e
	}
	oldRule := s.rule
	oldPolicies := cloneSchedPolicies(s.policies)
	var batch *schedReplayBatch
	var states []schedquality.LineState
	err = s.store.RunExclusive(ctx, func(tx SchedQualityTx) error {
		var e error
		batch, e = s.replayTx(ctx, tx, d)
		if e != nil {
			return e
		}
		states = batch.states
		result.Change, e = schedquality.CompareRuleBParams(s.rule.Params, p)
		if e != nil {
			return e
		}
		result.SchedTTFTRuleRecord, e = tx.SaveTTFTRule(expectedVersion, p)
		if e != nil {
			return e
		}
		s.rule = result.SchedTTFTRuleRecord
		if result.Change == schedquality.RuleBRelaxed || result.Change == schedquality.RuleBMixed {
			var rejudgeDecisions []schedquality.Decision
			states, result.Cleared, rejudgeDecisions, e = s.rejudge(tx, d, states, batch.resets, func(k schedquality.LineKey) bool { return k.Line == schedquality.DimensionTTFO }, func(schedquality.LineKey) bool { return false })
			batch.decisions = append(batch.decisions, rejudgeDecisions...)
		}
		return e
	})
	if err != nil {
		s.rule = oldRule
		s.policies = oldPolicies
		return
	}
	s.configAt = time.Time{}
	s.commitReplay(ctx, batch, states)
	notifications = s.publish(d, states, batch.overrides, batch.resets, now)
	return
}

// rejudge first clears every affected row, then freezes that entire cleared
// population, then evaluates. No intermediate configuration can be published.
func (s *SchedQualityEngineService) rejudge(tx SchedQualityTx, d *schedQualityData, states []schedquality.LineState, resets map[int64]time.Time, relax, remove func(schedquality.LineKey) bool) ([]schedquality.LineState, int, []schedquality.Decision, error) {
	keys, rows := s.rowKeys(d, states)
	old := make(map[schedquality.LineKey]schedquality.LineState)
	for _, r := range states {
		old[r.LineKey] = r
	}
	var deleted []schedquality.LineKey
	cleared := 0
	for _, r := range states {
		if remove(r.LineKey) || relax(r.LineKey) && (!s.isMember(r.GroupID, r.AccountID) || !s.policies[r.GroupID].Policy.Enabled) {
			deleted = append(deleted, r.LineKey)
			delete(rows, r.LineKey)
		} else if relax(r.LineKey) {
			cleared++
		}
	}
	for _, k := range keys {
		if remove(k) || !s.policies[k.GroupID].Policy.Enabled {
			continue
		}
		if relax(k) {
			relaxed := schedquality.Relaxed(k)
			// Rule 4.0 does not read LastRear. Preserve it so 0.6.14 can
			// apply its quiet-history clearing clock after rollback.
			relaxed.LastRear = rows[k].LastRear
			relaxed.LastAssessment = rows[k].LastAssessment
			rows[k] = relaxed
		}
	}
	var changed []schedquality.LineState
	var decisions []schedquality.Decision
	inputs := make(map[schedquality.LineKey]schedquality.Evaluation)
	var groups []schedQualityKey
	seen := make(map[schedQualityKey]bool)
	for _, k := range keys {
		if remove(k) || !relax(k) || !s.policies[k.GroupID].Policy.Enabled {
			continue
		}
		r := rows[k]
		r.LineKey = k
		rows[k] = r
		verdict, basis := s.assessLine(d, r, schedquality.EventRegular, d.now, resets)
		basis.Kind = "relax"
		inputs[k] = schedquality.Evaluation{Kind: schedquality.EventRegular, Verdict: verdict, Basis: basis}
		g := schedQualityKey{k.AccountID, k.Model, k.GroupID}
		if !seen[g] {
			seen[g] = true
			groups = append(groups, g)
		}
	}
	for _, g := range groups {
		var before []schedquality.LineState
		evaluations := make(map[schedquality.Dimension]schedquality.Evaluation)
		for _, k := range keys {
			if k.AccountID != g.account || k.Model != g.model || k.GroupID != g.group || remove(k) {
				continue
			}
			r := rows[k]
			r.LineKey = k
			before = append(before, r)
			if in, ok := inputs[k]; ok {
				evaluations[k.Line] = in
			}
		}
		after, actions := schedquality.StepGroup(before, evaluations, d.now)
		for i, next := range after {
			k, action := next.LineKey, actions[i]
			if _, ok := inputs[k]; !ok && action == "" {
				continue
			}
			rows[k] = next
			changed = append(changed, next)
			basis := inputs[k].Basis
			if next.Basis != nil {
				basis = *next.Basis
			}
			if relax(k) && (old[k].Identity == schedquality.IdentityRear || old[k].Trial) {
				decisions = append(decisions, schedquality.Decision{At: d.now, Key: k, Action: schedquality.ActionRelax, Basis: basis})
			}
			if action != "" {
				decisions = append(decisions, schedquality.Decision{At: d.now, Key: k, Action: action, Level: next.Level, Until: schedDecisionUntil(action, next), Basis: basis})
			}
		}
	}
	if err := tx.Delete(deleted); err != nil {
		return nil, 0, nil, err
	}
	if err := tx.Upsert(changed); err != nil {
		return nil, 0, nil, err
	}
	if err := tx.InsertDecisions(decisions); err != nil {
		return nil, 0, nil, err
	}
	out := make([]schedquality.LineState, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	return out, cleared, decisions, nil
}

func (s *SchedQualityEngineService) refreshConfig(ctx context.Context, now time.Time) error {
	if !s.configAt.IsZero() && now.Sub(s.configAt) < min(schedquality.MembersCacheTTL, schedquality.PoliciesCacheTTL) {
		return nil
	}
	policies, err := s.store.LoadPolicies(ctx)
	if err != nil {
		return err
	}
	members, err := s.store.LoadMembers(ctx)
	if err != nil {
		return err
	}
	rule, err := s.store.LoadTTFTRule(ctx)
	if err != nil {
		return err
	}
	s.policies, s.members, s.configAt, s.rule = policies, members, now, rule
	return nil
}

// PreviewPolicy reports current per-line verdicts without changing stored state.
func (s *SchedQualityEngineService) PreviewPolicy(ctx context.Context, group int64, p schedquality.Policy) ([]SchedPolicyChange, error) {
	if err := ValidateSchedQualityPolicy(p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	d, err := s.inputs(ctx, now, s.window.samples)
	if err != nil {
		return nil, err
	}
	if _, ok := s.members[group]; !ok {
		return nil, infraerrors.BadRequest("INVALID_SCHED_GROUP", "quality policies require an OpenAI group")
	}
	states, _, resets, err := s.store.LoadRows(ctx)
	if err != nil {
		return nil, err
	}
	keys, rows := s.rowKeys(d, states)
	old := s.policies[group]
	s.policies[group] = SchedPolicyRecord{GroupID: group, Policy: p}
	defer func() { s.policies[group] = old }()
	byPair := make(map[SchedAccountModel]*SchedPolicyChange)
	var out []SchedPolicyChange
	for _, k := range keys {
		if k.GroupID != group {
			continue
		}
		r := rows[k]
		r.LineKey = k
		pair := SchedAccountModel{k.AccountID, k.Model}
		change := byPair[pair]
		if change == nil {
			change = &SchedPolicyChange{AccountID: k.AccountID, Model: k.Model, Lines: make(map[schedquality.Dimension]schedquality.Verdict)}
			byPair[pair] = change
		}
		change.Lines[k.Line], _ = s.assessLine(d, r, schedquality.EventRegular, now, resets)
	}
	for _, k := range keys {
		pair := SchedAccountModel{k.AccountID, k.Model}
		if change := byPair[pair]; change != nil {
			out = append(out, *change)
			delete(byPair, pair)
		}
	}
	return out, nil
}
