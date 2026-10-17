package resilience

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMetricsWindowAndRetention(t *testing.T) {
	m := &metricStore{}
	now := time.Date(2026, 10, 15, 12, 0, 30, 0, time.UTC)
	m.add(MetricKey{Name: MetricLastResort, GroupID: 7, AccountID: 1}, now.Add(-90*time.Minute))
	m.add(MetricKey{Name: MetricLastResort, GroupID: 7, AccountID: 1}, now.Add(-30*time.Minute))
	m.add(MetricKey{Name: MetricLastResort, GroupID: 7, AccountID: 1}, now.Add(-5*time.Minute))
	m.add(MetricKey{Name: MetricUndelivered, GroupID: 7, Label: StageHeaderWait}, now)
	m.add(MetricKey{Name: MetricUndelivered, GroupID: 7, Label: StageHeaderWait}, now)

	rows := m.snapshot(10*time.Minute, now)
	require.Equal(t, []MetricRow{
		{MetricKey: MetricKey{Name: MetricLastResort, GroupID: 7, AccountID: 1}, Count: 1},
		{MetricKey: MetricKey{Name: MetricUndelivered, GroupID: 7, Label: StageHeaderWait}, Count: 2},
	}, rows)
	rows = m.snapshot(2*time.Hour, now)
	require.Equal(t, int64(2), rows[0].Count, "older than the retention is dropped")
}

func TestMetricsBoundedKeys(t *testing.T) {
	m := &metricStore{}
	now := time.Now()
	for i := 0; i < maxMetricKeysPerBucket+10; i++ {
		m.add(MetricKey{Name: MetricStall, AccountID: int64(i)}, now)
	}
	rows := m.snapshot(time.Minute, now)
	require.Len(t, rows, maxMetricKeysPerBucket+1)
	var overflow int64
	for _, r := range rows {
		if r.Label == "overflow" {
			overflow = r.Count
		}
	}
	require.Equal(t, int64(10), overflow)
}

func TestCDNCutBucket(t *testing.T) {
	require.Equal(t, "90s", CDNCutBucket(90*time.Second))
	require.Equal(t, "125s", CDNCutBucket(125500*time.Millisecond))
	require.Equal(t, "300s", CDNCutBucket(301*time.Second))
	require.Equal(t, "other", CDNCutBucket(40*time.Second))
}
