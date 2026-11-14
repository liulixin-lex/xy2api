//go:build unit

package schedquality

import (
	"math"
	"testing"
	"time"
)

func TestEvaluateCacheRules(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		n, sessions, lowSessions, read int
		want                           Verdict
	}{
		{"two-low-sessions", 30, 2, 2, 76, VerdictFail},
		{"one-low-session", 30, 1, 1, 0, VerdictPass},
		{"only-one-of-two-low", 30, 2, 1, 0, VerdictPass},
		{"exact-margin", 30, 2, 2, 77, VerdictPass},
		{"below-minimum-turns", 29, 2, 2, 0, VerdictNoData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := evaluationSamples(tc.n, 10, 10*time.Minute)
			for i := range rows {
				rows[i].Continuation = true
				rows[i].SessionKey = string(rune('a' + i%tc.sessions))
				read := 100
				if i%tc.sessions < tc.lowSessions {
					read = tc.read
				}
				rows[i].InputTokens, rows[i].CacheReadTokens, rows[i].CacheCreationTokens = sampleInt(100-read), sampleInt(read), sampleInt(0)
			}
			e := evaluateFixture(rows)
			if e.Verdicts.Cache != tc.want {
				t.Fatalf("cache = %+v, verdict = %v", e.Cache, e.Verdicts.Cache)
			}
		})
	}
	// The high-token turns outweigh low-token misses. Per-turn averaging would
	// incorrectly fail this account and each of its two sessions.
	rows := evaluationSamples(30, 10, time.Minute)
	for i := range rows {
		rows[i].Continuation = true
		rows[i].SessionKey = string(rune('a' + i%2))
		rows[i].InputTokens, rows[i].CacheReadTokens, rows[i].CacheCreationTokens = sampleInt(1), sampleInt(0), sampleInt(0)
		if i < 2 {
			rows[i].InputTokens, rows[i].CacheReadTokens = sampleInt(100), sampleInt(900)
		}
	}
	e := evaluateFixture(rows)
	if math.Abs(e.Cache.Ratio-1800.0/2028.0) > 1e-12 || e.Verdicts.Cache != VerdictPass {
		t.Fatalf("token weighting = %+v", e.Cache)
	}
	// Writes are part of all input, with no minimum prompt length or prior-size clamp.
	for i := range rows {
		rows[i].InputTokens, rows[i].CacheReadTokens, rows[i].CacheCreationTokens = sampleInt(1), sampleInt(8), sampleInt(2)
	}
	e = evaluateFixture(rows)
	if e.Cache.InputTokens != 330 || e.Cache.ReadTokens != 240 || e.Verdicts.Cache != VerdictFail {
		t.Fatalf("write-token denominator = %+v", e.Cache)
	}
}

func TestEvaluateCacheEligibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*AttemptSample)
		want   int
	}{
		{"continuation", func(s *AttemptSample) {}, 1},
		{"cold", func(s *AttemptSample) { s.Continuation = false }, 0},
		{"no-session", func(s *AttemptSample) { s.SessionKey = "" }, 0},
		{"unknown-usage", func(s *AttemptSample) { s.CacheReadTokens = nil }, 0},
		{"unknown-writes", func(s *AttemptSample) { s.CacheCreationTokens = nil }, 0},
		{"failed", func(s *AttemptSample) { s.Outcome = OutcomeAccountFault }, 0},
		{"zero-total", func(s *AttemptSample) {
			s.InputTokens, s.CacheReadTokens, s.CacheCreationTokens = sampleInt(0), sampleInt(0), sampleInt(0)
		}, 0},
		{"negative-input", func(s *AttemptSample) { s.InputTokens = sampleInt(-1) }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := evaluationSamples(1, 10, time.Minute)
			rows[0].Continuation = true
			tc.mutate(&rows[0])
			if e := evaluateFixture(rows); e.Cache.Turns != tc.want {
				t.Fatalf("cache = %+v", e.Cache)
			}
		})
	}
}

func TestContinuationDefinition(t *testing.T) {
	previous := evaluationSamples(1, 10, time.Minute)[0]
	current := previous
	current.StartedAt = previous.FinishedAt
	for _, tc := range []struct {
		name   string
		mutate func(*AttemptSample, *AttemptSample)
		want   bool
	}{
		{"same-account", func(c, p *AttemptSample) {}, true},
		{"no-five-minute-cutoff", func(c, p *AttemptSample) { c.StartedAt = p.FinishedAt.Add(time.Hour) }, true},
		{"switched-account", func(c, p *AttemptSample) { p.AccountID++ }, false},
		{"another-group", func(c, p *AttemptSample) { p.GroupID++ }, false},
		{"another-model", func(c, p *AttemptSample) { p.Model = "different-model" }, false},
		{"another-session", func(c, p *AttemptSample) { p.SessionKey = "different-session" }, false},
		{"no-session", func(c, p *AttemptSample) { c.SessionKey, p.SessionKey = "", "" }, false},
		{"prior-failed", func(c, p *AttemptSample) { p.Outcome = OutcomeOther }, false},
		{"prior-in-flight", func(c, p *AttemptSample) { p.FinishedAt = c.StartedAt.Add(time.Nanosecond) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, p := current, previous
			tc.mutate(&c, &p)
			if got := IsContinuation(c, p); got != tc.want {
				t.Fatalf("continuation = %v, want %v", got, tc.want)
			}
		})
	}
}
