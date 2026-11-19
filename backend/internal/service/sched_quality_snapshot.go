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
	replayStats   schedReplayStats
	now           time.Time
	samples       map[SchedAccountModel][]q.AttemptSample
	globalEnabled bool
	running       map[string]q.AttemptSample
	ruleSamples   map[SchedAccountModel][]q.RuleBSample
	ruleGroups    map[schedGroupModel][]q.RuleBSample
	peers         *peerBucket
	peerBuckets   map[time.Time]*peerBucket
}

type schedQualityView struct {
	version       int64
	at            time.Time
	globalEnabled bool
	states        map[schedQualityKey]q.StateView
	policies      map[int64]SchedPolicyRecord
	overrides     map[int64][]SchedOverride
	resets        map[int64]time.Time
	sessions      map[schedSessionKey][]q.AttemptSample
}

func (v *schedQualityView) Version() int64            { return v.version }
func (v *schedQualityView) At() time.Time             { return v.at }
func (v *schedQualityView) GlobalEnabled() bool       { return v.globalEnabled }
func (v *schedQualityView) GroupEnabled(g int64) bool { return v.policies[g].Policy.Enabled }
func (v *schedQualityView) ResetAt(a int64) time.Time { return v.resets[a] }
func (v *schedQualityView) StateOf(g, a int64, m string) q.StateView {
	state := v.states[schedQualityKey{a, m, g}]
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

func (v *schedQualityView) SessionSlowTwice(g, a int64, m, session string) bool {
	rows := v.sessions[schedSessionKey{g, m, session}]
	if len(rows) < 2 {
		return false
	}
	limit := v.policies[g].Policy.TTFTSeconds
	for _, r := range rows[len(rows)-2:] {
		if r.Switched || r.AccountID != a || !r.StartedAt.After(v.resets[a]) || !schedSlow(r, limit) {
			return false
		}
	}
	return true
}

func (s *SchedQualitySnapshot) Account(id int64) q.AccountSummary       { return s.Accounts[id] }
func (s *SchedQualitySnapshot) RowsForAccount(id int64) []SchedGroupRow { return s.GroupRows[id] }

func (s *SchedQualityEngineService) publish(d *schedQualityData, states []q.LineState, overrides []SchedOverride, resets map[int64]time.Time, evaluatedAt time.Time) map[int64]time.Time {
	version := s.snapshot.Load().Version + 1
	// Administrative rebuilds can run after a failed bootstrap page too.
	// Never activate layering from that incomplete evidence window.
	enabled := d.globalEnabled && s.loaded
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
	view := &schedQualityView{version: version, at: evaluatedAt, globalEnabled: enabled, states: make(map[schedQualityKey]q.StateView), policies: cloneSchedPolicies(s.policies), overrides: active, resets: cloneResetTimes(resets)}
	keys, previous := s.rowKeys(d, states)
	groupRows := make(map[int64][]SchedGroupRow)
	// Display uses the same bucket membership as replay, without changing it.
	for _, k := range keys {
		r := previous[k]
		r.LineKey = k
		previous[k] = r
	}
	d.displayPeers(d.now, previous)
	s.peerBuckets = d.peerBuckets
	grouped := make(map[schedQualityKey]*SchedGroupRow)
	for _, key := range keys {
		r := previous[key]
		r.LineKey = key
		groupKey := schedQualityKey{key.AccountID, key.Model, key.GroupID}
		row := grouped[groupKey]
		if row == nil {
			row = &SchedGroupRow{GroupID: key.GroupID, Model: key.Model}
			grouped[groupKey] = row
		}
		lv := q.LineView{Identity: r.Identity, Trial: r.Trial, Level: r.Level, Until: r.Until, TrialUntil: r.TrialUntil}
		verdict, basis := s.assessLine(d, r, q.EventRegular, d.now, resets)
		switch key.Line {
		case q.DimensionTTFO:
			row.TTFTLastEval = SchedAdminLastEvalFromState(r)
			row.TTFTSamples = basis.SampleCounts
			row.View.TTFO = lv
			row.TTFTVerdict = verdict
			row.TTFTSourceGroups = append([]int64(nil), basis.SourceGroups...)
			if basis.TTFT != nil {
				row.TTFT = *basis.TTFT
			}
		case q.DimensionError:
			row.ErrorLastEval = SchedAdminLastEvalFromState(r)
			row.ErrorSamples = basis.SampleCounts
			row.View.Error = lv
			if basis.Error != nil {
				row.Error = *basis.Error
			}
		case q.DimensionCache:
			row.CacheLastEval = SchedAdminLastEvalFromState(r)
			row.CacheSamples = basis.SampleCounts
			row.View.Cache = lv
			if basis.Cache != nil {
				row.Cache = *basis.Cache
			}
		}
		row.SourceGroups = append(row.SourceGroups, basis.SourceGroups...)
	}
	for key, row := range grouped {
		row.View.GroupOverride = overrideRef(active[key.account], key.group)
		row.View.AllOverride = overrideRef(active[key.account], 0)
		sort.Slice(row.SourceGroups, func(i, j int) bool { return row.SourceGroups[i] < row.SourceGroups[j] })
		unique := row.SourceGroups[:0]
		for _, g := range row.SourceGroups {
			if len(unique) == 0 || unique[len(unique)-1] != g {
				unique = append(unique, g)
			}
		}
		row.SourceGroups = unique
		view.states[key] = row.View
		groupRows[key.account] = append(groupRows[key.account], *row)
	}
	for _, rows := range groupRows {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].GroupID != rows[j].GroupID {
				return rows[i].GroupID < rows[j].GroupID
			}
			return rows[i].Model < rows[j].Model
		})
	}
	sessions := make(schedPredecessorIndex)
	for _, rows := range d.samples {
		for _, r := range rows {
			if !schedAvailableAt(r).After(d.now) {
				sessions.add(schedQualityCloneSample(r))
			}
		}
	}
	// Finished observations already have a result, even before the ledger write.
	// In-flight attempts are not completed rounds and cannot block migration.
	unpersisted := make(map[string]bool)
	for _, r := range d.running {
		if !r.FinishedAt.IsZero() && !r.FinishedAt.After(d.now) {
			sessions.add(schedQualityCloneSample(r))
			unpersisted[r.AttemptID] = true
		}
	}
	sessions.sort()
	view.sessions = make(map[schedSessionKey][]q.AttemptSample)
	for k, rows := range sessions {
		view.sessions[k] = schedSessionRounds(rows, unpersisted)
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
	s.snapshot.Store(&SchedQualitySnapshot{Version: version, At: evaluatedAt, GlobalEnabled: enabled, View: view, Accounts: accounts, GroupModels: models, GroupRows: groupRows, Members: members, Policies: cloneSchedPolicies(s.policies), Overrides: copied, ResetAt: cloneResetTimes(resets)})
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
func (s *SchedQualityEngineService) rowKeys(d *schedQualityData, states []q.LineState) ([]q.LineKey, map[q.LineKey]q.LineState) {
	models := make(map[int64]map[string]bool)
	add := func(a int64, m string) {
		if models[a] == nil {
			models[a] = make(map[string]bool)
		}
		models[a][m] = true
	}
	rows := make(map[q.LineKey]q.LineState)
	for pair := range d.samples {
		add(pair.AccountID, pair.Model)
	}
	for _, r := range d.running {
		add(r.AccountID, r.Model)
	}
	for _, r := range states {
		rows[r.LineKey] = r
		add(r.AccountID, r.Model)
	}
	var keys []q.LineKey
	for g, ids := range s.members {
		for _, a := range ids {
			for m := range models[a] {
				for _, line := range q.Lines {
					keys = append(keys, q.LineKey{AccountID: a, GroupID: g, Model: m, Line: line})
				}
			}
		}
	}
	sort.Slice(keys, func(i, j int) bool { return schedLineLess(keys[i], keys[j]) })
	return keys, rows
}
func schedLineLess(a, b q.LineKey) bool {
	if a.AccountID != b.AccountID {
		return a.AccountID < b.AccountID
	}
	if a.Model != b.Model {
		return a.Model < b.Model
	}
	if a.GroupID != b.GroupID {
		return a.GroupID < b.GroupID
	}
	return a.Line < b.Line
}
func (s *SchedQualityEngineService) isMember(group, account int64) bool {
	for _, id := range s.members[group] {
		if id == account {
			return true
		}
	}
	return false
}

// LogSchedQualityDecision emits a committed decision using the shared state-event format.
func LogSchedQualityDecision(ctx context.Context, d q.Decision) {
	slog.InfoContext(ctx, "sched_quality.state", "component", "sched_quality", "at", d.At, "rule_version", q.RuleVersion, "account_id", d.Key.AccountID, "model", d.Key.Model, "group_id", d.Key.GroupID, "action", d.Action, "line", d.Key.Line, "level", d.Level, "until", d.Until, "basis", d.Basis, "attempt_ids", d.Basis.AttemptIDs)
}

// These projections support the existing board/admin response during integration.
// The downstream packages replace their response shapes with per-line fields.
func schedRearLines(v q.StateView) []q.Dimension {
	var out []q.Dimension
	for i, l := range []q.LineView{v.TTFO, v.Error, v.Cache} {
		if l.Identity == q.IdentityRear {
			out = append(out, q.Lines[i])
		}
	}
	return out
}

// Retries share a logical request/turn and the final finished attempt determines
// its result. Preserve ledger-confirmed boundaries unless an unpersisted
// predecessor supplements the ledger; observed rounds use merged history.
func schedSessionRounds(attempts []q.AttemptSample, unpersisted map[string]bool) []q.AttemptSample {
	type roundKey struct {
		id      string
		attempt bool
	}
	rounds := make(map[roundKey]q.AttemptSample)
	for _, r := range attempts {
		key := roundKey{id: r.RequestID}
		if key.id == "" {
			key = roundKey{id: r.AttemptID, attempt: true}
		}
		previous, ok := rounds[key]
		switched := r.Switched || previous.Switched
		if !ok || r.StartedAt.After(previous.StartedAt) || r.StartedAt.Equal(previous.StartedAt) && (r.FinishedAt.After(previous.FinishedAt) || r.FinishedAt.Equal(previous.FinishedAt) && r.AttemptID > previous.AttemptID) {
			previous = r
		}
		previous.Switched = switched
		rounds[key] = previous
	}
	rows := make([]q.AttemptSample, 0, len(rounds))
	for _, r := range rounds {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].StartedAt.Equal(rows[j].StartedAt) {
			return rows[i].AttemptID < rows[j].AttemptID
		}
		return rows[i].StartedAt.Before(rows[j].StartedAt)
	})
	var tail []q.AttemptSample
	for i, r := range rows {
		if i > 0 && (unpersisted[r.AttemptID] || unpersisted[rows[i-1].AttemptID]) {
			r.Switched = rows[i-1].AccountID != r.AccountID
		} else if i == 0 && unpersisted[r.AttemptID] {
			// Without a predecessor, this observation may be a cold first round.
			// Exclude only this round; later completed rounds can still qualify.
			r.Switched = true
		}
		// Otherwise preserve the durable round's confirmed marker: its true
		// predecessor may have expired before an older, overlapping request.
		if r.Switched {
			tail = nil
			continue
		}
		tail = append(tail, r)
		if len(tail) > 2 {
			tail = tail[len(tail)-2:]
		}
	}
	return tail
}
