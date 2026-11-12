package service

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type schedQualityKey struct {
	account int64
	model   string
	group   int64
}
type schedQualitySummaryKey struct {
	pair  SchedAccountModel
	since time.Time
}
type schedQualityData struct {
	now                         time.Time
	samples                     map[SchedAccountModel][]schedquality.AttemptSample
	tests                       map[SchedAccountModel][]SchedTestRecord
	finished                    map[SchedAccountModel][]schedquality.TestResult
	summaries                   map[schedQualitySummaryKey]schedquality.Summary
	models                      map[int64]map[string]struct{}
	globalEnabled, testsEnabled bool
}

func (d *schedQualityData) summary(pair SchedAccountModel, since time.Time) schedquality.Summary {
	k := schedQualitySummaryKey{pair, since}
	if v, ok := d.summaries[k]; ok {
		return v
	}
	v := schedquality.Summarize(d.samples[pair], since, d.now)
	d.summaries[k] = v
	return v
}

type schedQualityView struct {
	version       int64
	at            time.Time
	globalEnabled bool
	states        map[schedQualityKey]schedquality.StateView
	policies      map[int64]SchedPolicyRecord
	overrides     map[int64][]SchedOverride
}

func (v *schedQualityView) Version() int64      { return v.version }
func (v *schedQualityView) At() time.Time       { return v.at }
func (v *schedQualityView) GlobalEnabled() bool { return v.globalEnabled }
func (v *schedQualityView) StateOf(g, a int64, m string) schedquality.StateView {
	state := v.states[schedQualityKey{a, m, g}]
	state.GroupOverride = overrideRef(v.overrides[a], g)
	state.AllOverride = overrideRef(v.overrides[a], 0)
	return state
}
func (v *schedQualityView) TakeoverIdle(g int64) (time.Duration, bool) {
	p, ok := v.policies[g]
	if !ok {
		return 0, true
	}
	if p.Policy.TakeoverIdleSeconds < 0 {
		return 0, false
	}
	return time.Duration(p.Policy.TakeoverIdleSeconds) * time.Second, true
}

func (s *SchedQualityEngineService) publish(d *schedQualityData, states []schedquality.StateRow, overrides []SchedOverride, resets map[int64]time.Time, evaluatedAt time.Time) map[int64]time.Time {
	old := s.snapshot.Load()
	version := int64(1)
	if old != nil {
		version = old.Version + 1
	}
	active := make(map[int64][]SchedOverride)
	copiedOverrides := make([]SchedOverride, len(overrides))
	for i, o := range overrides {
		if o.ExpiresAt != nil {
			at := *o.ExpiresAt
			o.ExpiresAt = &at
		}
		copiedOverrides[i] = o
		active[o.AccountID] = append(active[o.AccountID], o)
	}
	keys, rows := s.rowKeys(d, states)
	view := &schedQualityView{version: version, at: evaluatedAt, globalEnabled: d.globalEnabled, states: make(map[schedQualityKey]schedquality.StateView), policies: cloneSchedPolicies(s.policies), overrides: active}
	evidence := make(map[SchedAccountModel]schedquality.Summary)
	for pair := range d.samples {
		evidence[pair] = d.summary(pair, resets[pair.AccountID])
	}
	latestTests := make(map[SchedAccountModel]SchedTestRecord)
	for pair, tests := range d.tests {
		evidence[pair] = d.summary(pair, resets[pair.AccountID])
		for _, test := range tests {
			if previous, ok := latestTests[pair]; !ok || test.StartedAt.After(previous.StartedAt) {
				latestTests[pair] = test
			}
		}
	}
	groupRows := make(map[int64][]SchedGroupRow)
	for _, key := range keys {
		pair := SchedAccountModel{key.account, key.model}
		row, exists := rows[key]
		policy := s.policies[key.group].Policy
		summary := d.summary(pair, schedQualitySince(resets[key.account], row.Since))
		evidence[pair] = d.summary(pair, resets[key.account])
		effective, fromTest := schedquality.Effective(schedquality.Judge(summary, policy), d.finished[pair], key.model, policy, resets[key.account], row.Since, d.now)
		state := schedquality.StateView{State: row.State, Level: row.Level, Reason: row.Reason, Since: row.Since, Until: row.Until, Verdicts: effective, FromTest: fromTest, GroupOverride: overrideRef(active[key.account], key.group), AllOverride: overrideRef(active[key.account], 0)}
		if exists {
			if row.State == schedquality.StateCooling && !d.now.Before(row.Until) {
				_, _, _, state.WaitingTest = schedquality.Next(row, schedquality.Inputs{Policy: policy, Summary: summary, Tests: d.finished[pair], TestsEnabled: d.testsEnabled, ResetAt: resets[key.account]}, d.now)
			}
			view.states[key] = state
		}
		testSummary := schedquality.SummarizeTests(d.finished[pair], key.model, policy, resets[key.account], row.Since, d.now)
		groupRows[key.account] = append(groupRows[key.account], SchedGroupRow{GroupID: key.group, Model: key.model, View: state, Summary: summary, Test: testSummary})
	}
	for id := range groupRows {
		sort.Slice(groupRows[id], func(i, j int) bool {
			a, b := groupRows[id][i], groupRows[id][j]
			if a.GroupID != b.GroupID {
				return a.GroupID < b.GroupID
			}
			return a.Model < b.Model
		})
	}
	members := make(map[int64][]int64)
	for group, accounts := range s.members {
		for _, account := range accounts {
			members[account] = append(members[account], group)
		}
	}
	for account := range members {
		sort.Slice(members[account], func(i, j int) bool { return members[account][i] < members[account][j] })
	}
	s.snapshot.Store(&SchedQualitySnapshot{Version: version, At: evaluatedAt, GlobalEnabled: d.globalEnabled, TestsEnabled: d.testsEnabled, View: view, Evidence: evidence, Tests: latestTests, GroupRows: groupRows, Members: members, Policies: cloneSchedPolicies(s.policies), Overrides: copiedOverrides, ResetAt: cloneResetTimes(resets)})
	newer := make(map[int64]time.Time)
	for id, at := range resets {
		if at.After(s.seenResets[id]) {
			if old != nil && old.Version > 0 {
				newer[id] = at
			}
			s.seenResets[id] = at
		}
	}
	return newer
}

func cloneResetTimes(src map[int64]time.Time) map[int64]time.Time {
	out := make(map[int64]time.Time, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
func cloneSchedPolicies(src map[int64]SchedPolicyRecord) map[int64]SchedPolicyRecord {
	out := make(map[int64]SchedPolicyRecord, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
func overrideRef(rows []SchedOverride, g int64) schedquality.OverrideRef {
	for _, o := range rows {
		if o.GroupID == g {
			ref := schedquality.OverrideRef{Mode: o.Mode}
			if o.ExpiresAt != nil {
				ref.Expires = *o.ExpiresAt
			}
			return ref
		}
	}
	return schedquality.OverrideRef{}
}
func schedQualitySince(reset, since time.Time) time.Time {
	if reset.After(since) {
		return reset
	}
	return since
}

func (s *SchedQualityEngineService) rowKeys(d *schedQualityData, rows []schedquality.StateRow) ([]schedQualityKey, map[schedQualityKey]schedquality.StateRow) {
	if d.models == nil {
		d.models = make(map[int64]map[string]struct{})
		add := func(pair SchedAccountModel) {
			if d.models[pair.AccountID] == nil {
				d.models[pair.AccountID] = make(map[string]struct{})
			}
			d.models[pair.AccountID][pair.Model] = struct{}{}
		}
		for pair, samples := range d.samples {
			if len(samples) > 0 {
				add(pair)
			}
		}
		for pair, tests := range d.tests {
			if len(tests) > 0 {
				add(pair)
			}
		}
	}
	previous := make(map[schedQualityKey]schedquality.StateRow, len(rows))
	keys := make(map[schedQualityKey]bool)
	for _, r := range rows {
		k := schedQualityKey{r.AccountID, r.Model, r.GroupID}
		previous[k] = r
		keys[k] = true
	}
	for group, accounts := range s.members {
		for _, account := range accounts {
			for model := range d.models[account] {
				keys[schedQualityKey{account, model, group}] = true
			}
		}
	}
	out := make([]schedQualityKey, 0, len(keys))
	for k := range keys {
		if s.isMember(k.group, k.account) {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return schedQualityLess(out[i], out[j]) })
	return out, previous
}
func schedQualityKeyOf(r schedquality.StateRow) schedQualityKey {
	return schedQualityKey{r.AccountID, r.Model, r.GroupID}
}
func schedQualityLess(a, b schedQualityKey) bool {
	if a.account != b.account {
		return a.account < b.account
	}
	if a.model != b.model {
		return a.model < b.model
	}
	return a.group < b.group
}
func (s *SchedQualityEngineService) isMember(group, account int64) bool {
	for _, id := range s.members[group] {
		if id == account {
			return true
		}
	}
	return false
}

type schedQualityTransition struct {
	from, to schedquality.StateRow
	summary  schedquality.Summary
	verdicts schedquality.Verdicts
	fromTest [3]bool
	policy   schedquality.Policy
	test     schedquality.TestSummary
	via      string
}

func (t schedQualityTransition) log(ctx context.Context) {
	via := t.via
	if via == "" && t.from.State == schedquality.StateCooling && t.to.State == schedquality.StateNormal {
		switch {
		case t.to.Reason == schedquality.ReasonUnverified && !t.policy.Enabled:
			via = "gate_off"
		case t.to.Reason == schedquality.ReasonUnverified:
			via = "no_conclusion"
		default:
			via = "pass"
		}
	}
	attrs := []any{"component", "sched_quality", "account_id", t.to.AccountID, "model", t.to.Model, "group_id", t.to.GroupID, "from", t.from.State.String(), "to", t.to.State.String(), "reason", string(t.to.Reason), "level", t.to.Level, "speed_n", t.summary.Speed.N, "speed_p90_ms", t.summary.Speed.P90Ms, "speed_p90_timeout", t.summary.Speed.P90Timeout, "error_n", t.summary.Error.N, "error_rate", t.summary.Error.Rate, "error_lcb", t.summary.Error.LCB, "cache_n", t.summary.Cache.Turns, "cache_ratio", t.summary.Cache.Ratio, "speed_verdict", t.verdicts.Speed.String(), "error_verdict", t.verdicts.Error.String(), "cache_verdict", t.verdicts.Cache.String(), "speed_from_test", t.fromTest[0], "error_from_test", t.fromTest[1], "cache_from_test", t.fromTest[2]}
	if via != "" {
		attrs = append(attrs, "via", via)
	}
	attrs = append(attrs, "test_speed_n", t.test.SpeedTurns, "test_speed_slow", t.test.SlowTurns, "test_max_ttfo_ms", t.test.MaxTTFOMs, "test_error_n", t.test.ErrorTurns, "test_faults", t.test.Faults, "test_cache_n", t.test.CacheTurns, "test_cache_ratio", t.test.CacheRatio)
	slog.InfoContext(ctx, "sched_quality.state", attrs...)
}

func cloneSchedQualityTest(r SchedTestRecord) SchedTestRecord {
	r.Turns = append([]schedquality.TestTurn(nil), r.Turns...)
	for i := range r.Turns {
		t := &r.Turns[i]
		t.TTFOMs = schedQualityCloneInt(t.TTFOMs)
		t.PromptTokens = schedQualityCloneInt(t.PromptTokens)
		t.PrevPromptTokens = schedQualityCloneInt(t.PrevPromptTokens)
		t.CacheReadTokens = schedQualityCloneInt(t.CacheReadTokens)
	}
	return r
}
