package resilience

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestG9MetricOverflowKeepsFourBoundedLabels(t *testing.T) {
	m := &metricStore{}
	now := time.Now()
	for i := 0; i < maxMetricKeysPerBucket; i++ {
		m.add(MetricKey{Name: "other", GroupID: int64(i + 1)}, now)
	}
	labels := []string{"false:arrival", "true:arrival", "false:body_eof", "true:body_eof"}
	for i := 0; i < 100; i++ {
		for _, label := range labels {
			m.add(MetricKey{Name: "late_attempt_start", GroupID: int64(i + 1), AccountID: int64(i + 10), Label: label}, now)
		}
	}
	counts := map[string]int64{}
	for _, r := range m.snapshot(time.Minute, now) {
		if r.Name == "late_attempt_start" {
			require.Zero(t, r.GroupID)
			require.Zero(t, r.AccountID)
			counts[r.Label] += r.Count
		}
	}
	require.Equal(t, map[string]int64{"false:arrival": 100, "true:arrival": 100, "false:body_eof": 100, "true:body_eof": 100}, counts)
	require.Len(t, m.buckets[0].counts, maxMetricKeysPerBucket+4, "only four finite labels can bypass the detail cap")
	for i := 0; i < 20; i++ {
		m.add(MetricKey{Name: "late_attempt_start", GroupID: int64(i + 1), Label: "unrecognized"}, now)
	}
	require.EqualValues(t, 20, m.buckets[0].counts[MetricKey{Name: "late_attempt_start", Label: "overflow"}])
	require.Len(t, m.buckets[0].counts, maxMetricKeysPerBucket+5, "unknown labels retain the ordinary bounded overflow")
}

func TestG9MetricOverflowStillSignalsWithWarningsLost(t *testing.T) {
	old := metrics
	metrics = &metricStore{}
	defer func() { metrics = old }()
	for i := 0; i < maxMetricKeysPerBucket; i++ {
		Count("other", int64(i+1), 0, "filled")
	}
	now := time.Now()
	cfg := Defaults()
	cfg.Budget.BudgetStart = BudgetStartBodyEOF
	st := NewRequestState()
	st.SetGroup(96001)
	st.SetBudget(NewBudget(context.Background(), &cfg, KindText, now.Add(-239*time.Second)))
	st.SetAttemptProfile(AttemptProfile{AttemptStart: now})
	st.ObserveAttemptStart()
	var signal int64
	for _, r := range Snapshot(time.Hour) {
		if r.Name == "late_attempt_start" && r.Label == "false:body_eof" {
			signal += r.Count
			require.Zero(t, r.GroupID)
		}
	}
	// No Warn or SQL result is needed: sink loss plus this positive signal
	// still satisfies T-b when both relevant warnings have disappeared.
	require.EqualValues(t, 1, signal)
}
