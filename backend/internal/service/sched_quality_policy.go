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
	if p.MaxErrorRate < .05 {
		return infraerrors.BadRequest("SCHED_ERROR_RATE_TOO_LOW", "max_error_rate must be at least 0.05")
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
	var states, previous []schedquality.LineState
	var overrides []SchedOverride
	var resets map[int64]time.Time
	err = s.store.RunExclusive(ctx, func(tx SchedQualityTx) error {
		// Read after acquiring the lock: another instance may have saved since refreshConfig.
		policies, e := s.store.LoadPolicies(ctx)
		if e != nil {
			return e
		}
		s.policies = policies
		rule, e := s.store.LoadTTFTRule(ctx)
		if e != nil {
			return e
		}
		s.rule = rule
		result.RelaxedLines = relaxedPolicyLines(policies[groupID].Policy, p)
		result.SchedPolicyRecord, e = tx.SavePolicy(groupID, expectedVersion, p)
		if e != nil {
			return e
		}
		for group, record := range s.policies {
			record.Version = result.Version
			s.policies[group] = record
		}
		s.policies[groupID] = result.SchedPolicyRecord
		states, e = tx.States()
		if e != nil {
			return e
		}
		previous = append([]schedquality.LineState(nil), states...)
		overrides, e = tx.Overrides()
		if e != nil {
			return e
		}
		resets, e = tx.ResetTimes()
		if e != nil {
			return e
		}
		states, result.Cleared, e = s.rejudge(tx, d, states, resets, func(k schedquality.LineKey) bool {
			return k.GroupID == groupID && slices.Contains(result.RelaxedLines, k.Line)
		}, func(k schedquality.LineKey) bool { return k.GroupID == groupID && !p.Enabled })
		return e
	})
	if err != nil {
		s.policies = oldPolicies
		s.rule = oldRule
		return
	}
	s.configAt = time.Time{}
	s.releaseEndedTrials(ctx, previous, states)
	notifications = s.publish(d, states, overrides, resets, now)
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
	var states, previous []schedquality.LineState
	var overrides []SchedOverride
	var resets map[int64]time.Time
	err = s.store.RunExclusive(ctx, func(tx SchedQualityTx) error {
		policies, e := s.store.LoadPolicies(ctx)
		if e != nil {
			return e
		}
		s.policies = policies
		old, e := s.store.LoadTTFTRule(ctx)
		if e != nil {
			return e
		}
		result.Change, e = schedquality.CompareRuleBParams(old.Params, p)
		if e != nil {
			return e
		}
		result.SchedTTFTRuleRecord, e = tx.SaveTTFTRule(expectedVersion, p)
		if e != nil {
			return e
		}
		s.rule = result.SchedTTFTRuleRecord
		states, e = tx.States()
		if e != nil {
			return e
		}
		previous = append([]schedquality.LineState(nil), states...)
		overrides, e = tx.Overrides()
		if e != nil {
			return e
		}
		resets, e = tx.ResetTimes()
		if e != nil {
			return e
		}
		if result.Change == schedquality.RuleBRelaxed || result.Change == schedquality.RuleBMixed {
			states, result.Cleared, e = s.rejudge(tx, d, states, resets, func(k schedquality.LineKey) bool { return k.Line == schedquality.DimensionTTFO }, func(schedquality.LineKey) bool { return false })
		}
		return e
	})
	if err != nil {
		s.rule = oldRule
		s.policies = oldPolicies
		return
	}
	s.configAt = time.Time{}
	s.releaseEndedTrials(ctx, previous, states)
	notifications = s.publish(d, states, overrides, resets, now)
	return
}

// rejudge first clears every affected row, then freezes that entire cleared
// population, then evaluates. No intermediate configuration can be published.
func (s *SchedQualityEngineService) rejudge(tx SchedQualityTx, d *schedQualityData, states []schedquality.LineState, resets map[int64]time.Time, relax, remove func(schedquality.LineKey) bool) ([]schedquality.LineState, int, error) {
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
			rows[k] = schedquality.Relaxed(k)
		}
	}
	peerRows := make(map[schedquality.LineKey]schedquality.LineState)
	for _, k := range keys {
		if !remove(k) && s.policies[k.GroupID].Policy.Enabled {
			r := rows[k]
			r.LineKey = k
			peerRows[k] = r
		}
	}
	d.displayPeers(d.now, peerRows)
	// Only relaxed TTFO keys may replace identities within an existing bucket;
	// error/cache changes keep all first-output peers frozen as they were.
	var clearedPeers map[schedquality.LineKey]schedquality.LineState
	for k, r := range peerRows {
		if k.Line == schedquality.DimensionTTFO && relax(k) {
			if clearedPeers == nil {
				clearedPeers = make(map[schedquality.LineKey]schedquality.LineState, len(d.peers.rows))
				for pk, pr := range d.peers.rows {
					clearedPeers[pk] = pr
				}
			}
			clearedPeers[k] = schedquality.LineState{LineKey: k, Identity: r.Identity, Epoch: r.Epoch}
		}
	}
	if clearedPeers != nil {
		d.peers = &peerBucket{at: d.peers.at, rows: clearedPeers}
		if _, frozen := d.peerBuckets[d.peers.at]; frozen {
			d.peerBuckets[d.peers.at] = d.peers
		}
	}
	var changed []schedquality.LineState
	var decisions []schedquality.Decision
	for _, k := range keys {
		if remove(k) || !relax(k) || !s.policies[k.GroupID].Policy.Enabled {
			continue
		}
		r := rows[k]
		r.LineKey = k
		verdict, basis := s.assessLine(d, r, schedquality.EventRegular, d.now, resets)
		basis.Kind = "relax"
		next, action := schedquality.Step(r, schedquality.EventRegular, verdict, basis, d.now)
		rows[k] = next
		changed = append(changed, next)
		if old[k].Identity == schedquality.IdentityRear || old[k].Trial {
			decisions = append(decisions, schedquality.Decision{At: d.now, Key: k, Action: schedquality.ActionRelax, Basis: basis})
		}
		if action != "" {
			decisions = append(decisions, schedquality.Decision{At: d.now, Key: k, Action: action, Level: next.Level, Until: schedDecisionUntil(action, next), Basis: basis})
		}
	}
	if err := tx.Delete(deleted); err != nil {
		return nil, 0, err
	}
	if err := tx.Upsert(changed); err != nil {
		return nil, 0, err
	}
	if err := tx.InsertDecisions(decisions); err != nil {
		return nil, 0, err
	}
	out := make([]schedquality.LineState, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	return out, cleared, nil
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
	d.displayPeers(now, rows)
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

func schedFailedLines(lines map[schedquality.Dimension]schedquality.Verdict) []schedquality.Dimension {
	var out []schedquality.Dimension
	for _, line := range schedquality.Lines {
		if lines[line] == schedquality.VerdictFail {
			out = append(out, line)
		}
	}
	return out
}
