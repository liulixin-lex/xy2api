package servertiming

import "time"

// Summary is the activity recorded for one metric.
type Summary struct {
	// RoundTrips counts the blocking calls; a Redis pipeline is one.
	RoundTrips int
	// Count is the number of operations, e.g. commands or queries.
	Count int64
	// Busy is the time spent in the calls, overlapping calls merged.
	Busy time.Duration
}

// Summary returns what has been recorded for a metric so far.
func (c *Collector) Summary(name string) Summary {
	if c == nil {
		return Summary{}
	}
	name = normalizeMetricName(name)
	c.mu.Lock()
	var out Summary
	var intervals []interval
	if m := c.metrics[name]; m != nil {
		out.RoundTrips, out.Count = len(m.intervals), m.count
		intervals = append(intervals, m.intervals...)
	}
	c.mu.Unlock()
	out.Busy = unionDuration(intervals, c.startedAt, time.Now())
	return out
}
