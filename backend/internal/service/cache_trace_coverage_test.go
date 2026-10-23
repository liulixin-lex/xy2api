package service

import (
	"bytes"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"log/slog"
	"testing"
	"time"
)

func TestCacheTraceCoverageReasons(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		excluded             bool
		reason               uint32
		ready, sampled, hist bool
		want                 string
	}{
		{"percent", true, cacheTraceBudgetFull, false, false, false, "chain_percent"},
		{"budget", false, cacheTraceBudgetFull, false, false, false, "budget_full"},
		{"expired", false, cacheTraceExpired, false, false, false, "expired"},
		{"pending", false, 0, false, false, false, "not_ready"},
		{"worker_percent", false, 0, true, false, false, "chain_percent"},
		{"history", false, 0, true, true, true, "hist_unavailable"},
		{"complete", false, 0, true, true, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &cacheTraceAsync{chainExcluded: tc.excluded}
			a.reason.Store(tc.reason)
			if tc.ready {
				a.result.Store(&cacheTraceComputed{prefix: cachePrefixObservation{Sampled: tc.sampled, HistoryUnsampled: tc.hist}})
			}
			require.Equal(t, tc.want, a.naReason())
			for _, class := range []string{"n/a_sampled_out", "n/a_usage", "n/a_plugin", "full"} {
				var out bytes.Buffer
				old := slog.Default()
				slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
				tr := &cacheTrace{data: cacheTraceData{LossClass: class, NAReason: tc.want}, ingress: []byte("private-body-marker")}
				tr.emitCacheTrace()
				slog.SetDefault(old)
				var event map[string]any
				require.NoError(t, json.Unmarshal(out.Bytes(), &event))
				if class == "n/a_sampled_out" {
					require.Equal(t, tc.want, event["na_reason"])
				} else {
					require.NotContains(t, event, "na_reason")
				}
				require.NotContains(t, out.String(), "private-body-marker")
			}
		})
	}
}
func TestCacheTraceQueueWaitAndEviction(t *testing.T) {
	s := &OpenAIGatewayService{}
	histories := new(cacheTraceSessionState)
	telemetry := s.cacheTelemetry()
	telemetry.sessions["private-session"] = openAICacheHistory{traceChains: histories}
	job := &cacheTraceJob{service: s, policy: s.cachePolicies.Snapshot(), session: "private-session", history: histories}
	require.False(t, job.historyEvicted())
	delete(telemetry.sessions, "private-session")
	require.True(t, job.historyEvicted())
	telemetry.sessions["private-session"] = openAICacheHistory{traceChains: new(cacheTraceSessionState)}
	require.True(t, job.historyEvicted(), "reinserted same key is a different history")
	job.history = nil
	require.False(t, job.historyEvicted(), "new session is cold, not evicted")
	a := &cacheTraceAsync{}
	pool := &cacheTraceWorkers{budget: 4096, workers: 1, maxQueueWait: time.Millisecond}
	require.True(t, pool.submit(&cacheTraceJob{state: a, queuedAt: time.Now().Add(-time.Second)}))
	require.Eventually(t, func() bool { return a.reason.Load() == cacheTraceExpired }, time.Second, time.Millisecond)
	require.Equal(t, "expired", a.naReason())
}
