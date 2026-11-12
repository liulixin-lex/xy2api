//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

// G2's full-window workload: 400 pairs, 80,000 samples, 3,600 memberships,
// 720 existing states, 20 overrides, 40 tests and 15 overlapping ledger rows.
func newSchedQualityBenchmark(b *testing.B) (*SchedQualityEngineService, time.Time) {
	b.Helper()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	store := &fakeSchedQualityStore{Members: make(map[int64][]int64), Policies: make(map[int64]SchedPolicyRecord), Now: func() time.Time { return now }}
	runner := &fakeSchedTestRunner{Records: make(map[SchedAccountModel][]SchedTestRecord)}
	ledger := &fakeSchedAttemptStore{}
	e := NewSchedQualityEngineService(store, ledger, runner, &qualitySwitches{true, true}, func() time.Time { return now })
	for group := int64(1); group <= 90; group++ {
		store.Policies[group] = SchedPolicyRecord{GroupID: group, Policy: q.Policy{Enabled: true, TTFTSeconds: 10, MaxErrorRate: .05, MinCacheRatio: .8}}
		for member := int64(0); member < 40; member++ {
			account := ((group-1)*40+member)%400 + 1
			store.Members[group] = append(store.Members[group], account)
			if group <= 18 {
				row := q.StateRow{AccountID: account, Model: "gpt-6.1-sol", GroupID: group, Since: now.Add(-time.Hour)}
				if group > 9 {
					row.State = q.StateCooling
					row.Reason = q.ReasonSpeed
					row.Level = 1
					row.Until = now.Add(time.Minute)
				}
				store.Rows = append(store.Rows, row)
			}
		}
	}
	for account := int64(1); account <= 400; account++ {
		for i := 0; i < 200; i++ {
			ms, prompt, prev, cached := 1000, 10000, 8000, 8000
			age := 90*time.Second + time.Duration(i)*(5*time.Hour-90*time.Second)/199
			sample := q.AttemptSample{AttemptID: fmt.Sprintf("%d-%03d", account, i), AccountID: account, Model: "gpt-6.1-sol", StartedAt: now.Add(-age), FinishedAt: now.Add(-age + time.Second), Class: q.ClassRegular, Stream: true, Warm: true, Outcome: q.OutcomeOK, TTFOMs: &ms, PromptTokens: &prompt, PrevPromptTokens: &prev, CacheReadTokens: &cached, SessionKey: fmt.Sprint(i % 10), InsertedAt: now}
			e.Observe(sample)
			if account <= 15 && i == 0 {
				ledger.Rows = append(ledger.Rows, sample)
			}
		}
		if account <= 20 {
			store.OverrideRows = append(store.OverrideRows, SchedOverride{AccountID: account, Mode: q.OverrideExempt})
		}
		if account <= 40 {
			r := qualityTest(now.Add(-10*time.Minute), 0, false)
			r.AccountID = account
			runner.Records[SchedAccountModel{account, r.Model}] = []SchedTestRecord{r}
		}
	}
	e.loaded = true
	e.insertedAt = now
	if err := e.cycle(context.Background()); err != nil {
		b.Fatal(err)
	}
	if len(e.Snapshot().Evidence) != 400 || len(store.Rows) != 720 {
		b.Fatal("unexpected warm fixture", len(e.Snapshot().Evidence), len(store.Rows))
	}
	return e, now
}

func BenchmarkSchedQualityCycleG2(b *testing.B) {
	e, _ := newSchedQualityBenchmark(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := e.cycle(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSchedQualityWindowG2(b *testing.B) {
	e, now := newSchedQualityBenchmark(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.window.take(now)
	}
}
