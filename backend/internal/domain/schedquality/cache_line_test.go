//go:build unit

package schedquality

import (
	"math"
	"testing"
	"time"
)

func TestAssessCacheTurnsAndSessions(t *testing.T) {
	for _, tc := range []struct {
		name              string
		n, read, sessions int
		want              Verdict
	}{
		{"nineteen", 19, 80, 2, VerdictUnknown}, {"twenty", 20, 80, 2, VerdictPass},
		{"no-tolerance", 20, 79, 2, VerdictFail}, {"one-low-session", 20, 0, 1, VerdictUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := machineSamples(tc.n)
			for i := range rows {
				rows[i].InputTokens = machineInt(100 - tc.read)
				rows[i].CacheReadTokens = machineInt(tc.read)
				if tc.sessions == 1 {
					rows[i].SessionKey = "one"
				}
			}
			got := AssessCache(rows, .8, time.Time{}, machineNow)
			if got.Verdict != tc.want || got.Turns != tc.n || got.Sessions != tc.sessions {
				t.Fatalf("cache: %+v", got)
			}
		})
	}
	rows := machineSamples(20)
	for i := range rows {
		rows[i].SessionKey = "same-key"
		rows[i].CacheReadTokens = machineInt(0)
		if i%2 == 0 {
			rows[i].GroupID++
		}
	}
	if got := AssessCache(rows, .8, time.Time{}, machineNow); got.LowSessions != 2 || got.Verdict != VerdictFail {
		t.Fatalf("source groups collapsed: %+v", got)
	}
	// Aggregate low with only one of two sessions low remains unknown.
	for i := range rows {
		if i%2 == 0 {
			rows[i].InputTokens = machineInt(1)
			rows[i].CacheReadTokens = machineInt(9)
		}
	}
	if got := AssessCache(rows, .8, time.Time{}, machineNow); got.LowSessions != 1 || got.Verdict != VerdictUnknown {
		t.Fatalf("single low session: %+v", got)
	}
}

func TestAssessCacheTokenWeightAndEligibility(t *testing.T) {
	rows := machineSamples(20)
	for i := range rows {
		rows[i].InputTokens = machineInt(1)
		rows[i].CacheReadTokens = machineInt(0)
	}
	for i := 0; i < 2; i++ {
		rows[i].InputTokens = machineInt(100)
		rows[i].CacheReadTokens = machineInt(900)
	}
	got := AssessCache(rows, .8, time.Time{}, machineNow)
	if got.Verdict != VerdictPass || math.Abs(got.Ratio-1800.0/2018) > 1e-12 {
		t.Fatalf("not token weighted: %+v", got)
	}
	for _, tc := range []struct {
		name   string
		change func(*AttemptSample)
	}{
		{"unknown-read", func(s *AttemptSample) { s.CacheReadTokens = nil }},
		{"unknown-writes", func(s *AttemptSample) { s.CacheCreationTokens = nil }},
		{"unknown-input", func(s *AttemptSample) { s.InputTokens = nil }},
		{"negative-input", func(s *AttemptSample) { s.InputTokens = machineInt(-1) }},
		{"zero-total", func(s *AttemptSample) { s.InputTokens = machineInt(0); s.CacheReadTokens = machineInt(0) }},
		{"cold", func(s *AttemptSample) { s.Continuation = false }},
		{"no-session", func(s *AttemptSample) { s.SessionKey = "" }},
		{"failure", func(s *AttemptSample) { s.Outcome = OutcomeAccountFault }},
		{"canceled", func(s *AttemptSample) { s.ClientCanceled = true }},
		{"unfinished", func(s *AttemptSample) { s.FinishedAt = time.Time{} }},
		{"future", func(s *AttemptSample) { s.FinishedAt = machineNow.Add(time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := machineSamples(20)
			tc.change(&rows[0])
			if got := AssessCache(rows, .8, time.Time{}, machineNow); got.Turns != 19 || got.Verdict != VerdictUnknown {
				t.Fatalf("ineligible turn: %+v", got)
			}
		})
	}
	rows = machineSamples(20)
	for i := range rows {
		rows[i].CacheCreationTokens = machineInt(100)
	}
	if got := AssessCache(rows, .8, time.Time{}, machineNow); got.InputTokens != 4000 || got.Ratio != .4 || got.Verdict != VerdictFail {
		t.Fatalf("writes omitted: %+v", got)
	}
}

func TestContinuationAndCacheCut(t *testing.T) {
	previous := machineSamples(1)[0]
	current := previous
	current.AttemptID = "next"
	current.RequestID = "next-request"
	for _, tc := range []struct {
		name string
		gap  time.Duration
		want bool
	}{
		{"same-time", 0, true}, {"five-minutes", 5 * time.Minute, true}, {"five-minutes-one-second", 5*time.Minute + time.Second, false}, {"unfinished-previous", -time.Nanosecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current.StartedAt = previous.FinishedAt.Add(tc.gap)
			if IsContinuation(current, previous) != tc.want {
				t.Fatal("gap boundary")
			}
		})
	}
	current.StartedAt = previous.FinishedAt.Add(time.Second)
	current.FinishedAt = current.StartedAt.Add(time.Second)
	for _, tc := range []struct {
		name   string
		change func(*AttemptSample)
	}{
		{"account", func(s *AttemptSample) { s.AccountID++ }}, {"group", func(s *AttemptSample) { s.GroupID++ }},
		{"model", func(s *AttemptSample) { s.Model = "another" }}, {"session", func(s *AttemptSample) { s.SessionKey = "another" }},
		{"failure", func(s *AttemptSample) { s.Outcome = OutcomeOther }}, {"cancel", func(s *AttemptSample) { s.ClientCanceled = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := previous
			tc.change(&p)
			if IsContinuation(current, p) {
				t.Fatal("invalid predecessor")
			}
		})
	}
	cut := previous.FinishedAt
	current.Continuation = IsContinuation(current, previous)
	if got := AssessCache([]AttemptSample{previous, current}, .8, cut, current.FinishedAt); got.Turns != 1 {
		t.Fatalf("pre-cut predecessor lost: %+v", got)
	}
	rows := machineSamples(20)
	rows[0].FinishedAt = machineNow.Add(-CacheWindow)
	rows[0].StartedAt = rows[0].FinishedAt.Add(-time.Second)
	if got := AssessCache(rows, .8, time.Time{}, machineNow); got.Turns != 20 {
		t.Fatal("inclusive left boundary")
	}
	rows[0].FinishedAt = rows[0].FinishedAt.Add(-time.Nanosecond)
	if got := AssessCache(rows, .8, time.Time{}, machineNow); got.Turns != 19 {
		t.Fatal("expired turn included")
	}
}
