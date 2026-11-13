package schedquality

import (
	"sort"
	"time"
)

type Dimension string

const (
	DimensionTTFO  Dimension = "ttfo"
	DimensionError Dimension = "error"
	DimensionCache Dimension = "cache"
)

func (e Evaluation) Dimensions() []Dimension {
	var dims []Dimension
	for i, verdict := range [...]Verdict{e.Verdicts.Speed, e.Verdicts.Error, e.Verdicts.Cache} {
		if verdict == VerdictFail {
			dims = append(dims, [...]Dimension{DimensionTTFO, DimensionError, DimensionCache}[i])
		}
	}
	return dims
}

// AccountSummary covers all groups and models, after the manual reset cutoff.
// Timed group recovery never changes this account-wide window.
type AccountSummary struct {
	From, To time.Time
	Speed    SpeedEvidence
	Error    ErrorEvidence
	Cache    CacheEvidence
}

// SummarizeAccount computes the displayed account totals without group recovery
// cutoffs or group policy thresholds. Unknown token buckets remain unknown.
func SummarizeAccount(samples []AttemptSample, accountID int64, reset, now time.Time) AccountSummary {
	out := AccountSummary{From: now.Add(-time.Hour), To: now}
	if reset.After(out.From) {
		out.From = reset
	}
	var speed, errors, cache []AttemptSample
	seen := make(map[string]bool)
	for _, s := range samples {
		if s.AccountID != accountID || s.FinishedAt.Before(out.From) || s.FinishedAt.After(now) || s.FinishedAt.IsZero() ||
			s.StartedAt.After(s.FinishedAt) || !s.StartedAt.After(reset) || seen[s.AttemptID] {
			continue
		}
		seen[s.AttemptID] = true
		if speedSample(s) {
			speed = append(speed, s)
		}
		if errorSample(s) {
			errors = append(errors, s)
		}
		if cacheSample(s) {
			cache = append(cache, s)
		}
	}
	sortByFinish := func(rows []AttemptSample) {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].FinishedAt.Equal(rows[j].FinishedAt) {
				return rows[i].AttemptID < rows[j].AttemptID
			}
			return rows[i].FinishedAt.Before(rows[j].FinishedAt)
		})
	}
	sortByFinish(speed)
	sortByFinish(errors)
	sortByFinish(cache)
	out.Speed = speedEvidence(speed, describeEvidence(speed, EvidenceShared, time.Hour), Policy{})
	out.Speed.Slow, out.Speed.LCB = 0, 0
	values := make([]int, 0, len(speed))
	for _, s := range speed {
		value := s.TTFOMs
		if s.Outcome == OutcomeTimeout {
			value = s.CensoredMs
		}
		if value != nil {
			values = append(values, *value)
		}
	}
	out.Speed.P50Ms = nil
	out.Speed.P50Timeout = false
	if len(values) == len(speed) && len(values) > 0 {
		sort.Ints(values)
		median := values[(len(values)-1)/2]
		out.Speed.P50Ms = &median
	}
	out.Error = errorEvidence(errors, describeEvidence(errors, EvidenceShared, time.Hour))
	out.Cache = cacheEvidence(cache, describeEvidence(cache, EvidenceShared, time.Hour), Policy{})
	return out
}
