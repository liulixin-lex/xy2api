//go:build unit

package schedquality

import (
	"fmt"
	"testing"
	"time"
)

var evaluationNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
var evaluationScope = Scope{AccountID: 1, GroupID: 10, Model: "synthetic-model"}

func sampleInt(v int) *int { return &v }

func evaluationPolicy() Policy {
	return Policy{Enabled: true, TTFTSeconds: 1, MaxErrorRate: .05, MinCacheRatio: .8}
}

func evaluationSamples(n int, group int64, age time.Duration) []AttemptSample {
	rows := make([]AttemptSample, n)
	for i := range rows {
		finish := evaluationNow.Add(-age).Add(time.Duration(i) * time.Millisecond)
		rows[i] = AttemptSample{
			AttemptID: fmt.Sprintf("sample-%d-%d-%d", group, age, i), RequestID: fmt.Sprintf("request-%d-%d-%d", group, age, i),
			AccountID: evaluationScope.AccountID, Model: evaluationScope.Model, GroupID: group,
			StartedAt: finish.Add(-time.Second), FinishedAt: finish, Outcome: OutcomeOK,
			SessionKey: "synthetic-session", Stream: true, TTFOMs: sampleInt(500),
			PromptTokens: sampleInt(10), CacheReadTokens: sampleInt(80), CacheWriteTokens: sampleInt(10),
		}
	}
	return rows
}

func evaluateFixture(rows []AttemptSample) Evaluation {
	return Evaluate(rows, evaluationScope, evaluationPolicy(), time.Time{}, evaluationNow)
}

func TestEvaluateWilsonBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		n, bad      int
		errorMetric bool
		want        Verdict
	}{
		{"speed-20-4", 20, 4, false, VerdictPass},
		{"speed-20-5", 20, 5, false, VerdictFail},
		{"speed-30-6", 30, 6, false, VerdictPass},
		{"speed-30-7", 30, 7, false, VerdictFail},
		{"speed-50-9", 50, 9, false, VerdictPass},
		{"speed-50-10", 50, 10, false, VerdictFail},
		{"speed-100-15", 100, 15, false, VerdictPass},
		{"speed-100-16", 100, 16, false, VerdictFail},
		{"error-50-5", 50, 5, true, VerdictPass},
		{"error-50-6", 50, 6, true, VerdictFail},
		{"error-100-9", 100, 9, true, VerdictPass},
		{"error-100-10", 100, 10, true, VerdictFail},
		{"error-200-16", 200, 16, true, VerdictPass},
		{"error-200-17", 200, 17, true, VerdictFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := evaluationSamples(tc.n, 10, 10*time.Minute)
			for i := 0; i < tc.bad; i++ {
				if tc.errorMetric {
					rows[i].Outcome = OutcomeAccountFault
				} else {
					rows[i].TTFOMs = sampleInt(1001)
				}
			}
			e := evaluateFixture(rows)
			got := e.Verdicts.Speed
			if tc.errorMetric {
				got = e.Verdicts.Error
			}
			if got != tc.want {
				t.Fatalf("verdict = %v, want %v; %+v", got, tc.want, e)
			}
		})
	}
}

func TestEvaluateEvidenceCascade(t *testing.T) {
	for _, tc := range []struct {
		name                                           string
		localShort, localLong, sharedShort, sharedLong int
		source                                         EvidenceSource
		window                                         time.Duration
		n                                              int
	}{
		{"local-short-first", 20, 40, 100, 100, EvidenceLocal, 30 * time.Minute, 20},
		{"local-long-before-shared-short", 19, 1, 100, 100, EvidenceLocal, 60 * time.Minute, 20},
		{"shared-short", 10, 9, 10, 100, EvidenceShared, 30 * time.Minute, 20},
		{"shared-long", 10, 9, 9, 1, EvidenceShared, 60 * time.Minute, 29},
		{"insufficient-uses-last", 1, 1, 1, 1, EvidenceShared, 60 * time.Minute, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := evaluationSamples(tc.localShort, 10, 10*time.Minute)
			rows = append(rows, evaluationSamples(tc.localLong, 10, 40*time.Minute)...)
			rows = append(rows, evaluationSamples(tc.sharedShort, 20, 10*time.Minute)...)
			rows = append(rows, evaluationSamples(tc.sharedLong, 20, 40*time.Minute)...)
			e := evaluateFixture(rows)
			if e.Speed.Source != tc.source || e.Speed.Window != tc.window || e.Speed.N != tc.n {
				t.Fatalf("speed = %+v", e.Speed)
			}
		})
	}
	// Each metric chooses its own source and duration; speed's sufficient local
	// short window cannot force error or cache to use that same set.
	rows := evaluationSamples(20, 10, 10*time.Minute)
	old := evaluationSamples(30, 10, 90*time.Minute)
	for i := range old {
		old[i].Stream = false
	}
	rows = append(rows, old...)
	cache := evaluationSamples(30, 20, 10*time.Minute)
	for i := range cache {
		cache[i].Continuation = true
	}
	rows = append(rows, cache...)
	e := evaluateFixture(rows)
	if e.Speed.Source != EvidenceLocal || e.Speed.Window != 30*time.Minute ||
		e.Error.Source != EvidenceLocal || e.Error.Window != 120*time.Minute ||
		e.Cache.Source != EvidenceShared || e.Cache.Window != 60*time.Minute {
		t.Fatalf("dimensions did not choose independently: %+v", e)
	}
}

func TestEvaluateFastPath(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		local, localSlow, shared, sharedSlow int
		want                                 Verdict
		source                               EvidenceSource
	}{
		{"five-of-six", 6, 5, 0, 0, VerdictFail, EvidenceLocal},
		{"four-of-six", 6, 4, 0, 0, VerdictNoData, EvidenceLocal},
		{"five-is-not-six", 5, 5, 0, 0, VerdictNoData, EvidenceShared},
		{"enough-local-ignores-bad-shared", 6, 0, 6, 6, VerdictNoData, EvidenceLocal},
		{"short-local-merges", 5, 4, 1, 1, VerdictFail, EvidenceShared},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := evaluationSamples(tc.local, 10, time.Minute)
			shared := evaluationSamples(tc.shared, 20, time.Minute)
			for i := 0; i < tc.localSlow; i++ {
				local[i].TTFOMs = sampleInt(1001)
			}
			for i := 0; i < tc.sharedSlow; i++ {
				shared[i].Outcome, shared[i].TTFOMs = OutcomeTimeout, nil
			}
			e := evaluateFixture(append(local, shared...))
			if e.Verdicts.Speed != tc.want || e.Fast.Source != tc.source {
				t.Fatalf("fast = %+v, verdict = %v", e.Fast, e.Verdicts.Speed)
			}
		})
	}
	rows := evaluationSamples(12, 10, time.Minute)
	for i := 0; i < 6; i++ {
		rows[i].TTFOMs = sampleInt(2000)
	}
	e := evaluateFixture(rows)
	if e.Fast.N != 6 || e.Fast.Slow != 0 || e.Verdicts.Speed != VerdictNoData {
		t.Fatalf("old burst was reused: %+v", e)
	}
}

func TestEvaluateSampleEligibility(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		mutate                   func(*AttemptSample)
		speed, slow, errors, bad int
	}{
		{"cold-stream-counts", func(s *AttemptSample) { s.Warm = false }, 1, 0, 1, 0},
		{"class-does-not-scale", func(s *AttemptSample) { s.Class = ClassXHeavy; s.TTFOMs = sampleInt(1001) }, 1, 1, 1, 0},
		{"non-stream", func(s *AttemptSample) { s.Stream = false }, 0, 0, 1, 0},
		{"first-output-timeout", func(s *AttemptSample) { s.Outcome = OutcomeTimeout; s.TTFOMs = nil }, 1, 1, 1, 1},
		{"after-output-failure", func(s *AttemptSample) { s.Outcome = OutcomeAccountFault }, 1, 0, 1, 1},
		{"other-is-error", func(s *AttemptSample) { s.Outcome = OutcomeOther }, 1, 0, 1, 1},
		{"canceled-excludes-error-only", func(s *AttemptSample) { s.ClientCanceled = true; s.Outcome = OutcomeOther }, 1, 0, 0, 0},
		{"exact-limit", func(s *AttemptSample) { s.TTFOMs = sampleInt(1000) }, 1, 0, 1, 0},
		{"wrong-account", func(s *AttemptSample) { s.AccountID++ }, 0, 0, 0, 0},
		{"wrong-model", func(s *AttemptSample) { s.Model = "other-model" }, 0, 0, 0, 0},
		{"unfinished", func(s *AttemptSample) { s.FinishedAt = time.Time{} }, 0, 0, 0, 0},
		{"future-completion", func(s *AttemptSample) { s.FinishedAt = evaluationNow.Add(time.Second) }, 0, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := evaluationSamples(1, 10, time.Minute)
			tc.mutate(&rows[0])
			e := evaluateFixture(rows)
			if e.Speed.N != tc.speed || e.Speed.Slow != tc.slow || e.Error.N != tc.errors || e.Error.Bad != tc.bad {
				t.Fatalf("unexpected counts: %+v", e)
			}
		})
	}
	rows := evaluationSamples(250, 10, time.Minute)
	for i := range rows {
		rows[i].Outcome = OutcomeOther
	}
	e := evaluateFixture(append(rows, rows...))
	if e.Error.N != 250 || e.Error.Bad != 250 || e.Verdicts.Error != VerdictFail {
		t.Fatalf("session/sample cap or duplicate: %+v", e.Error)
	}
}

func TestEvaluateWindowAndResetBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		age  time.Duration
		n    int
	}{
		{"long-endpoint", 60 * time.Minute, 20},
		{"outside-long-window", 60*time.Minute + time.Nanosecond, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := evaluationSamples(20, 10, tc.age)
			for i := range rows {
				rows[i].FinishedAt = evaluationNow.Add(-tc.age)
				rows[i].StartedAt = rows[i].FinishedAt.Add(-time.Second)
			}
			if got := evaluateFixture(rows).Speed.N; got != tc.n {
				t.Fatalf("count = %d, want %d", got, tc.n)
			}
		})
	}
	cut := evaluationNow.Add(-time.Minute)
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		rows := evaluationSamples(1, 10, time.Second)
		rows[0].StartedAt = cut.Add(delta)
		e := Evaluate(rows, evaluationScope, evaluationPolicy(), cut, evaluationNow)
		want := 0
		if delta > 0 {
			want = 1
		}
		if e.Error.N != want {
			t.Fatalf("dispatch offset %v: %+v", delta, e.Error)
		}
	}
	rows := evaluationSamples(1, 10, time.Minute)
	rows[0].StartedAt = evaluationNow.Add(-3 * time.Hour)
	if e := evaluateFixture(rows); e.Error.N != 1 {
		t.Fatal("window must use completion, not dispatch")
	}
}

func TestEvaluateErrorThresholdFloorAndStrictComparison(t *testing.T) {
	rows := evaluationSamples(50, 10, 10*time.Minute)
	for i := 0; i < 5; i++ {
		rows[i].Outcome = OutcomeAccountFault
	}
	p := evaluationPolicy()
	p.MaxErrorRate = .01
	if e := Evaluate(rows, evaluationScope, p, time.Time{}, evaluationNow); e.Verdicts.Error != VerdictPass {
		t.Fatal("minimum error line is 5%")
	}
	rows[5].Outcome = OutcomeAccountFault
	p.MaxErrorRate = WilsonLower(6, 50)
	if e := Evaluate(rows, evaluationScope, p, time.Time{}, evaluationNow); e.Verdicts.Error != VerdictPass {
		t.Fatal("LCB equal to line must pass")
	}
	p.Enabled = false
	if e := Evaluate(rows, evaluationScope, p, time.Time{}, evaluationNow); e.Reason() != "" {
		t.Fatal("disabled group triggered")
	}
}
