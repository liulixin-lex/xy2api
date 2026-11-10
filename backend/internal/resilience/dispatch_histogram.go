package resilience

import (
	"sort"
	"sync"
	"time"
)

const DispatchRemainingMetric = "resilience_dispatch_remaining_seconds"

var dispatchBounds = [...]float64{0, 1, 2, 5, 10, 30, 60, 120, 180, 240, 290}

type DispatchHistogram struct {
	Path       string `json:"path"`
	BodyBucket string `json:"body_bucket"`
	Committed  bool   `json:"committed"`
	// Buckets are cumulative, with the final +Inf count represented by Count.
	Buckets []DispatchHistogramBucket `json:"buckets"`
	Count   int64                     `json:"count"`
	Sum     float64                   `json:"sum"`
}
type DispatchHistogramBucket struct {
	LE    float64 `json:"le"`
	Count int64   `json:"count"`
}
type dispatchKey struct {
	path, body string
	committed  bool
}
type dispatchSample struct {
	counts [len(dispatchBounds)]int64
	count  int64
	sum    float64
}
type dispatchBucket struct {
	minute int64
	values map[dispatchKey]dispatchSample
}

var dispatchHist = struct {
	sync.Mutex
	buckets []dispatchBucket
}{}

func ObserveDispatchRemaining(path string, bodyBytes int64, committed bool, rem time.Duration, now time.Time) {
	switch path {
	case "direct", "plugin", "codex_ticket", "codex_models", "image_backfill", "grok_media", "grok_voice", "seedance", "model_catalog", "codex_proxy", "agent_identity":
	default:
		path = "other"
	}
	body := "lt1m"
	if bodyBytes >= 10<<20 {
		body = "ge10m"
	} else if bodyBytes >= 1<<20 {
		body = "1m_10m"
	}
	key := dispatchKey{path, body, committed}
	minute := now.Unix() / 60
	dispatchHist.Lock()
	defer dispatchHist.Unlock()
	if n := len(dispatchHist.buckets); n == 0 || dispatchHist.buckets[n-1].minute != minute {
		dispatchHist.buckets = append(dispatchHist.buckets, dispatchBucket{minute, make(map[dispatchKey]dispatchSample)})
		cut := minute - int64(MetricsRetention/time.Minute)
		i := 0
		for i < len(dispatchHist.buckets) && dispatchHist.buckets[i].minute <= cut {
			i++
		}
		dispatchHist.buckets = dispatchHist.buckets[i:]
	}
	bucket := &dispatchHist.buckets[len(dispatchHist.buckets)-1]
	sample := bucket.values[key]
	value := rem.Seconds()
	sample.count++
	sample.sum += value
	for i, bound := range dispatchBounds {
		if value <= bound {
			sample.counts[i]++
		}
	}
	bucket.values[key] = sample
}
func DispatchHistogramSnapshot(window time.Duration) []DispatchHistogram {
	if window <= 0 || window > MetricsRetention {
		window = MetricsRetention
	}
	from := time.Now().Add(-window).Unix() / 60
	sums := make(map[dispatchKey]dispatchSample)
	dispatchHist.Lock()
	for _, bucket := range dispatchHist.buckets {
		if bucket.minute < from {
			continue
		}
		for key, sample := range bucket.values {
			sum := sums[key]
			sum.count += sample.count
			sum.sum += sample.sum
			for i := range sample.counts {
				sum.counts[i] += sample.counts[i]
			}
			sums[key] = sum
		}
	}
	dispatchHist.Unlock()
	rows := make([]DispatchHistogram, 0, len(sums))
	for key, sum := range sums {
		row := DispatchHistogram{Path: key.path, BodyBucket: key.body, Committed: key.committed, Count: sum.count, Sum: sum.sum}
		for i, bound := range dispatchBounds {
			row.Buckets = append(row.Buckets, DispatchHistogramBucket{bound, sum.counts[i]})
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, z := rows[i], rows[j]
		if a.Path != z.Path {
			return a.Path < z.Path
		}
		if a.BodyBucket != z.BodyBucket {
			return a.BodyBucket < z.BodyBucket
		}
		return !a.Committed && z.Committed
	})
	return rows
}
