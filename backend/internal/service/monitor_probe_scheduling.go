package service

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/monitorprobe"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
)

// A channel monitor probe (monitorprobe.IsProbe) measures the model on a free
// account, not the queue of a busy one. Its question differs every time, so
// it has no conversation to keep either. The schedulers treat a verified
// probe, and only a probe, like this:
//
//   - it has no sticky session: no binding is read or written, so a probe is
//     never pinned to a bound account and never waits for one;
//   - before a selection plans a wait, the probe tries the candidates of the
//     selection by their real concurrency and takes a free slot if there is
//     one. The load rate a selection ranks by counts in-flight requests
//     against the load factor, so an account whose load factor is below its
//     concurrency counts as full while it still has free slots. Only a group
//     that is really full makes a probe wait.
//
// Every other request is scheduled exactly as before.

// monitorProbeSessionHash drops the sticky session of a probe.
func monitorProbeSessionHash(ctx context.Context, sessionHash string) string {
	if monitorprobe.IsProbe(ctx) {
		return ""
	}
	return sessionHash
}

// monitorProbeRequest reports whether the request of c is a probe.
func monitorProbeRequest(c *gin.Context) bool {
	return c != nil && c.Request != nil && monitorprobe.IsProbe(c.Request.Context())
}

// openAIMonitorProbeFreeSlot is the extra pass of a probe in the OpenAI load-aware
// selection, after the passes by load rate found no slot and before the
// fallback wait. tryAcquire is that selection's pass over its candidates; an
// empty load map ranks every candidate as idle, so the pass tries each one,
// with the usual freshness checks, for a slot within its concurrency.
func openAIMonitorProbeFreeSlot(ctx context.Context, tryAcquire func(map[int64]*AccountLoadInfo) (*AccountSelectionResult, bool, error)) (*AccountSelectionResult, error) {
	if !monitorprobe.IsProbe(ctx) {
		return nil, nil
	}
	selection, _, err := tryAcquire(nil)
	return selection, err
}

// monitorProbeFreeSlot is the extra pass of a probe in the Anthropic
// load-aware selection: before the fallback wait it tries every candidate in
// priority order for a slot within its concurrency.
func (s *GatewayService) monitorProbeFreeSlot(ctx context.Context, candidates []*Account, groupID *int64, preferOAuth bool) (*AccountSelectionResult, bool, error) {
	if !monitorprobe.IsProbe(ctx) {
		return nil, false, nil
	}
	return s.tryAcquireByLegacyOrder(ctx, candidates, groupID, "", preferOAuth)
}

// monitorProbeRoutedWait returns the routed accounts a request may wait on
// when model routing found no free slot. A probe waits on none of them: it
// goes on to the whole group, as a request does whose routed accounts are all
// full by load rate, and takes a free slot there before any wait.
func monitorProbeRoutedWait(ctx context.Context, routed []accountWithLoad) []accountWithLoad {
	if monitorprobe.IsProbe(ctx) {
		return nil
	}
	return routed
}

// monitorProbeAcquire is the extra pass of a probe in share scheduling,
// before the fallback wait: the plan's steps again, without the load gate,
// so a step skipped as saturated is tried for a slot within its limit. Plans
// without the load gate (last resort) have tried every step already.
func (e *shareExecutor) monitorProbeAcquire(d sharesched.Decision) *shareOutcome {
	if !e.loadGate || !monitorprobe.IsProbe(e.ctx) {
		return nil
	}
	e.loadGate = false
	defer func() { e.loadGate = true }()
	return e.acquire(d)
}
