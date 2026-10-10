package servertiming

import (
	"context"
	"testing"
	"time"
)

func TestCollectorSummaryCountsRoundTrips(t *testing.T) {
	start := time.Now().Add(-time.Second)
	collector := New(start)
	ctx := WithCollector(context.Background(), collector)

	Record(ctx, MetricRedis, start, start.Add(10*time.Millisecond), 1)
	Record(ctx, MetricRedis, start.Add(5*time.Millisecond), start.Add(20*time.Millisecond), 3) // a pipeline
	RecordInterval(ctx, MetricDatabase, start, start.Add(time.Millisecond))

	redis := collector.Summary("Redis")
	if redis.RoundTrips != 2 || redis.Count != 4 || redis.Busy != 20*time.Millisecond {
		t.Fatalf("redis summary = %+v", redis)
	}
	if db := collector.Summary(MetricDatabase); db.RoundTrips != 1 || db.Count != 0 {
		t.Fatalf("db summary = %+v", db)
	}
	if empty := collector.Summary("missing"); empty != (Summary{}) {
		t.Fatalf("missing summary = %+v", empty)
	}
	var none *Collector
	if none.Summary(MetricRedis) != (Summary{}) {
		t.Fatal("a nil collector has no activity")
	}
}
