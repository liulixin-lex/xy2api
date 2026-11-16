package service

import (
	"log/slog"
	"math"
	"sort"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

// peerBucket freezes identities and epochs before the first transition of a
// UTC five-minute bucket. Evidence is always evaluated at the current event.
type peerBucket struct {
	at   time.Time
	rows map[q.LineKey]q.LineState
}

func freezePeers(at time.Time, rows map[q.LineKey]q.LineState) *peerBucket {
	b := &peerBucket{at: at.UTC().Truncate(q.RegularInterval), rows: make(map[q.LineKey]q.LineState)}
	for k, r := range rows {
		if k.Line == q.DimensionTTFO {
			b.rows[k] = q.LineState{LineKey: k, Identity: r.Identity, Epoch: r.Epoch}
		}
	}
	return b
}

func ruleBTime(at time.Time) float64 {
	if at.IsZero() {
		return math.Inf(-1)
	}
	return float64(at.Unix()) + float64(at.Nanosecond())/1e9
}

func ruleBSamples(rows []q.AttemptSample) []q.RuleBSample {
	out := make([]q.RuleBSample, 0, len(rows))
	for _, r := range rows {
		if r.FinishedAt.IsZero() {
			continue
		}
		s := q.RuleBSample{AccountID: r.AccountID, GroupID: r.GroupID, Model: r.Model, SessionKey: r.SessionKey, StartedAt: ruleBTime(r.StartedAt), AvailableAt: ruleBTime(schedAvailableAt(r)), Stream: r.Stream, Outcome: r.Outcome, SwitchedAccount: r.Switched}
		if r.TTFOMs != nil {
			s.TTFOMs = float64(*r.TTFOMs)
		}
		if r.CensoredMs != nil {
			s.CensoredMs = float64(*r.CensoredMs)
		}
		out = append(out, s)
	}
	// Stable accumulation order makes replay independent of map/SQL row order.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.AvailableAt != b.AvailableAt {
			return a.AvailableAt < b.AvailableAt
		}
		if a.StartedAt != b.StartedAt {
			return a.StartedAt < b.StartedAt
		}
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		if a.GroupID != b.GroupID {
			return a.GroupID < b.GroupID
		}
		if a.SessionKey != b.SessionKey {
			return a.SessionKey < b.SessionKey
		}
		if a.TTFOMs != b.TTFOMs {
			return a.TTFOMs < b.TTFOMs
		}
		return a.CensoredMs < b.CensoredMs
	})
	return out
}

func ttftInput(d *schedQualityData, k q.LineKey, cut, at time.Time, limit float64, resets map[int64]time.Time) q.RuleBInput {
	in := q.RuleBInput{Scope: q.RuleBScope{AccountID: k.AccountID, GroupID: k.GroupID, Model: k.Model}, Now: ruleBTime(at), Cut: ruleBTime(cut), LimitSeconds: limit, Samples: d.ruleSamples[SchedAccountModel{k.AccountID, k.Model}], GroupSamples: d.ruleGroups[schedGroupModel{k.GroupID, k.Model}]}
	if d.peers != nil {
		for key, r := range d.peers.rows {
			if key.GroupID != k.GroupID || key.Model != k.Model || key.AccountID == k.AccountID {
				continue
			}
			identity := -1
			if r.Identity == q.IdentityRear {
				identity = 1
			} // Rule B uses a different persisted identity encoding.
			in.Peers = append(in.Peers, q.RuleBPeer{Scope: q.RuleBScope{AccountID: key.AccountID, GroupID: key.GroupID, Model: key.Model}, Identity: identity, Epoch: ruleBTime(schedQualitySince(r.Epoch, resets[key.AccountID])), Samples: d.ruleSamples[SchedAccountModel{key.AccountID, key.Model}]})
		}
	}
	sort.Slice(in.Peers, func(i, j int) bool { return in.Peers[i].Scope.AccountID < in.Peers[j].Scope.AccountID })
	return in
}

func (s *SchedQualityEngineService) assessLine(d *schedQualityData, r q.LineState, kind q.EventKind, at time.Time, resets map[int64]time.Time) (q.Verdict, q.Basis) {
	cut := schedQualitySince(r.Cut(kind), resets[r.AccountID])
	p := s.policies[r.GroupID].Policy
	basis := q.Basis{RuleVersion: q.RuleVersion, Line: r.Line, Cut: cut, Kind: []string{"regular", "trial", "review", "clock"}[kind]}
	pair := SchedAccountModel{r.AccountID, r.Model}
	var samples, running []q.AttemptSample
	for _, a := range d.samples[pair] {
		if !schedAvailableAt(a).After(at) {
			samples = append(samples, a)
		} else if !a.StartedAt.After(at) && a.FinishedAt.After(at) {
			a.FinishedAt = time.Time{}
			running = append(running, a)
		}
	}
	for _, a := range d.running {
		if a.AccountID == r.AccountID && a.Model == r.Model && !a.StartedAt.After(at) && (a.FinishedAt.IsZero() || a.FinishedAt.After(at)) {
			a.FinishedAt = time.Time{}
			running = append(running, a)
		}
	}
	verdict := q.VerdictUnknown
	switch r.Line {
	case q.DimensionTTFO:
		basis.Threshold = p.TTFTSeconds
		result, err := q.EvaluateRuleB(ttftInput(d, r.LineKey, cut, at, p.TTFTSeconds, resets), s.rule.Params)
		if err != nil {
			slog.Warn("sched_quality.ttft_assessment", "component", "sched_quality", "account_id", r.AccountID, "model", r.Model, "group_id", r.GroupID, "error", err)
		}
		if err == nil {
			basis.TTFT = &result
			switch result.State {
			case q.RuleBGood:
				verdict = q.VerdictPass
			case q.RuleBBad:
				verdict = q.VerdictFail
			}
		}
	case q.DimensionError:
		result := q.AssessError(samples, running, p.MaxErrorRate, cut, at)
		basis.Threshold = result.Limit
		basis.Error = &result
		basis.AttemptIDs = result.AttemptIDs
		verdict = result.Verdict
	case q.DimensionCache:
		result := q.AssessCache(samples, p.MinCacheRatio, cut, at)
		basis.Threshold = p.MinCacheRatio
		basis.Cache = &result
		basis.AttemptIDs = result.AttemptIDs
		verdict = result.Verdict
	}
	sources := make(map[int64]bool)
	switch r.Line {
	case q.DimensionTTFO:
		for _, a := range samples {
			available := schedAvailableAt(a)
			if !a.Stream || a.Switched || a.StartedAt.Before(cut) || available.Before(cut) || available.Before(at.Add(-4*time.Hour)) || (a.TTFOMs == nil || *a.TTFOMs <= 0) && a.Outcome != q.OutcomeTimeout {
				continue
			}
			sources[a.GroupID] = true
			if schedSlow(a, p.TTFTSeconds) {
				basis.AttemptIDs = append(basis.AttemptIDs, a.AttemptID)
			}
		}
		times := make(map[string]time.Time)
		for _, a := range samples {
			times[a.AttemptID] = schedAvailableAt(a)
		}
		sort.Slice(basis.AttemptIDs, func(i, j int) bool {
			a, b := basis.AttemptIDs[i], basis.AttemptIDs[j]
			if times[a].Equal(times[b]) {
				return a > b
			}
			return times[a].After(times[b])
		})
		if len(basis.AttemptIDs) > 50 {
			basis.AttemptIDs = basis.AttemptIDs[:50]
		}
	case q.DimensionError:
		for _, a := range schedErrorEvidence(samples, running, cut, at) {
			sources[a.GroupID] = true
		}
	case q.DimensionCache:
		for _, a := range samples {
			if q.AssessCache([]q.AttemptSample{a}, p.MinCacheRatio, cut, at).Turns > 0 {
				sources[a.GroupID] = true
			}
		}
	}
	for g := range sources {
		basis.SourceGroups = append(basis.SourceGroups, g)
	}
	sort.Slice(basis.SourceGroups, func(i, j int) bool { return basis.SourceGroups[i] < basis.SourceGroups[j] })
	return verdict, basis
}

func schedSlow(a q.AttemptSample, limit float64) bool {
	if !a.Stream || a.FinishedAt.IsZero() {
		return false
	}
	if a.TTFOMs != nil && *a.TTFOMs > 0 {
		return float64(*a.TTFOMs) > limit*1000
	}
	return a.Outcome == q.OutcomeTimeout && a.CensoredMs != nil && float64(*a.CensoredMs) >= limit*1000
}

// Provenance follows the same final-request and slot selection as AssessError;
// it includes successful and unknown units, not just the returned bad IDs.
func schedErrorEvidence(samples, running []q.AttemptSample, cut, now time.Time) []q.AttemptSample {
	type key struct {
		id       string
		fallback bool
	}
	latest := make(map[key]q.AttemptSample)
	for _, rows := range [][]q.AttemptSample{samples, running} {
		for _, a := range rows {
			if a.StartedAt.After(now) || a.FinishedAt.After(now) || !a.FinishedAt.IsZero() && a.FinishedAt.Before(a.StartedAt) {
				continue
			}
			k := key{a.RequestID, a.RequestID == ""}
			if k.fallback {
				k.id = a.AttemptID
			}
			old, ok := latest[k]
			if !ok || a.StartedAt.After(old.StartedAt) || a.StartedAt.Equal(old.StartedAt) && (a.AttemptID > old.AttemptID || a.AttemptID == old.AttemptID && a.FinishedAt.After(old.FinishedAt)) {
				latest[k] = a
			}
		}
	}
	at := func(a q.AttemptSample) time.Time {
		if a.FinishedAt.IsZero() {
			return a.StartedAt
		}
		return a.FinishedAt
	}
	var out []q.AttemptSample
	for _, a := range latest {
		if a.StartedAt.After(cut) && !at(a).Before(now.Add(-q.ErrorWindow)) && (a.FinishedAt.IsZero() || a.Outcome == q.OutcomeOK || a.Class.Counted() || a.Class == q.ClassUnknown) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !at(a).Equal(at(b)) {
			return at(a).After(at(b))
		}
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.After(b.StartedAt)
		}
		return a.AttemptID > b.AttemptID
	})
	return out[:min(len(out), q.ErrorSlots)]
}
