package service

import (
	"testing"
	"time"
)

func TestCacheTraceAsyncPublicationRequiresSettlementEpoch(t *testing.T) {
	for _, mode := range []string{"same", "deleted", "recreated", "missing_owner"} {
		t.Run(mode, func(t *testing.T) {
			s := &OpenAIGatewayService{}
			telemetry := s.cacheTelemetry()
			owner := new(cacheTraceSessionState)
			telemetry.sessions["s"] = openAICacheHistory{traceChains: owner, account: 7}
			state := &cacheTraceAsync{}
			settlement := &cacheTraceSettlement{prompt: 2048, at: time.Now(), owner: owner}
			state.settled.Store(settlement)
			state.result.Store(&cacheTraceComputed{chain: cachePrefixChain{hashes: []uint64{1}, blocks: 1}})
			var replacement *cacheTraceSessionState
			switch mode {
			case "deleted":
				delete(telemetry.sessions, "s")
			case "recreated":
				replacement = new(cacheTraceSessionState)
				telemetry.sessions["s"] = openAICacheHistory{traceChains: replacement, account: 8}
			case "missing_owner":
				settlement.owner = nil
			}
			state.publish(s, "s")
			switch mode {
			case "same":
				if owner.chains[0].prompt != 2048 {
					t.Fatal("live settlement failed to publish")
				}
			case "deleted":
				if len(telemetry.sessions) != 0 {
					t.Fatal("async publication resurrected evicted session")
				}
			case "recreated":
				if replacement.chains[0].blocks != 0 || telemetry.sessions["s"].account != 8 {
					t.Fatal("old async publication polluted replacement epoch")
				}
			case "missing_owner":
				if owner.chains[0].blocks != 0 {
					t.Fatal("publication without settlement ownership retained a chain")
				}
			}
		})
	}
}
