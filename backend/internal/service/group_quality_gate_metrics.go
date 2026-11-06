package service

import (
	"math"
	"sort"
	"time"
)

type groupQualityMetrics struct {
	Factor       *float64
	TTFTSamples  int
	Sessions     int
	ErrorSamples int
	Failures     int
	ErrorRate    *float64
	ErrorLow     *float64
	ErrorHigh    *float64
	CacheSamples int
	CacheRate    *float64
	CacheLow     *float64
	CacheHigh    *float64
}

type groupQualityComposition struct {
	P90     *float64
	Samples int
}

type groupQualityCell struct {
	Model, Effort string
	Band, Cache   int
	Level         int
}

func groupQualityCells(o GroupQualityObservation) [4]groupQualityCell {
	band := 0
	switch {
	case o.PromptTokens >= 120000:
		band = 3
	case o.PromptTokens >= 60000:
		band = 2
	case o.PromptTokens >= 20000:
		band = 1
	}
	cache := 0
	if float64(o.CacheReadTokens) >= float64(o.PreviousPromptTokens)*.5 {
		cache = 1
	}
	return [4]groupQualityCell{
		{Model: o.Model, Effort: o.ReasoningEffort, Band: band, Cache: cache},
		{Model: o.Model, Effort: o.ReasoningEffort, Band: band, Level: 1},
		{Model: o.Model, Effort: o.ReasoningEffort, Band: band / 2, Level: 2},
		{Model: o.Model, Effort: o.ReasoningEffort, Level: 3},
	}
}

type groupQualityBaselines map[groupQualityCell]float64

func (b groupQualityBaselines) expected(o GroupQualityObservation) (float64, bool) {
	for _, cell := range groupQualityCells(o) {
		if value, ok := b[cell]; ok {
			return value, true
		}
	}
	return 0, false
}

type groupQualityRequest struct {
	AccountID int64
	RequestID string
}

type groupQualitySession struct {
	APIKeyID, GroupID int64
	SessionID         string
}

// prepareGroupQualityObservations applies process/reset boundaries before
// matching failures. Empty request IDs cannot be matched or deduplicated.
func prepareGroupQualityObservations(observations []GroupQualityObservation, now, processStart time.Time, resets map[int64]time.Time) ([]GroupQualityObservation, map[groupQualityRequest]bool, map[groupQualityRequest]bool) {
	eligible := make([]GroupQualityObservation, 0, len(observations))
	failed, softFailed := map[groupQualityRequest]bool{}, map[groupQualityRequest]bool{}
	for _, o := range observations {
		start := maxTime(now.Add(-groupQualityCompositionWindow), resets[o.AccountID], o.ResetAt)
		if o.At.Before(start) || !o.At.Before(now) {
			continue
		}
		if !o.PreviousAt.IsZero() && o.PreviousAt.Before(start) {
			o.StrictContinuation = false
		}
		ttftStart := maxTime(start, processStart)
		o.TTFTEligible = !o.At.Before(ttftStart) && (o.PreviousAt.IsZero() || !o.PreviousAt.Before(ttftStart))
		eligible = append(eligible, o)
		if o.RequestID == "" {
			continue
		}
		key := groupQualityRequest{o.AccountID, o.RequestID}
		if o.Failed || o.SoftFailure {
			failed[key] = true
		}
		if groupQualityV3SoftFailure(o) {
			softFailed[key] = true
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		a, b := eligible[i], eligible[j]
		if !a.At.Equal(b.At) {
			return a.At.After(b.At)
		}
		if a.ID != b.ID {
			return a.ID > b.ID
		}
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		return a.RequestID < b.RequestID
	})
	return eligible, failed, softFailed
}

func groupQualitySuccess(o GroupQualityObservation, failed map[groupQualityRequest]bool) bool {
	return o.Success && !o.Failed && !o.SoftFailure && (o.RequestID == "" || !failed[groupQualityRequest{o.AccountID, o.RequestID}])
}

func groupQualitySpeedSample(o GroupQualityObservation, failed map[groupQualityRequest]bool) bool {
	return o.TTFTEligible && groupQualitySuccess(o, failed) && o.StrictContinuation && o.AttemptFirstOutputMs != nil && *o.AttemptFirstOutputMs > 0
}

func buildGroupQualityBaselines(observations []GroupQualityObservation, failed map[groupQualityRequest]bool, now time.Time) groupQualityBaselines {
	type cellSamples struct {
		accounts map[int64][]float64
		total    int
	}
	cells := map[groupQualityCell]*cellSamples{}
	for _, o := range observations {
		if o.At.Before(now.Add(-groupQualityBaselineWindow)) || !groupQualitySpeedSample(o, failed) {
			continue
		}
		for _, key := range groupQualityCells(o) {
			cell := cells[key]
			if cell == nil {
				cell = &cellSamples{accounts: map[int64][]float64{}}
				cells[key] = cell
			}
			cell.accounts[o.AccountID] = append(cell.accounts[o.AccountID], float64(*o.AttemptFirstOutputMs))
			cell.total++
		}
	}
	out := groupQualityBaselines{}
	for key, cell := range cells {
		if len(cell.accounts) < 3 || cell.total < 30 {
			continue
		}
		medians := make([]float64, 0, len(cell.accounts))
		for _, values := range cell.accounts {
			medians = append(medians, groupQualityPercentile(values, .5))
		}
		out[key] = groupQualityPercentile(medians, .5)
	}
	return out
}

// Input is newest-first. Baseline matching happens before each sample cap;
// unavailable cells must not displace usable observations from the window.
func calculateGroupQualityMetrics(observations []GroupQualityObservation, now time.Time, baseline groupQualityBaselines, failed, softFailed map[groupQualityRequest]bool, enabledGroups map[int64]bool) (map[int64]groupQualityMetrics, map[int64]groupQualityComposition) {
	metrics := map[int64]groupQualityMetrics{}
	type speedSamples struct {
		logSum   float64
		sessions map[groupQualitySession]int
	}
	speeds := map[int64]*speedSamples{}
	kept := map[int64]int{}
	compositionValues := map[int64][]float64{}
	type failureKey struct {
		groupQualityRequest
		AtUnixMs int64
	}
	seenFailures := map[failureKey]bool{}
	for _, o := range observations {
		// Cache and composition describe the persisted request, including partial
		// usage. Only speed samples exclude matching failed attempts.
		success := o.Success
		if enabledGroups[o.GroupID] && o.TTFTEligible && success && o.StrictContinuation && len(compositionValues[o.GroupID]) < 100 {
			if expected, ok := baseline.expected(o); ok {
				compositionValues[o.GroupID] = append(compositionValues[o.GroupID], expected)
			}
		}
		if o.At.Before(now.Add(-groupQualityAccountWindow)) {
			continue
		}
		m := metrics[o.AccountID]
		if m.ErrorSamples < 100 {
			if groupQualityV3SoftFailure(o) {
				key := failureKey{groupQualityRequest{o.AccountID, o.RequestID}, o.At.UnixMilli()}
				if o.RequestID == "" || !seenFailures[key] {
					seenFailures[key] = true
					m.ErrorSamples++
					m.Failures++
				}
			} else if o.Success && (o.RequestID == "" || !softFailed[groupQualityRequest{o.AccountID, o.RequestID}]) {
				m.ErrorSamples++
			}
		}
		if m.TTFTSamples < 50 && groupQualitySpeedSample(o, failed) {
			if expected, ok := baseline.expected(o); ok {
				speed := speeds[o.AccountID]
				if speed == nil {
					speed = &speedSamples{sessions: map[groupQualitySession]int{}}
					speeds[o.AccountID] = speed
				}
				key := groupQualitySession{o.APIKeyID, o.GroupID, o.SessionID}
				if speed.sessions[key] < 3 {
					speed.sessions[key]++
					speed.logSum += math.Log(math.Min(4, float64(*o.AttemptFirstOutputMs)/expected))
					m.TTFTSamples++
					m.Sessions = len(speed.sessions)
				}
			}
		}
		if m.CacheSamples < 40 && success && o.StrictContinuation && o.PromptTokens >= 2048 && o.PreviousPromptTokens >= 2048 && float64(o.PromptTokens) >= float64(o.PreviousPromptTokens)*.5 {
			m.CacheSamples++
			if float64(o.CacheReadTokens) >= float64(o.PreviousPromptTokens)*.5 {
				kept[o.AccountID]++
			}
		}
		metrics[o.AccountID] = m
	}
	for id, m := range metrics {
		if m.TTFTSamples >= 10 && m.Sessions >= 3 {
			value := math.Exp(speeds[id].logSum / float64(m.TTFTSamples))
			m.Factor = &value
		}
		if m.ErrorSamples > 0 {
			value := float64(m.Failures) / float64(m.ErrorSamples)
			low, high := wilsonInterval(m.Failures, m.ErrorSamples)
			m.ErrorRate, m.ErrorLow, m.ErrorHigh = &value, &low, &high
		}
		if m.CacheSamples > 0 {
			value := float64(kept[id]) / float64(m.CacheSamples)
			low, high := wilsonInterval(kept[id], m.CacheSamples)
			m.CacheRate, m.CacheLow, m.CacheHigh = &value, &low, &high
		}
		metrics[id] = m
	}
	composition := map[int64]groupQualityComposition{}
	for id, values := range compositionValues {
		value := groupQualityComposition{Samples: len(values)}
		if len(values) >= 50 {
			p90 := groupQualityPercentile(values, .9)
			value.P90 = &p90
		}
		composition[id] = value
	}
	return metrics, composition
}

func groupQualityPercentile(values []float64, q float64) float64 {
	sort.Float64s(values)
	position := q * float64(len(values)-1)
	lo, hi := int(math.Floor(position)), int(math.Ceil(position))
	return values[lo] + (values[hi]-values[lo])*(position-float64(lo))
}

func groupQualityPrediction(m groupQualityMetrics, composition groupQualityComposition) *float64 {
	if m.Factor == nil || composition.P90 == nil {
		return nil
	}
	value := *m.Factor * *composition.P90
	return &value
}

func evaluateGroupQualityMember(now time.Time, p GroupQualityGateV3Policy, m groupQualityMetrics, expected *float64, state groupQualityAccountState) groupQualityAccountState {
	if expected == nil {
		state.TTFT = voteGroupQualityDimension(state.TTFT, now, false, false, false, false)
	} else {
		limit := float64(p.MaxTTFTP90Seconds) * 1000
		state.TTFT = voteGroupQualityDimension(state.TTFT, now, true, *expected > limit, *expected <= limit, *expected <= .9*limit)
	}
	if m.ErrorSamples < 20 || m.ErrorRate == nil {
		state.Error = voteGroupQualityDimension(state.Error, now, false, false, false, false)
	} else {
		state.Error = voteGroupQualityDimension(state.Error, now, true, m.Failures >= 5 && *m.ErrorRate > p.MaxErrorRate, *m.ErrorRate <= p.MaxErrorRate, *m.ErrorRate <= .8*p.MaxErrorRate)
	}
	if m.CacheSamples < 20 || m.CacheRate == nil || m.CacheHigh == nil {
		state.Cache = voteGroupQualityDimension(state.Cache, now, false, false, false, false)
	} else {
		pass := *m.CacheRate >= p.MinCacheKeepRate
		state.Cache = voteGroupQualityDimension(state.Cache, now, true, *m.CacheHigh < p.MinCacheKeepRate, pass, pass)
	}
	return state
}
