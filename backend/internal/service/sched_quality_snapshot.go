package service

import (
	"context"
	"log/slog"
	"sort"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

type schedQualityKey struct {
	account int64
	model   string
	group   int64
}
type schedQualityData struct {
	now           time.Time
	samples       map[SchedAccountModel][]q.AttemptSample
	globalEnabled bool
}

type schedQualityView struct {
	version       int64
	at            time.Time
	globalEnabled bool
	states        map[schedQualityKey]q.StateView
	policies      map[int64]SchedPolicyRecord
	overrides     map[int64][]SchedOverride
	resets        map[int64]time.Time
}

func (v *schedQualityView) Version() int64            { return v.version }
func (v *schedQualityView) At() time.Time             { return v.at }
func (v *schedQualityView) GlobalEnabled() bool       { return v.globalEnabled }
func (v *schedQualityView) GroupEnabled(g int64) bool { return v.policies[g].Policy.Enabled }
func (v *schedQualityView) ResetAt(a int64) time.Time { return v.resets[a] }
func (v *schedQualityView) StateOf(g, a int64, m string) q.StateView {
	state := v.states[schedQualityKey{a, m, g}]
	state.Dims = append([]q.Dimension(nil), state.Dims...)
	state.GroupOverride = overrideRef(v.overrides[a], g)
	state.AllOverride = overrideRef(v.overrides[a], 0)
	return state
}
func (v *schedQualityView) TakeoverIdle(g int64) (time.Duration, bool) {
	p, ok := v.policies[g]
	if !ok {
		return 900 * time.Second, true
	}
	if p.Policy.TakeoverIdleSeconds < 0 {
		return 0, false
	}
	return time.Duration(p.Policy.TakeoverIdleSeconds) * time.Second, true
}

func (s *SchedQualitySnapshot) Account(id int64) q.AccountSummary       { return s.Accounts[id] }
func (s *SchedQualitySnapshot) RowsForAccount(id int64) []SchedGroupRow { return s.GroupRows[id] }

func (s *SchedQualityEngineService) publish(d *schedQualityData, states []q.StateRow, overrides []SchedOverride, resets map[int64]time.Time, evaluatedAt time.Time) map[int64]time.Time {
	version := s.snapshot.Load().Version + 1
	active := make(map[int64][]SchedOverride)
	var copied []SchedOverride
	for _, o := range overrides {
		if o.Mode != q.OverrideExempt {
			continue
		}
		if o.ExpiresAt != nil {
			at := *o.ExpiresAt
			o.ExpiresAt = &at
		}
		copied = append(copied, o)
		active[o.AccountID] = append(active[o.AccountID], o)
	}
	view := &schedQualityView{version: version, at: evaluatedAt, globalEnabled: d.globalEnabled, states: make(map[schedQualityKey]q.StateView), policies: cloneSchedPolicies(s.policies), overrides: active, resets: cloneResetTimes(resets)}
	keys, previous := s.rowKeys(d, states)
	groupRows := make(map[int64][]SchedGroupRow)
	for _, key := range keys {
		row := previous[key]
		cut := schedQualitySince(resets[key.account], row.EvidenceSince)
		e := q.EvaluateAt(d.samples[SchedAccountModel{key.account, key.model}], q.Scope{AccountID: key.account, Model: key.model, GroupID: key.group}, s.policies[key.group].Policy, cut, d.now, q.EvaluateRegular)
		v := q.StateView{Failing: row.State == q.StateCooling, Level: row.Level, Since: row.Since, Until: row.Until, EvidenceSince: cut, GroupOverride: overrideRef(active[key.account], key.group), AllOverride: overrideRef(active[key.account], 0)}
		var other []int64
		if v.Failing && row.Basis != nil {
			v.Dims = append([]q.Dimension(nil), row.Basis.Dims...)
			groups := make(map[int64]bool)
			for _, metric := range row.Basis.Metrics {
				for _, g := range metric.OtherGroupIDs {
					groups[g] = true
				}
			}
			for g := range groups {
				other = append(other, g)
			}
			sort.Slice(other, func(i, j int) bool { return other[i] < other[j] })
		}
		if !v.Failing && !cut.IsZero() && d.now.Sub(cut) >= q.LevelDecayAfter {
			v.Level = 0
		}
		view.states[key] = v
		groupRows[key.account] = append(groupRows[key.account], SchedGroupRow{GroupID: key.group, Model: key.model, View: v, Evaluation: e, OtherGroupIDs: other})
	}
	members := make(map[int64][]int64)
	for group, ids := range s.members {
		for _, id := range ids {
			members[id] = append(members[id], group)
		}
	}
	accounts := make(map[int64]q.AccountSummary)
	byAccount := make(map[int64][]q.AttemptSample)
	modelRequests := make(map[int64]map[string]map[string]bool)
	for pair, rows := range d.samples {
		byAccount[pair.AccountID] = append(byAccount[pair.AccountID], rows...)
		for _, r := range rows {
			if r.FinishedAt.Before(d.now.Add(-time.Hour)) || r.FinishedAt.After(d.now) {
				continue
			}
			if modelRequests[r.GroupID] == nil {
				modelRequests[r.GroupID] = make(map[string]map[string]bool)
			}
			if modelRequests[r.GroupID][r.Model] == nil {
				modelRequests[r.GroupID][r.Model] = make(map[string]bool)
			}
			id := r.RequestID
			if id == "" {
				id = r.AttemptID
			}
			modelRequests[r.GroupID][r.Model][id] = true
		}
	}
	for id, rows := range byAccount {
		accounts[id] = q.SummarizeAccount(rows, id, resets[id], d.now)
	}
	for id := range members {
		sort.Slice(members[id], func(i, j int) bool { return members[id][i] < members[id][j] })
		if _, ok := accounts[id]; !ok {
			accounts[id] = q.SummarizeAccount(nil, id, resets[id], d.now)
		}
	}
	models := make(map[int64][]SchedModelCount)
	for group, counts := range modelRequests {
		for model, requests := range counts {
			models[group] = append(models[group], SchedModelCount{Model: model, Requests: len(requests)})
		}
		sort.Slice(models[group], func(i, j int) bool {
			a, b := models[group][i], models[group][j]
			if a.Requests == b.Requests {
				return a.Model < b.Model
			}
			return a.Requests > b.Requests
		})
	}
	s.snapshot.Store(&SchedQualitySnapshot{Version: version, At: evaluatedAt, GlobalEnabled: d.globalEnabled, View: view, Accounts: accounts, GroupModels: models, GroupRows: groupRows, Members: members, Policies: cloneSchedPolicies(s.policies), Overrides: copied, ResetAt: cloneResetTimes(resets)})
	newer := make(map[int64]time.Time)
	for id, at := range resets {
		if at.After(s.seenResets[id]) {
			if version > 1 {
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
func overrideRef(rows []SchedOverride, g int64) q.OverrideRef {
	for _, o := range rows {
		if o.GroupID == g && o.Mode == q.OverrideExempt {
			ref := q.OverrideRef{Mode: o.Mode}
			if o.ExpiresAt != nil {
				ref.Expires = *o.ExpiresAt
			}
			return ref
		}
	}
	return q.OverrideRef{}
}
func schedQualitySince(reset, since time.Time) time.Time {
	if reset.After(since) {
		return reset
	}
	return since
}
func (s *SchedQualityEngineService) rowKeys(d *schedQualityData, rows []q.StateRow) ([]schedQualityKey, map[schedQualityKey]q.StateRow) {
	models := make(map[int64]map[string]bool)
	add := func(a int64, m string) {
		if models[a] == nil {
			models[a] = make(map[string]bool)
		}
		models[a][m] = true
	}
	previous := make(map[schedQualityKey]q.StateRow)
	keys := make(map[schedQualityKey]bool)
	for pair := range d.samples {
		add(pair.AccountID, pair.Model)
	}
	for _, r := range rows {
		previous[schedQualityKeyOf(r)] = r
		add(r.AccountID, r.Model)
	}
	for group, ids := range s.members {
		for _, id := range ids {
			for model := range models[id] {
				keys[schedQualityKey{id, model, group}] = true
			}
		}
	}
	out := make([]schedQualityKey, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool { return schedQualityLess(out[i], out[j]) })
	return out, previous
}
func schedQualityKeyOf(r q.StateRow) schedQualityKey {
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

func logSchedDecision(ctx context.Context, d q.Decision) {
	slog.InfoContext(ctx, "sched_quality.state", "component", "sched_quality", "at", d.At, "rule_version", q.RuleVersion, "account_id", d.Scope.AccountID, "model", d.Scope.Model, "group_id", d.Scope.GroupID, "action", d.Action, "level", d.Level, "until", d.Until, "basis", d.Basis, "attempt_ids", d.Basis.AttemptIDs)
}
