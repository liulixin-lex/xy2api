//go:build unit

package service

import (
	"fmt"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

// Explicit benchmark only: no work is added to the default unit-test run.
// Both versions use exactly these synthetic states and 24 samples/model key.
func BenchmarkReadPublish(b *testing.B) {
	for _, stress := range []bool{false, true} {
		name := "representative"
		if stress {
			name = "six_restricted_per_account"
		}
		b.Run(name, func(b *testing.B) {
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			e := NewSchedQualityEngineService(nil, nil, nil, func() time.Time { return now }, nil)
			e.loaded = true
			e.members, e.policies = map[int64][]int64{}, map[int64]SchedPolicyRecord{}
			d := &schedQualityData{now: now, globalEnabled: true, samples: map[SchedAccountModel][]q.AttemptSample{}, ruleSamples: map[SchedAccountModel][]q.RuleBSample{}, ruleGroups: map[schedGroupModel][]q.RuleBSample{}}
			var states []q.LineState
			for account := int64(1); account <= 300; account++ {
				group := (account-1)%10 + 1
				e.members[group] = append(e.members[group], account)
				e.policies[group] = SchedPolicyRecord{Policy: q.Policy{Enabled: true, TTFTSeconds: 1, MinCacheRatio: .8, MaxErrorRate: .05}}
				models := 2
				if account <= 34 {
					models++
				}
				for m := 0; m < models; m++ {
					model := fmt.Sprintf("synthetic-%02d", (int(account)*2+m)%12)
					pair := SchedAccountModel{account, model}
					for sample := 0; sample < 24; sample++ {
						at := now.Add(-time.Duration(sample+1) * time.Minute)
						id := fmt.Sprintf("a%d-m%d-r%d", account, m, sample)
						r := q.AttemptSample{AccountID: account, GroupID: group, Model: model, AttemptID: id, RequestID: id, SessionKey: fmt.Sprintf("session-%d", sample%3), StartedAt: at.Add(-time.Second), FinishedAt: at, InsertedAt: at, Stream: true, Outcome: q.OutcomeOK, TTFOMs: new(1500), Continuation: true, InputTokens: new(30), CacheReadTokens: new(70)}
						d.samples[pair] = append(d.samples[pair], r)
						d.ruleSamples[pair] = append(d.ruleSamples[pair], ruleBSample(r))
						key := schedGroupModel{group, model}
						d.ruleGroups[key] = append(d.ruleGroups[key], ruleBSample(r))
					}
					for _, line := range q.Lines {
						state := q.LineState{LineKey: q.LineKey{AccountID: account, GroupID: group, Model: model, Line: line}}
						if (!stress && len(states) < 100) || (stress && m < 2) {
							state.Identity, state.Until = q.IdentityRear, now.Add(time.Hour)
							_, basis := e.assessLine(d, state, q.EventRegular, now, nil)
							state.Basis = &basis
						}
						states = append(states, state)
					}
				}
			}
			if len(states) != 1902 {
				b.Fatalf("states=%d", len(states))
			}
			e.publish(d, states, nil, nil, now)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e.publish(d, states, nil, nil, now)
			}
		})
	}
}
