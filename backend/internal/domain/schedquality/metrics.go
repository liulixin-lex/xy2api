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

const RecentSlowLineMs = 10_000

type FirstOutputSummary struct {
	N        int  `json:"n"`
	Timeouts int  `json:"timeouts"`
	P50Ms    *int `json:"p50_ms"`
}

type Slow10sSummary struct {
	N        int `json:"n"`
	Slow     int `json:"slow"`
	Timeouts int `json:"timeouts"`
}

type AccountSummary struct {
	From, To         time.Time `json:",omitzero"`
	FirstOutput      FirstOutputSummary
	Slow10s          Slow10sSummary
	ErrorN, ErrorBad int // Known final request outcomes, without padding.
	CacheTurns       int
	CacheRatio       *float64 // Successful continuation attempts.
}

// SummarizeAccount is display-only and covers all models over the past hour.
// Reset applies to the first start of each request for errors, and to each
// attempt for first output and cache.
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
	var outputs []int
	var continuationRead, continuationInput int64
	for _, s := range completedAttempts(account, reset, out.From, now) {
		switch {
		case s.Stream && s.TTFOMs != nil && *s.TTFOMs >= 0:
			outputs = append(outputs, *s.TTFOMs)
			if *s.TTFOMs > RecentSlowLineMs {
				out.Slow10s.Slow++
			}
		case s.Stream && s.Outcome == OutcomeTimeout:
			out.FirstOutput.Timeouts++
			if s.CensoredMs != nil && *s.CensoredMs >= RecentSlowLineMs {
				out.Slow10s.Timeouts++
				out.Slow10s.Slow++
			}
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
	}
	out.FirstOutput.N = len(outputs) + out.FirstOutput.Timeouts
	out.Slow10s.N = len(outputs) + out.Slow10s.Timeouts
	// Timeouts sort after all observed outputs. A rank in that tail has no duration.
	if mid := (out.FirstOutput.N - 1) / 2; mid < len(outputs) {
		sort.Ints(outputs)
		out.FirstOutput.P50Ms = new(outputs[mid])
	}
	if continuationInput > 0 {
		ratio := float64(continuationRead) / float64(continuationInput)
		out.CacheRatio = &ratio
	}
	return out
}
