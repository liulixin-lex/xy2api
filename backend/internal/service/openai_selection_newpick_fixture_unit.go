//go:build unit

package service

import (
	"fmt"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

// InstallNewPickSnapshotForTest gives handler tests published short and long
// snapshots with enough recent sessions for account 2 to improve on account 1.
func InstallNewPickSnapshotForTest(s *OpenAIGatewayService, group int64, model string) {
	now := time.Now().UTC()
	policies := map[int64]SchedPolicyRecord{group: {Policy: schedquality.Policy{Enabled: true, TTFTSeconds: 2, TakeoverIdleSeconds: -1}}}
	facts := make(map[string]SchedPerfFact)
	for account, ttfo := range map[int64]int{1: 3000, 2: 1000} {
		for i := range 24 {
			id := fmt.Sprintf("%d-%d", account, i)
			facts[id] = SchedPerfFact{
				ID: id, Account: account, Group: group, Model: model, Session: id,
				First: true, Stream: true, TTFO: ttfo, Outcome: schedquality.OutcomeOK,
				Start: now.Add(-time.Minute).UnixMicro(), Finish: now.Add(-time.Second).UnixMicro(),
			}
		}
	}
	engine := NewSchedQualityEngineService(nil, nil, nil, nil, nil)
	engine.snapshot.Store(&SchedQualitySnapshot{View: &schedQualityView{at: now, globalEnabled: true, policies: policies}})
	engine.performance.Store(buildSchedPerf(facts, nil, policies, nil, now))
	ProvideOpenAISelectionBoard(s, engine)
}
