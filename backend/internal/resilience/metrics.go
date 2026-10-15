package resilience

import (
	"sort"
	"sync"
	"time"
)

// Resilience metrics (design §12). Counters live in this process, in
// one-minute buckets for the last MetricsRetention; the stats endpoint sums
// them over a window. Multi-instance deployments report per instance.

// Metric names.
const (
	MetricKeepaliveCommitted = "keepalive_committed"
	MetricFirstOutputTimeout = "first_output_timeout"
	MetricBudgetExhausted    = "budget_exhausted"
	MetricLastResort         = "last_resort"
	MetricFallbackGroup      = "fallback_group"
	MetricFallbackReturn     = "fallback_return"
	MetricSessionRebind      = "session_rebind"
	MetricUndelivered        = "undelivered"
	MetricCDNCut             = "cdn_cut"
	MetricFastFail           = "fast_fail"
	MetricAbuseGuard         = "abuse_guard"
	MetricStall              = "stall"
)

// MetricsRetention bounds the history kept for the stats endpoint.
const MetricsRetention = time.Hour

const maxMetricKeysPerBucket = 4096

// MetricKey identifies one counter.
type MetricKey struct {
	Name      string `json:"name"`
	GroupID   int64  `json:"group_id,omitempty"`
	AccountID int64  `json:"account_id,omitempty"`
	Label     string `json:"label,omitempty"`
}

// MetricRow is a counter summed over a window.
type MetricRow struct {
	MetricKey
	Count int64 `json:"count"`
}

type metricBucket struct {
	minute int64
	counts map[MetricKey]int64
}

type metricStore struct {
	mu      sync.Mutex
	buckets []metricBucket
}

var metrics = &metricStore{}

// Count adds one to a counter.
func Count(name string, groupID, accountID int64, label string) {
	metrics.add(MetricKey{Name: name, GroupID: groupID, AccountID: accountID, Label: label}, time.Now())
}

func (m *metricStore) add(key MetricKey, now time.Time) {
	minute := now.Unix() / 60
	m.mu.Lock()
	defer m.mu.Unlock()
	if n := len(m.buckets); n == 0 || m.buckets[n-1].minute != minute {
		m.buckets = append(m.buckets, metricBucket{minute: minute, counts: map[MetricKey]int64{}})
		cut := minute - int64(MetricsRetention/time.Minute)
		i := 0
		for i < len(m.buckets) && m.buckets[i].minute <= cut {
			i++
		}
		m.buckets = append([]metricBucket(nil), m.buckets[i:]...)
	}
	b := &m.buckets[len(m.buckets)-1]
	if _, ok := b.counts[key]; !ok && len(b.counts) >= maxMetricKeysPerBucket {
		// Bounded memory: fold the detail into a per-name total.
		key = MetricKey{Name: key.Name, Label: "overflow"}
	}
	b.counts[key]++
}

// Snapshot sums the counters of the last window (at most MetricsRetention).
func Snapshot(window time.Duration) []MetricRow {
	return metrics.snapshot(window, time.Now())
}

func (m *metricStore) snapshot(window time.Duration, now time.Time) []MetricRow {
	if window <= 0 || window > MetricsRetention {
		window = MetricsRetention
	}
	from := now.Add(-window).Unix() / 60
	sum := map[MetricKey]int64{}
	m.mu.Lock()
	for _, b := range m.buckets {
		if b.minute < from {
			continue
		}
		for k, v := range b.counts {
			sum[k] += v
		}
	}
	m.mu.Unlock()
	rows := make([]MetricRow, 0, len(sum))
	for k, v := range sum {
		rows = append(rows, MetricRow{MetricKey: k, Count: v})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		if rows[i].GroupID != rows[j].GroupID {
			return rows[i].GroupID < rows[j].GroupID
		}
		if rows[i].AccountID != rows[j].AccountID {
			return rows[i].AccountID < rows[j].AccountID
		}
		return rows[i].Label < rows[j].Label
	})
	return rows
}

// CDNCutBucket labels the duration of a disconnect before output with the
// CDN timeout it most likely hit (design §12: 90, 125 and 300 s), or
// "other".
func CDNCutBucket(elapsed time.Duration) string {
	sec := elapsed.Seconds()
	switch {
	case sec >= 85 && sec < 95:
		return "90s"
	case sec >= 120 && sec < 130:
		return "125s"
	case sec >= 290 && sec < 310:
		return "300s"
	}
	return "other"
}
