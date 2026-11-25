package service

import (
	"log/slog"
	"sort"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

func ruleBSample(r q.AttemptSample) q.RuleBSample {
	s := q.RuleBSample{
		AccountID: r.AccountID, GroupID: r.GroupID, Model: r.Model, SessionKey: r.SessionKey,
		StartedAt: r.StartedAt, AvailableAt: schedAvailableAt(r),
		Stream: r.Stream, Outcome: r.Outcome, SwitchedAccount: r.Switched,
		CaptureVersion: r.CaptureVersion, Warm: r.Warm, FullTierFirstOutputTimeout: r.FullTierFirstOutputTimeout,
	}
	if r.TTFOMs != nil {
		s.TTFOMs = float64(*r.TTFOMs)
	}
	if r.CensoredMs != nil {
		s.CensoredMs = float64(*r.CensoredMs)
	}
	return s
}

func ruleBSamples(rows []q.AttemptSample) []q.RuleBSample {
	out := make([]q.RuleBSample, 0, len(rows))
	for _, r := range rows {
		if r.FinishedAt.IsZero() {
			continue
		}
		out = append(out, ruleBSample(r))
	}
	// Stable accumulation order makes replay independent of map/SQL row order.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if cmp := a.AvailableAt.Compare(b.AvailableAt); cmp != 0 {
			return cmp < 0
		}
		if cmp := a.StartedAt.Compare(b.StartedAt); cmp != 0 {
			return cmp < 0
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

func ttftInput(d *schedQualityData, k q.LineKey, cut, at time.Time, limit float64) q.RuleBInput {
	in := q.RuleBInput{Scope: q.RuleBScope{AccountID: k.AccountID, GroupID: k.GroupID, Model: k.Model}, Now: at, Cut: cut, LimitSeconds: limit, Samples: d.ruleSamples[SchedAccountModel{k.AccountID, k.Model}], GroupSamples: d.ruleGroups[schedGroupModel{k.GroupID, k.Model}]}
	return in
}

func (s *SchedQualityEngineService) assessLine(d *schedQualityData, r q.LineState, kind q.EventKind, at time.Time, resets map[int64]time.Time) (q.Verdict, q.Basis) {
	cut := schedQualitySince(r.Cut(kind), resets[r.AccountID])
	p := s.policies[r.GroupID].Policy
	basis := q.Basis{RuleVersion: q.RuleVersion, Line: r.Line, Cut: cut, Kind: []string{"regular", "trial", "review", "clock"}[kind]}
	// A missing historical window cannot produce a new confirmed failure.
	window := q.ErrorWindow
	if r.Line == q.DimensionCache {
		window = q.CacheWindow
	}
	if r.Line == q.DimensionTTFO {
		window = 8 * q.RuleBHalfLife
	}
	if !d.evidenceSince.IsZero() && schedQualitySince(cut, at.Add(-window)).Before(d.evidenceSince) {
		basis.HistoryMissing = true
		return q.VerdictUnknown, basis
	}
	pair := SchedAccountModel{r.AccountID, r.Model}
	var samples, running []q.AttemptSample
	for _, a := range d.samples[pair] {
		if !schedAvailableAt(a).After(at) {
			samples = append(samples, a)
		} else if !a.StartedAt.After(at) {
			// A completed but not yet available retry must still hide the earlier
			// attempt. Its eventual outcome cannot become evidence before insertion.
			a.FinishedAt = time.Time{}
			running = append(running, a)
		}
	}
	for _, a := range d.running {
		if a.AccountID == r.AccountID && a.Model == r.Model && !a.StartedAt.After(at) {
			a.FinishedAt = time.Time{}
			running = append(running, a)
		}
	}
	verdict := q.VerdictUnknown
	switch r.Line {
	case q.DimensionTTFO:
		basis.Threshold = p.TTFTSeconds
		result, err := q.EvaluateRuleB(ttftInput(d, r.LineKey, cut, at, p.TTFTSeconds), s.rule.Params)
		if err != nil {
			slog.Warn("sched_quality.ttft_assessment", "component", "sched_quality", "account_id", r.AccountID, "model", r.Model, "group_id", r.GroupID, "error", err)
		}
		if err == nil {
			basis.TTFT = q.NewTTFTEvidence(result, p.TTFTSeconds)
			basis.SampleCounts = q.NewSampleCounts(result.SamplesLocal, result.SamplesTotal)
			switch result.State {
			case q.RuleBGood:
				verdict = q.VerdictPass
			case q.RuleBBad:
				verdict = q.VerdictFail
			}
		}
	case q.DimensionError:
		result, local := q.AssessErrorForGroup(samples, running, p.MaxErrorRate, cut, at, r.GroupID)
		basis.SampleCounts = q.NewSampleCounts(local, result.Clear)
		if kind == q.EventReview || kind == q.EventTrial {
			result = result.ForRecovery()
		}
		basis.Threshold = result.Limit
		basis.Error = &result
		basis.SourceGroups = append([]int64(nil), result.SourceGroups...)
		basis.AttemptIDs = result.AttemptIDs
		verdict = result.Verdict
	case q.DimensionCache:
		cacheSamples := maskRunningCachePredecessors(samples, d.runningCache, d.previous)
		result, local := q.AssessCacheForGroup(cacheSamples, p.MinCacheRatio, cut, at, r.GroupID)
		basis.SampleCounts = q.NewSampleCounts(local, result.Turns)
		basis.Threshold = p.MinCacheRatio
		basis.Cache = &result
		basis.SourceGroups = append([]int64(nil), result.SourceGroups...)
		basis.AttemptIDs = result.AttemptIDs
		verdict = result.Verdict
	}
	sources := make(map[int64]bool)
	switch r.Line {
	case q.DimensionTTFO:
		for _, a := range samples {
			if !ruleBSample(a).Eligible(at, cut) {
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
		return float64(*a.TTFOMs)/1000 > limit
	}
	return a.Outcome == q.OutcomeTimeout && a.CensoredMs != nil && float64(*a.CensoredMs)/1000 >= limit
}
