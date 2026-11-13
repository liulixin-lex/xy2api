package schedquality

import "time"

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
