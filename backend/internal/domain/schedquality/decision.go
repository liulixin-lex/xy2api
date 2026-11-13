package schedquality

import (
	"sort"
	"time"
)

type MetricBasis struct {
	Threshold     float64        `json:"threshold"`
	Scope         EvidenceSource `json:"scope"`
	OtherGroupIDs []int64        `json:"other_group_ids,omitempty"`
	WindowMin     int            `json:"window_min"`
	N             int            `json:"n"`
	Bad           int            `json:"bad"`
	Ratio         float64        `json:"ratio"`
	LCB           float64        `json:"lcb"`
	SessionsBelow int            `json:"sessions_below"`
}

type DecisionBasis struct {
	RuleVersion string                    `json:"rule_version"`
	Dims        []Dimension               `json:"dims"`
	Metrics     map[Dimension]MetricBasis `json:"metrics"`
	AttemptIDs  []string                  `json:"-"`
}

// DescribeDecision captures the evidence actually responsible for entering or
// renewing. Bad attempts are newest first and bounded independently of the
// unbounded statistical sample count.
func DescribeDecision(e Evaluation, samples []AttemptSample, p Policy) DecisionBasis {
	b := DecisionBasis{RuleVersion: RuleVersion, Dims: e.Dimensions(), Metrics: make(map[Dimension]MetricBasis)}
	bad := make(map[string]AttemptSample)
	for _, dim := range b.Dims {
		var w EvidenceWindow
		var metric MetricBasis
		switch dim {
		case DimensionTTFO:
			speed := e.Speed
			if e.Kind == EvaluateFast || e.Fast.N == FastMinSamples && e.Fast.Slow >= FastMinSlow && (e.Speed.N < SpeedMinSamples || e.Speed.LCB <= SlowShareTolerance) {
				speed = e.Fast
			}
			w = speed.EvidenceWindow
			metric = MetricBasis{Threshold: p.TTFTSeconds, N: speed.N, Bad: speed.Slow, LCB: speed.LCB}
		case DimensionError:
			w = e.Error.EvidenceWindow
			metric = MetricBasis{Threshold: p.MaxErrorRate, N: e.Error.N, Bad: e.Error.Bad, Ratio: e.Error.Rate, LCB: e.Error.LCB}
		case DimensionCache:
			w = e.Cache.EvidenceWindow
			metric = MetricBasis{Threshold: p.MinCacheRatio, N: e.Cache.Turns, Ratio: e.Cache.Ratio, SessionsBelow: e.Cache.LowSessions}
		}
		metric.Scope, metric.WindowMin = w.Source, int(w.Window/time.Minute)
		ids := make(map[string]bool, len(w.AttemptIDs))
		for _, id := range w.AttemptIDs {
			ids[id] = true
		}
		groups := make(map[int64]bool)
		for _, s := range samples {
			if !ids[s.AttemptID] {
				continue
			}
			if w.Source == EvidenceOther {
				groups[s.GroupID] = true
			}
			isBad := false
			switch dim {
			case DimensionTTFO:
				isBad = s.Outcome == OutcomeTimeout || s.TTFOMs != nil && float64(*s.TTFOMs) > p.TTFTSeconds*1000
			case DimensionError:
				isBad = errorSample(s) && s.Outcome != OutcomeOK
			case DimensionCache:
				if cacheSample(s) {
					total := int64(*s.InputTokens) + int64(*s.CacheReadTokens) + int64(*s.CacheCreationTokens)
					isBad = float64(*s.CacheReadTokens)/float64(total) < p.MinCacheRatio-CacheTolerance
				}
			}
			if isBad {
				bad[s.AttemptID] = s
			}
		}
		for id := range groups {
			metric.OtherGroupIDs = append(metric.OtherGroupIDs, id)
		}
		sort.Slice(metric.OtherGroupIDs, func(i, j int) bool { return metric.OtherGroupIDs[i] < metric.OtherGroupIDs[j] })
		b.Metrics[dim] = metric
	}
	ordered := make([]AttemptSample, 0, len(bad))
	for _, s := range bad {
		ordered = append(ordered, s)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].FinishedAt.Equal(ordered[j].FinishedAt) {
			return ordered[i].AttemptID > ordered[j].AttemptID
		}
		return ordered[i].FinishedAt.After(ordered[j].FinishedAt)
	})
	for _, s := range ordered[:min(len(ordered), 50)] {
		b.AttemptIDs = append(b.AttemptIDs, s.AttemptID)
	}
	return b
}

type Decision struct {
	At     time.Time
	Scope  Scope
	Action string
	Level  int
	Until  time.Time
	Basis  DecisionBasis
}
