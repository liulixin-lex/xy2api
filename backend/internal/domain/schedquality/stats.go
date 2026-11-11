package schedquality

import (
	"math"
	"sort"
	"time"
)

func WilsonLower(bad, n int) float64 {
	if bad == 0 || n == 0 {
		return 0
	}
	p := float64(bad) / float64(n)
	z2 := WilsonZ * WilsonZ
	return (p + z2/(2*float64(n)) - WilsonZ*math.Sqrt((p*(1-p)+z2/(4*float64(n)))/float64(n))) / (1 + z2/float64(n))
}

// Both scale tables have exact hundredths; round upward without float division.
func normalizedMs(ms int, scale float64) int {
	k := int64(math.Round(scale * 100))
	return int((int64(ms)*100 + k - 1) / k)
}

func speedLimitMs(p Policy) int { return int(math.Round(p.TTFTSeconds * 1000)) }

type latency struct {
	ms      int
	timeout bool
}

func sortLatencies(values []latency) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].timeout != values[j].timeout {
			return !values[i].timeout
		}
		return values[i].ms < values[j].ms
	})
}

// values must be ascending; timeouts sort as positive infinity.
func p90(values []latency) latency {
	if len(values) == 0 {
		return latency{}
	}
	return values[(9*len(values)+9)/10-1]
}

func failBound(values []latency) latency {
	for b := 1; b <= len(values); b++ {
		if WilsonLower(b, len(values)) > SlowShareTolerance {
			return values[len(values)-b]
		}
	}
	return latency{}
}

type sampleSession struct {
	key     string
	request bool
}

func sessionOf(s AttemptSample) sampleSession {
	if s.SessionKey != "" {
		return sampleSession{key: s.SessionKey}
	}
	if s.RequestID == "" {
		return sampleSession{key: s.AttemptID, request: true}
	}
	return sampleSession{key: s.RequestID, request: true}
}

func evidenceOutcome(o Outcome) bool {
	return o == OutcomeOK || o == OutcomeTimeout || o == OutcomeAccountFault
}

func sortedSamples(samples []AttemptSample, since, now time.Time) []AttemptSample {
	seen := make(map[string]bool, len(samples))
	filtered := make([]AttemptSample, 0, len(samples))
	for _, s := range samples {
		if seen[s.AttemptID] || !s.StartedAt.After(since) || s.StartedAt.Before(now.Add(-WindowMaxAge)) {
			continue
		}
		seen[s.AttemptID] = true
		filtered = append(filtered, s)
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].StartedAt.Equal(filtered[j].StartedAt) {
			return filtered[i].AttemptID > filtered[j].AttemptID
		}
		return filtered[i].StartedAt.After(filtered[j].StartedAt)
	})
	return filtered
}
