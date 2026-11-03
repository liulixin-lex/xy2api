package service

import (
	"context"
	"math"
	"sort"
	"strconv"
	"time"
)

const (
	AccountRecentFailureMaxEvents  = 200
	AccountRecentFailureWindow     = time.Hour
	AccountRecentSampleMax         = 50
	AccountRecentSampleWindow      = 2 * time.Hour
	AccountRecentHistoryWindow     = 125 * time.Minute
	AccountRecentMinDisplaySamples = 20
	AccountRecentMinColorSamples   = 50
)

type AccountRecentFailureStats struct {
	Calls               int     `json:"calls"`
	Failures            int     `json:"failures"`
	WilsonLow           float64 `json:"wilson_low"`
	WilsonHigh          float64 `json:"wilson_high"`
	FirstOutputTimeouts int     `json:"first_output_timeouts"`
	StreamFailures      int     `json:"stream_failures"`
	OtherFailures       int     `json:"other_failures"`
	SpanSeconds         int     `json:"span_seconds"`
}

type AccountRecentFirstOutputStats struct {
	Samples     int     `json:"samples"`
	P50Ms       int     `json:"p50_ms"`
	P90Ms       int     `json:"p90_ms"`
	SlowShare   float64 `json:"slow_share"`
	SpanSeconds int     `json:"span_seconds"`
}

type AccountRecentCacheStats struct {
	Samples      int   `json:"samples"`
	Kept         int   `json:"kept"`
	Zero         int   `json:"zero"`
	Partial      int   `json:"partial"`
	Mid          int   `json:"mid"`
	Full         int   `json:"full"`
	ReadTokens   int64 `json:"read_tokens"`
	PromptTokens int64 `json:"prompt_tokens"`
	SpanSeconds  int   `json:"span_seconds"`
}

type AccountRecentHealth struct {
	Failure     *AccountRecentFailureStats     `json:"failure"`
	FirstOutput *AccountRecentFirstOutputStats `json:"first_output"`
	Cache       *AccountRecentCacheStats       `json:"cache"`
}

// AccountRecentHealthBatchReader is an optional UsageLogRepository capability.
// The repository implements it without widening UsageLogRepository so existing
// test doubles need not implement recent-health reads. The type assertion below
// cannot detect a stale signature at compile time, so keep test doubles' method
// signatures in sync with this interface.
type AccountRecentHealthBatchReader interface {
	GetAccountRecentHealthBatch(ctx context.Context, accountIDs []int64, now time.Time) (map[int64][]GroupQualityObservation, error)
}

func (s *AccountUsageService) GetRecentHealthBatch(ctx context.Context, accountIDs []int64) (map[int64]AccountRecentHealth, error) {
	ids := make([]int64, 0, len(accountIDs))
	seen := make(map[int64]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	result := make(map[int64]AccountRecentHealth, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	reader, ok := s.usageLogRepo.(AccountRecentHealthBatchReader)
	if !ok {
		return result, nil
	}
	now := time.Now().UTC()
	observations, err := reader.GetAccountRecentHealthBatch(ctx, ids, now)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		health := AccountRecentHealthFor(observations[id], now)
		if health.Failure != nil || health.FirstOutput != nil || health.Cache != nil {
			result[id] = health
		}
	}
	return result, nil
}

// AccountRecentHealthFor derives the recent-health metrics from persisted observations.
func AccountRecentHealthFor(observations []GroupQualityObservation, now time.Time) AccountRecentHealth {
	return AccountRecentHealth{
		Failure:     recentFailureStats(observations, now),
		FirstOutput: recentFirstOutputStats(observations, now),
		Cache:       recentCacheStats(observations, now),
	}
}

type recentEvent struct {
	at        time.Time
	accountID int64
	requestID string
	softFail  bool
	kind      string
	category  string
}

func recentFailureStats(observations []GroupQualityObservation, now time.Time) *AccountRecentFailureStats {
	from := now.Add(-AccountRecentFailureWindow)
	type requestKey struct {
		accountID int64
		requestID string
	}
	failedRequests := make(map[requestKey]bool)
	for _, o := range observations {
		if o.SoftFailure && o.RequestID != "" {
			failedRequests[requestKey{o.AccountID, o.RequestID}] = true
		}
	}

	seenFailures := make(map[string]bool)
	all := make([]recentEvent, 0, len(observations))
	for _, o := range observations {
		if o.At.Before(from) || !o.At.Before(now) {
			continue
		}
		if o.SoftFailure {
			key := strconv.FormatInt(o.AccountID, 10) + "\x00" + o.RequestID + "\x00" + strconv.FormatInt(o.At.UnixMilli(), 10)
			if seenFailures[key] {
				continue
			}
			seenFailures[key] = true
			all = append(all, recentEvent{at: o.At, accountID: o.AccountID, requestID: o.RequestID, softFail: true, kind: o.Kind, category: o.Category})
			continue
		}
		if o.Success && (o.RequestID == "" || !failedRequests[requestKey{o.AccountID, o.RequestID}]) {
			all = append(all, recentEvent{at: o.At, accountID: o.AccountID, requestID: o.RequestID})
		}
	}
	if len(all) == 0 {
		return nil
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].at.Equal(all[j].at) {
			if all[i].accountID == all[j].accountID {
				return all[i].requestID < all[j].requestID
			}
			return all[i].accountID < all[j].accountID
		}
		return all[i].at.After(all[j].at)
	})
	if len(all) > AccountRecentFailureMaxEvents {
		all = all[:AccountRecentFailureMaxEvents]
	}

	stats := &AccountRecentFailureStats{Calls: len(all)}
	earliest := all[0].at
	for _, event := range all {
		if event.at.Before(earliest) {
			earliest = event.at
		}
		if !event.softFail {
			continue
		}
		stats.Failures++
		switch {
		case event.kind == "first_output_timeout":
			stats.FirstOutputTimeouts++
		case event.kind == "stream_failed" || event.kind == "stream_error" || event.category == "STALL":
			stats.StreamFailures++
		default:
			stats.OtherFailures++
		}
	}
	stats.WilsonLow, stats.WilsonHigh = wilsonInterval(stats.Failures, stats.Calls)
	stats.SpanSeconds = spanSeconds(now, earliest)
	return stats
}

func recentFirstOutputStats(observations []GroupQualityObservation, now time.Time) *AccountRecentFirstOutputStats {
	from := now.Add(-AccountRecentSampleWindow)
	samples := make([]GroupQualityObservation, 0)
	for _, o := range observations {
		if o.Success && o.StrictContinuation && o.Streaming && o.FirstOutputMs > 0 && !o.At.Before(from) && o.At.Before(now) {
			samples = append(samples, o)
		}
	}
	samples = selectRecentObservations(samples, AccountRecentSampleMax)
	if len(samples) == 0 {
		return nil
	}
	values := make([]int, 0, len(samples))
	slow := 0
	earliest := samples[0].At
	for _, sample := range samples {
		values = append(values, sample.FirstOutputMs)
		if sample.FirstOutputMs >= 10000 {
			slow++
		}
		if sample.At.Before(earliest) {
			earliest = sample.At
		}
	}
	sort.Ints(values)
	return &AccountRecentFirstOutputStats{
		Samples: len(samples), P50Ms: percentileInt(values, 0.50), P90Ms: percentileInt(values, 0.90),
		SlowShare: float64(slow) / float64(len(samples)), SpanSeconds: spanSeconds(now, earliest),
	}
}

func recentCacheStats(observations []GroupQualityObservation, now time.Time) *AccountRecentCacheStats {
	from := now.Add(-AccountRecentSampleWindow)
	samples := make([]GroupQualityObservation, 0)
	for _, o := range observations {
		if o.Success && o.StrictContinuation && o.PromptTokens >= 2048 && o.PreviousPromptTokens >= 2048 && !o.At.Before(from) && o.At.Before(now) {
			samples = append(samples, o)
		}
	}
	samples = selectRecentObservations(samples, AccountRecentSampleMax)
	if len(samples) == 0 {
		return nil
	}
	stats := &AccountRecentCacheStats{Samples: len(samples)}
	earliest := samples[0].At
	for _, sample := range samples {
		if sample.At.Before(earliest) {
			earliest = sample.At
		}
		stats.ReadTokens += sample.CacheReadTokens
		stats.PromptTokens += sample.PromptTokens
		switch {
		case sample.CacheReadTokens == 0:
			stats.Zero++
		case float64(sample.CacheReadTokens)/float64(sample.PreviousPromptTokens) < 0.5:
			stats.Partial++
		case float64(sample.CacheReadTokens)/float64(sample.PreviousPromptTokens) < 0.9:
			stats.Mid++
		default:
			stats.Full++
		}
	}
	stats.Kept = stats.Mid + stats.Full
	stats.SpanSeconds = spanSeconds(now, earliest)
	return stats
}

func selectRecentObservations(observations []GroupQualityObservation, limit int) []GroupQualityObservation {
	sort.SliceStable(observations, func(i, j int) bool {
		if observations[i].At.Equal(observations[j].At) {
			return observations[i].RequestID > observations[j].RequestID
		}
		return observations[i].At.After(observations[j].At)
	})
	if len(observations) > limit {
		observations = observations[:limit]
	}
	return observations
}

func percentileInt(sorted []int, percentile float64) int {
	if len(sorted) == 0 {
		return 0
	}
	position := percentile * float64(len(sorted)-1)
	low, high := int(math.Floor(position)), int(math.Ceil(position))
	value := float64(sorted[low])
	if high > low {
		value += (float64(sorted[high]) - value) * (position - float64(low))
	}
	return int(math.Round(value))
}

func wilsonInterval(failures, calls int) (float64, float64) {
	if calls <= 0 {
		return 0, 0
	}
	const z = 1.96
	n := float64(calls)
	p := float64(failures) / n
	z2 := z * z
	denominator := 1 + z2/n
	center := (p + z2/(2*n)) / denominator
	margin := z * math.Sqrt((p*(1-p)+z2/(4*n))/n) / denominator
	return math.Max(0, center-margin), math.Min(1, center+margin)
}

func spanSeconds(now, earliest time.Time) int {
	if earliest.IsZero() || !now.After(earliest) {
		return 0
	}
	return int(now.Sub(earliest).Seconds())
}
