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

type AccountSummary struct {
	From, To                                       time.Time `json:",omitzero"`
	FirstEventP50Ms, FirstOutputP50Ms, GapP50Ms    *int      // 流式且两种时间都有的样本；Gap 是逐条差值的中位数
	FirstOutputN                                   int       // Paired streaming samples used by all three latency medians.
	ErrorN, ErrorBad, CacheTurns, CacheAllRequests int       // 报错按请求单位、口径同报错线（不填补）
	CacheRatio, CacheAllRatio                      *float64  // 判定口径（续接轮）、全部成功请求口径
}

// SummarizeAccount is display-only: all models and requests in the past hour,
// without the assessment's 100-slot cap or padding. Unknowns are not in ErrorN.
// CacheAllRatio deduplicates final request results; CacheRatio uses successful
// continuation attempts, over this hour rather than the line's 30-minute window.
// Request totals apply reset to the first request start, not to a retry's start;
// attempt-based continuation and latency totals retain their attempt cutoffs.
// The three latency medians use the same paired streaming samples; GapP50Ms is
// the median of per-attempt differences, not a subtraction of the two medians.
func SummarizeAccount(samples []AttemptSample, accountID int64, reset, now time.Time) AccountSummary {
	out := AccountSummary{From: now.Add(-time.Hour), To: now}
	if reset.After(out.From) {
		out.From = reset
	}
	account := make([]AttemptSample, 0, len(samples))
	for _, s := range samples {
		if s.AccountID == accountID {
			account = append(account, s)
		}
	}
	var events, outputs, gaps []int
	var continuationRead, continuationInput, allRead, allInput int64
	for _, s := range completedAttempts(account, reset, out.From, now) {
		if s.Stream && s.FirstEventMs != nil && s.TTFOMs != nil && *s.FirstEventMs >= 0 && *s.TTFOMs >= 0 {
			events = append(events, *s.FirstEventMs)
			outputs = append(outputs, *s.TTFOMs)
			gaps = append(gaps, *s.TTFOMs-*s.FirstEventMs)
		}
		if s.Continuation && s.SessionKey != "" {
			if read, input, ok := cacheTokens(s); ok {
				out.CacheTurns++
				continuationRead += read
				continuationInput += input
			}
		}
	}
	for _, unit := range finalRequests(account, nil, now) {
		s := unit.final
		if s.FinishedAt.IsZero() || s.FinishedAt.Before(out.From) || !unit.startedAt.After(reset) {
			continue
		}
		if s.Outcome == OutcomeOK {
			out.ErrorN++
		} else if s.Class.Counted() {
			out.ErrorN++
			out.ErrorBad++
		}
		if read, input, ok := cacheTokens(s); ok {
			out.CacheAllRequests++
			allRead += read
			allInput += input
		}
	}
	out.FirstOutputN = len(outputs)
	out.FirstEventP50Ms, out.FirstOutputP50Ms, out.GapP50Ms = medianMs(events), medianMs(outputs), medianMs(gaps)
	if continuationInput > 0 {
		ratio := float64(continuationRead) / float64(continuationInput)
		out.CacheRatio = &ratio
	}
	if allInput > 0 {
		ratio := float64(allRead) / float64(allInput)
		out.CacheAllRatio = &ratio
	}
	return out
}

// Preserve the existing nearest-rank (lower middle) P50 for integer timings.
func medianMs(values []int) *int {
	if len(values) == 0 {
		return nil
	}
	sort.Ints(values)
	value := values[(len(values)-1)/2]
	return &value
}
