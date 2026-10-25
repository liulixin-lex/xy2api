package service

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestCacheTraceReviewHistoryCutoffUsesDispatchTime(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		historyAfterSend, missingSend bool
		wantRef                       string
	}{
		{name: "settled_between_build_and_dispatch", wantRef: "prev1"},
		{name: "settled_after_dispatch", historyAfterSend: true, wantRef: "none"},
		{name: "adapter_without_send_retains_build_cutoff", missingSend: true, wantRef: "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := rejectedTraceFixture(t, 32)
			body := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"synthetic-cutoff-body"}]}`)
			baseline := s.computeCacheTrace(body, body, 1, false, "cutoff", "responses", "http", s.cachePolicies.Snapshot())
			if len(baseline.chain.hashes) == 0 {
				t.Fatal("fixture did not produce a comparable chain")
			}
			start := time.Now()
			chain := baseline.chain
			chain.prompt = 2048
			chain.settledAt = start.Add(time.Second)
			if tc.historyAfterSend {
				chain.settledAt = start.Add(3 * time.Second)
			}
			owner := &cacheTraceSessionState{chains: [cachePrefixHistoryCount]cachePrefixChain{chain}}
			s.cacheTelemetry().sessions["cutoff"] = openAICacheHistory{traceChains: owner}
			state := &cacheTraceAsync{}
			job := &cacheTraceJob{policy: s.cachePolicies.Snapshot(), service: s, state: state, ingress: body, apiKeyID: 1,
				session: "cutoff", protocol: "responses", transport: "http", size: int64(len(body)), history: owner,
				created: start, sentAt: start.Add(2 * time.Second),
				getBody: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }}
			if tc.missingSend {
				job.sentAt = time.Time{}
			}
			job.compute()
			got := state.result.Load()
			if got == nil || got.prefix.ForkRef != tc.wantRef {
				t.Fatalf("history cutoff mismatch: got %#v, want %s", got, tc.wantRef)
			}
			if tc.wantRef == "prev1" && (got.prefix.HistoryUnsampled || got.prefix.PreviousPrompt != 2048) {
				t.Fatalf("pre-dispatch history was treated as unavailable: %#v", got.prefix)
			}
		})
	}
}
