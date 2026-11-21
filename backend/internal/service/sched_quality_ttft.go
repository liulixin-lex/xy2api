package service

import (
	"log/slog"
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

// Bucket values are immutable. A cycle copies the index so a failed commit
// cannot publish newly frozen membership or a parameter-change override.
func clonePeerBuckets(src map[time.Time]*peerBucket, now time.Time) map[time.Time]*peerBucket {
	out := make(map[time.Time]*peerBucket)
	for at, b := range src {
		if !at.Before(now.Add(-schedEvidenceRetention)) {
			out[at] = b
		}
	}
	return out
}
func (d *schedQualityData) usePeerBucket(at time.Time, rows map[q.LineKey]q.LineState) {
	if d.peerBuckets == nil {
		d.peerBuckets = make(map[time.Time]*peerBucket)
	}
	bucket := at.UTC().Truncate(q.RegularInterval)
	if d.peerBuckets[bucket] == nil {
		d.peerBuckets[bucket] = freezePeers(at, rows)
	}
	d.peers = d.peerBuckets[bucket]
}

// Display and immediate reevaluation may precede replay's watermark. They may
// reuse an established bucket, but must not reserve a future bucket's membership.
func (d *schedQualityData) displayPeers(at time.Time, rows map[q.LineKey]q.LineState) {
	bucket := at.UTC().Truncate(q.RegularInterval)
	d.peers = d.peerBuckets[bucket]
	if d.peers == nil {
		d.peers = freezePeers(at, rows)
	}
}

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

func ttftInput(d *schedQualityData, k q.LineKey, cut, at time.Time, limit float64, resets map[int64]time.Time) q.RuleBInput {
	in := q.RuleBInput{Scope: q.RuleBScope{AccountID: k.AccountID, GroupID: k.GroupID, Model: k.Model}, Now: at, Cut: cut, LimitSeconds: limit, Samples: d.ruleSamples[SchedAccountModel{k.AccountID, k.Model}], GroupSamples: d.ruleGroups[schedGroupModel{k.GroupID, k.Model}]}
	if d.peers != nil {
		for key, r := range d.peers.rows {
			if key.GroupID != k.GroupID || key.Model != k.Model || key.AccountID == k.AccountID {
				continue
			}
			identity := -1
			if r.Identity == q.IdentityRear {
				identity = 1
			} // Rule B uses a different persisted identity encoding.
			in.Peers = append(in.Peers, q.RuleBPeer{Scope: q.RuleBScope{AccountID: key.AccountID, GroupID: key.GroupID, Model: key.Model}, Identity: identity, Epoch: schedQualitySince(r.Epoch, resets[key.AccountID]), Samples: d.ruleSamples[SchedAccountModel{key.AccountID, key.Model}]})
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
		result, err := q.EvaluateRuleB(ttftInput(d, r.LineKey, cut, at, p.TTFTSeconds, resets), s.rule.Params)
		if err != nil {
			slog.Warn("sched_quality.ttft_assessment", "component", "sched_quality", "account_id", r.AccountID, "model", r.Model, "group_id", r.GroupID, "error", err)
		}
		if err == nil {
			basis.TTFT = q.NewTTFTEvidence(result, p.TTFTSeconds)
			basis.PeerKnown = result.PeerCounts.N - result.PeerCounts.X
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
