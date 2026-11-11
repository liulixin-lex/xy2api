//go:build unit

package schedquality

import (
	"fmt"
	"testing"
	"time"
)

var ruleNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func ptr(v int) *int { return &v }

func rulePolicy() Policy {
	return Policy{Enabled: true, TTFTSeconds: 1, MaxErrorRate: 0.05, MinCacheRatio: 0.9}
}

func attempt(id string, at time.Time, session string, class RequestClass, outcome Outcome, ttfo *int) AttemptSample {
	return AttemptSample{AttemptID: id, StartedAt: at, FinishedAt: at.Add(time.Hour), SessionKey: session, RequestID: id, Class: class,
		Model: "gpt-6.1-sol", Stream: true, Warm: true, Outcome: outcome, TTFOMs: ttfo}
}

func warmSamples(n, sessions int, value int) []AttemptSample {
	// Round-robin sessions: 40/4 means 4 sessions of 10, 60/4 means 4 of 15.
	rows := make([]AttemptSample, 0, n)
	for i := 0; i < n; i++ {
		t := ruleNow.Add(-time.Duration(i+1) * time.Second)
		rows = append(rows, attempt(fmt.Sprintf("a-%03d", i), t, fmt.Sprintf("s-%d", i%sessions), ClassRegular, OutcomeOK, ptr(value)))
	}
	return rows
}

func expectVerdict(t *testing.T, got, want Verdict) {
	t.Helper()
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func expectVerdicts(t *testing.T, got, want Verdicts) {
	t.Helper()
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func testTurn(tier TestTier, turn int, outcome Outcome, ttfo *int) TestTurn {
	return TestTurn{Tier: tier, Turn: turn, Outcome: outcome, TTFOMs: ttfo,
		Warm: turn >= 2, PromptTokens: ptr(101200), PrevPromptTokens: ptr(100000), CacheReadTokens: ptr(95000)}
}

func fullTurns(slow map[int]bool, cache float64) []TestTurn {
	turns := make([]TestTurn, 0, 9)
	warm := 0
	for tier := TierSmall; tier <= TierLarge; tier++ {
		turns = append(turns, testTurn(tier, 1, OutcomeOK, ptr(20000)))
		for turn := 2; turn <= 3; turn++ {
			ttfo := 5000
			if slow[warm] {
				ttfo = 15000
			}
			v := testTurn(tier, turn, OutcomeOK, ptr(ttfo))
			v.Warm = true
			v.CacheReadTokens = ptr(int(100000 * cache))
			turns = append(turns, v)
			warm++
		}
	}
	return turns
}

func faultTierTurns(faultTier TestTier, faultTurn int) []TestTurn {
	turns := make([]TestTurn, 0, 8)
	for tier := TierSmall; tier <= TierLarge; tier++ {
		for turn := 1; turn <= 3; turn++ {
			if tier == faultTier && turn == faultTurn {
				turns = append(turns, testTurn(tier, turn, OutcomeAccountFault, nil))
				break
			}
			turns = append(turns, testTurn(tier, turn, OutcomeOK, ptr(5000)))
		}
	}
	return turns
}

func testResult(start time.Time, turns []TestTurn, done bool) TestResult {
	return TestResult{ID: start.Format(time.RFC3339Nano), StartedAt: start, FinishedAt: start.Add(time.Minute), Done: done, Turns: turns}
}

func realSpeed(verdict Verdict, p Policy) Summary {
	s := Summary{Speed: SpeedSummary{N: MinSamples, Sessions: MinSessions}}
	limit := speedLimitMs(p)
	switch verdict {
	case VerdictPass:
		s.Speed.P90Ms, s.Speed.FailBoundMs = limit, limit
	case VerdictMarginal:
		s.Speed.P90Ms, s.Speed.FailBoundMs = limit+1, limit
	case VerdictFail:
		s.Speed.P90Ms, s.Speed.FailBoundMs = limit+1, limit+1
	}
	return s
}

func TestSummarizeAndJudgeAcceptance(t *testing.T) {
	p := rulePolicy()
	basePolicy := p
	cases := []struct {
		name  string
		check func(*testing.T)
	}{
		{"B1_Marginal_40_in_4_sessions_5_slow", func(t *testing.T) {
			rows := warmSamples(40, 4, 500)
			for i := 0; i < 5; i++ {
				rows[i].TTFOMs = ptr(1500)
			}
			s := Summarize(rows, time.Time{}, ruleNow)
			p.TTFTSeconds = 1
			if s.Speed.N != 40 || s.Speed.Sessions != 4 || s.Speed.P90Ms != 1500 || s.Speed.FailBoundMs != 500 {
				t.Fatalf("speed summary = %+v", s.Speed)
			}
			expectVerdict(t, Judge(s, p).Speed, VerdictMarginal)
		}},
		{"B2_Fail_40_in_4_sessions_12_slow", func(t *testing.T) {
			rows := warmSamples(40, 4, 500)
			for i := 0; i < 12; i++ {
				rows[i].TTFOMs = ptr(1500)
			}
			expectVerdict(t, Judge(Summarize(rows, time.Time{}, ruleNow), p).Speed, VerdictFail)
		}},
		{"B3_Heavy_18s_scaled_to_15s", func(t *testing.T) {
			rows := warmSamples(40, 4, 1500)
			for i := range rows {
				rows[i].Class = ClassHeavy
				rows[i].TTFOMs = ptr(18000)
			}
			s := Summarize(rows, time.Time{}, ruleNow)
			if s.Speed.P90Ms != 15000 {
				t.Fatalf("scaled p90 = %d ms, want 15000", s.Speed.P90Ms)
			}
			p.TTFTSeconds = 15
			expectVerdict(t, Judge(s, p).Speed, VerdictPass)
			if s.Speed.ByClass[ClassHeavy] != (ClassStat{N: 40, P90Ms: 18000}) {
				t.Fatalf("by-class P90 must stay raw: %+v", s.Speed.ByClass)
			}
			// K1: 1001/1.2 = 834.16... ms must be represented as 835, not 834.
			for i := range rows {
				rows[i].TTFOMs = ptr(1001)
			}
			s = Summarize(rows, time.Time{}, ruleNow)
			p.TTFTSeconds = .834
			if s.Speed.P90Ms != 835 || s.Speed.FailBoundMs != 835 {
				t.Fatalf("K1 ceil of normalized milliseconds: %+v", s.Speed)
			}
			expectVerdict(t, Judge(s, p).Speed, VerdictFail)
			p.TTFTSeconds = .835
			expectVerdict(t, Judge(s, p).Speed, VerdictPass)
		}},
		{"B4_Timeout_Is_Infinite_At_15s_And_25s", func(t *testing.T) {
			rows := warmSamples(40, 4, 500)
			for i := range rows {
				rows[i].Outcome, rows[i].TTFOMs = OutcomeTimeout, nil
				rows[i].CensoredMs = ptr(20000)
			}
			s := Summarize(rows, time.Time{}, ruleNow)
			if !s.Speed.P90Timeout || !s.Speed.FailBoundTimeout || s.Speed.Timeouts != 40 {
				t.Fatalf("timeout stats = %+v", s.Speed)
			}
			for _, limit := range []float64{15, 25} {
				p.TTFTSeconds = limit
				expectVerdict(t, Judge(s, p).Speed, VerdictFail)
			}
		}},
		{"B5_Cap_Uses_50_Of_200_And_Is_Not_Recomputed", func(t *testing.T) {
			rows := make([]AttemptSample, 0, 200)
			for i := 0; i < 150; i++ {
				ms := 100
				if i >= 50 {
					ms = 2000
				}
				rows = append(rows, attempt(fmt.Sprintf("a-%03d", i), ruleNow.Add(-time.Duration(i+1)*time.Second), "large", ClassRegular, OutcomeOK, ptr(ms)))
			}
			for i := 0; i < 50; i++ {
				rows = append(rows, attempt(fmt.Sprintf("b-%03d", i), ruleNow.Add(-time.Duration(1000+i)*time.Second), fmt.Sprintf("other-%d", i), ClassRegular, OutcomeOK, ptr(100)))
			}
			s := Summarize(rows, time.Time{}, ruleNow)
			if s.Samples != 100 || s.Sessions != 51 || s.Speed.N != 100 || s.Speed.P90Ms != 100 {
				t.Fatalf("post-cap counts = samples:%d sessions:%d speed:%d", s.Samples, s.Sessions, s.Speed.N)
			}
		}},
		{"B6_Cache_Ratio_Thresholds_And_Pooling", func(t *testing.T) {
			rows := warmSamples(40, 4, 500)
			for i := range rows {
				rows[i].PromptTokens, rows[i].PrevPromptTokens, rows[i].CacheReadTokens = ptr(3000), ptr(2500), ptr(2150)
			}
			s := Summarize(rows, time.Time{}, ruleNow)
			if s.Cache.Ratio != 0.86 {
				t.Fatalf("cache ratio = %v", s.Cache.Ratio)
			}
			p.MinCacheRatio = 0.90
			if s.Speed.N != 40 || s.Error.N != 40 || s.Cache.Turns != 40 || s.Cache.Sessions != 4 {
				t.Fatalf("dimension denominators = %+v", s)
			}
			expectVerdict(t, Judge(s, p).Cache, VerdictFail)
			p.MinCacheRatio = 0.86
			expectVerdict(t, Judge(s, p).Cache, VerdictPass)
			p.MinCacheRatio = 0.80
			expectVerdict(t, Judge(s, p).Cache, VerdictPass)
			p.MinCacheRatio = 0.90
			// Each dimension filters independently; the window still contains 40 rows.
			rows[0].Warm = false
			rows[1].Stream = false
			rows[2].Outcome, rows[2].TTFOMs = OutcomeTimeout, nil
			rows[3].TTFOMs = nil
			rows[4].PrevPromptTokens = nil
			rows[5].PromptTokens = ptr(2000)
			rows[6].PromptTokens = ptr(2499)
			rows[7].PrevPromptTokens, rows[7].PromptTokens = ptr(1000), ptr(3000)
			rows[8].Outcome, rows[8].TTFOMs = OutcomeAccountFault, nil
			rows[9].CacheReadTokens = ptr(9000) // capped at the previous prompt total
			rows[10].PrevPromptTokens, rows[10].PromptTokens, rows[10].CacheReadTokens = ptr(5000), ptr(6000), ptr(2500)
			s = Summarize(rows, time.Time{}, ruleNow)
			if s.Samples != 40 || s.Speed.N != 36 || s.Error.N != 39 || s.Cache.Turns != 33 || s.Cache.Ratio != float64(71650)/85000 {
				t.Fatalf("independent dimensions = %+v", s)
			}
		}},
		{"B7_Minimum_Samples_And_Sessions", func(t *testing.T) {
			p.TTFTSeconds = 1
			// Twenty-nine rows = 4 sessions of 6 and 1 of 5, all under cap=7.
			s := Summarize(warmSamples(29, 5, 100), time.Time{}, ruleNow)
			if s.Speed.N != 29 || s.Speed.Sessions != 5 {
				t.Fatalf("post-cap fixture has wrong counts: %+v", s.Speed)
			}
			expectVerdict(t, Judge(s, p).Speed, VerdictNoData)
			// Direct summary: post-cap 30 rows in 3 sessions (a chain fixture would cap these).
			expectVerdict(t, Judge(Summary{Speed: SpeedSummary{N: 30, Sessions: 3, P90Ms: 100}}, p).Speed, VerdictNoData)
			rows := warmSamples(40, 4, 100)
			for i := range rows {
				if rows[i].SessionKey == "s-3" {
					rows[i].Stream = false
				}
			}
			s = Summarize(rows, time.Time{}, ruleNow)
			if s.Speed.N != 30 || s.Speed.Sessions != 3 || s.Error.N != 40 || s.Error.Sessions != 4 {
				t.Fatalf("session counts must be independent per dimension: %+v", s)
			}
			expectVerdicts(t, Judge(s, p), Verdicts{Error: VerdictPass})
		}},
		{"B8_No_Session_Hash_Uses_Request_ID", func(t *testing.T) {
			rows := warmSamples(40, 4, 100)
			for i := range rows {
				rows[i].SessionKey = ""
				rows[i].RequestID = rows[i].AttemptID
				if i < 10 {
					rows[i].Outcome = OutcomeAccountFault
				}
			}
			s := Summarize(rows, time.Time{}, ruleNow)
			if s.Error.Sessions != 40 || s.Error.N != 40 || s.Error.Bad != 10 || s.Error.Rate != .25 || s.Error.LCB != WilsonLower(10, 40) {
				t.Fatalf("error stats = %+v", s.Error)
			}
			expectVerdict(t, Judge(s, p).Error, VerdictFail)
		}},
		{"B9_Error_Rate_And_Zero_Threshold", func(t *testing.T) {
			p.MaxErrorRate = 0.8
			expectVerdict(t, Judge(Summary{Error: ErrorSummary{N: 30, Sessions: 4, Bad: 15, Rate: 0.5, LCB: WilsonLower(15, 30)}}, p).Error, VerdictPass)
			p.MaxErrorRate = 0
			expectVerdict(t, Judge(Summary{Error: ErrorSummary{N: 44, Sessions: 4}}, p).Error, VerdictPass)
			expectVerdict(t, Judge(Summary{Error: ErrorSummary{N: 44, Sessions: 4, Bad: 1, Rate: 1.0 / 44, LCB: WilsonLower(1, 44)}}, p).Error, VerdictFail)
			p.MaxErrorRate = 0.05
		}},
		{"B10_Other_Does_Not_Use_Window_Slots", func(t *testing.T) {
			// Thirty eligible rows = 5 sessions of 6; cap=floor(.25*30)=7.
			rows := make([]AttemptSample, 0, 231)
			for i := 0; i < 30; i++ {
				rows = append(rows, attempt(fmt.Sprintf("ok-%03d", i), ruleNow.Add(-time.Duration(1000+i)*time.Second), fmt.Sprintf("s-%d", i%5), ClassRegular, OutcomeOK, ptr(200)))
			}
			for i := 0; i < 201; i++ {
				rows = append(rows, attempt(fmt.Sprintf("other-%03d", i), ruleNow.Add(-time.Duration(i+1)*time.Second), fmt.Sprintf("o-%d", i), ClassRegular, OutcomeOther, nil))
			}
			s := Summarize(rows, time.Time{}, ruleNow)
			if s.Samples != 30 || s.Speed.N != 30 {
				t.Fatalf("other samples consumed the evidence window: %+v", s)
			}
		}},
		{"B11_Start_Must_Be_Strictly_After_Since", func(t *testing.T) {
			since := ruleNow.Add(-time.Minute)
			rows := []AttemptSample{
				attempt("equal", since, "s1", ClassRegular, OutcomeOK, ptr(100)),
				attempt("older", since.Add(-time.Second), "s2", ClassRegular, OutcomeOK, ptr(100)),
			}
			rows[0].FinishedAt, rows[1].FinishedAt = ruleNow, ruleNow
			if got := Summarize(rows, since, ruleNow); got.Samples != 0 {
				t.Fatalf("samples started at/before since were counted: %+v", got)
			}
			rows = append(rows, attempt("after", since.Add(time.Nanosecond), "s3", ClassRegular, OutcomeOK, ptr(100)))
			if got := Summarize(rows, since, ruleNow); got.Samples != 1 || !got.From.Equal(rows[2].StartedAt) {
				t.Fatalf("start immediately after since must be counted: %+v", got)
			}
		}},
		{"B12_Acute_Uses_Unbounded_Independent_Stream", func(t *testing.T) {
			makeTimeouts := func(first time.Time) []AttemptSample {
				rows := []AttemptSample{
					attempt("t1", first, "", ClassLight, OutcomeTimeout, nil),
					attempt("t2", first.Add(time.Second), "", ClassLight, OutcomeTimeout, nil),
					attempt("t3", first.Add(2*time.Second), "", ClassLight, OutcomeTimeout, nil),
				}
				for i := range rows {
					rows[i].Warm = false
				}
				return rows
			}
			t.Run("three recent timeouts", func(t *testing.T) {
				if !Summarize(makeTimeouts(ruleNow.Add(-time.Minute)), time.Time{}, ruleNow).Acute {
					t.Fatal("expected acute timeout streak")
				}
			})
			t.Run("later stream successes do not hide an existing streak", func(t *testing.T) {
				for _, count := range []int{1, 64} {
					rows := makeTimeouts(ruleNow.Add(-4 * time.Minute))
					for i := 0; i < count; i++ {
						rows = append(rows, attempt(fmt.Sprintf("ok-%03d", i), ruleNow.Add(-time.Duration(i+1)*time.Second), "", ClassLight, OutcomeOK, ptr(20)))
					}
					if !Summarize(rows, time.Time{}, ruleNow).Acute {
						t.Fatalf("%d later successes hid the existing streak", count)
					}
				}
			})
			t.Run("success breaks streak", func(t *testing.T) {
				rows := makeTimeouts(ruleNow.Add(-time.Minute))
				rows = append(rows, attempt("success", rows[1].StartedAt.Add(500*time.Millisecond), "", ClassLight, OutcomeOK, ptr(20)))
				if Summarize(rows, time.Time{}, ruleNow).Acute {
					t.Fatal("success between timeouts must break streak")
				}
			})
			t.Run("oldest outside five minutes", func(t *testing.T) {
				rows := makeTimeouts(ruleNow.Add(-5*time.Minute - time.Second))
				rows[2].StartedAt = ruleNow.Add(-time.Minute)
				if Summarize(rows, time.Time{}, ruleNow).Acute {
					t.Fatal("only two timeouts are in acute window")
				}
			})
			t.Run("nonstream successes do not hide an existing streak", func(t *testing.T) {
				rows := makeTimeouts(ruleNow.Add(-4 * time.Minute))
				for i := 0; i < 200; i++ {
					s := attempt(fmt.Sprintf("ns-%03d", i), ruleNow.Add(-time.Duration(i+1)*time.Second), fmt.Sprintf("n-%d", i), ClassLight, OutcomeOK, ptr(50))
					s.Stream = false
					rows = append(rows, s)
				}
				if !Summarize(rows, time.Time{}, ruleNow).Acute {
					t.Fatal("nonstream evidence must not limit the acute sample column")
				}
			})
			t.Run("a stream with first output breaks streak", func(t *testing.T) {
				rows := makeTimeouts(ruleNow.Add(-time.Minute))
				other := attempt("other", rows[1].StartedAt.Add(500*time.Millisecond), "", ClassLight, OutcomeOther, ptr(15))
				rows = append(rows, other)
				if Summarize(rows, time.Time{}, ruleNow).Acute {
					t.Fatal("stream other with first output must break streak")
				}
				rows[len(rows)-1].TTFOMs = nil
				if !Summarize(rows, time.Time{}, ruleNow).Acute {
					t.Fatal("K2: other without first output is absent from the acute column")
				}
			})
			t.Run("attempt IDs deduplicate the two input windows", func(t *testing.T) {
				rows := makeTimeouts(ruleNow.Add(-time.Minute))[:2]
				rows = append(rows, rows...)
				if Summarize(rows, time.Time{}, ruleNow).Acute {
					t.Fatal("duplicates must not manufacture a streak")
				}
			})
		}},
		{"B13_Alternating_Timeouts_And_Fast_40_of_60", func(t *testing.T) {
			rows := warmSamples(60, 4, 100)
			for i := range rows {
				if i%3 < 2 {
					rows[i].Outcome, rows[i].TTFOMs = OutcomeTimeout, nil
				}
			}
			expectVerdict(t, Judge(Summarize(rows, time.Time{}, ruleNow), p).Speed, VerdictFail)
		}},
		{"B14_Window_Orders_By_Start_Time", func(t *testing.T) {
			rows := warmSamples(205, 4, 100)
			for i := range rows {
				rows[i].FinishedAt = ruleNow.Add(time.Duration(i+1) * time.Second)
			}
			s := Summarize(rows, time.Time{}, ruleNow)
			if s.Samples != WindowMaxSamples || !s.From.Equal(rows[199].StartedAt) || !s.To.Equal(rows[0].StartedAt) {
				t.Fatalf("window bounds/count used wrong clock: samples=%d from=%s to=%s", s.Samples, s.From, s.To)
			}
			newer := Summarize(rows, rows[100].StartedAt, ruleNow)
			if newer.Samples != 100 || !newer.From.Equal(rows[99].StartedAt) || !newer.To.Equal(s.To) {
				t.Fatalf("newer window must be the prefix ordered by start: %+v", newer)
			}
			// All start together; attempt_id DESC breaks ties before the 200-row limit.
			tied := make([]AttemptSample, 201)
			for i := range tied {
				tied[i] = attempt(fmt.Sprintf("tie-%03d", i), ruleNow, fmt.Sprintf("s-%d", i%4), ClassRegular, OutcomeOK, ptr(100))
			}
			tied[0].Outcome = OutcomeAccountFault
			if got := Summarize(tied, time.Time{}, ruleNow); got.Samples != 200 || got.Error.Bad != 0 {
				t.Fatalf("attempt_id tie-break did not exclude the smallest ID: %+v", got)
			}
			old := attempt("six-hours", ruleNow.Add(-WindowMaxAge), "one", ClassRegular, OutcomeOK, ptr(100))
			tooOld := old
			tooOld.AttemptID, tooOld.StartedAt = "too-old", old.StartedAt.Add(-time.Nanosecond)
			if got := Summarize([]AttemptSample{old, tooOld}, time.Time{}, ruleNow); got.Samples != 1 {
				t.Fatalf("six-hour start boundary: %+v", got)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p = basePolicy
			tc.check(t)
		})
	}
}

func TestJudgeTestAcceptance(t *testing.T) {
	p := rulePolicy()
	p.TTFTSeconds = 10
	basePolicy := p
	cases := []struct {
		name  string
		check func(*testing.T)
	}{
		{"B15_0_1_2_slow", func(t *testing.T) {
			p.TTFTSeconds = 10
			expectVerdict(t, JudgeTest(fullTurns(nil, .95), "gpt-6.1-sol", p).Speed, VerdictPass)
			expectVerdict(t, JudgeTest(fullTurns(map[int]bool{0: true}, .95), "gpt-6.1-sol", p).Speed, VerdictNoData)
			expectVerdict(t, JudgeTest(fullTurns(map[int]bool{0: true, 1: true}, .95), "gpt-6.1-sol", p).Speed, VerdictFail)
			turns := fullTurns(map[int]bool{2: true}, .95)
			turns[1] = testTurn(TierSmall, 2, OutcomeTimeout, nil)
			turns = append(turns[:2], turns[3:]...)
			expectVerdict(t, JudgeTest(turns, "gpt-6.1-sol", p).Speed, VerdictFail)
		}},
		{"B15a_Cold_Timeouts_Fail_Before_Minimum", func(t *testing.T) {
			turns := []TestTurn{testTurn(TierSmall, 1, OutcomeTimeout, nil), testTurn(TierMedium, 1, OutcomeTimeout, nil), testTurn(TierLarge, 1, OutcomeTimeout, nil)}
			expectVerdict(t, JudgeTest(turns, "gpt-6.1-sol", p).Speed, VerdictFail)
		}},
		{"B15b_Cold_Timeout_And_Four_Warm_Then_Pooled_Ten", func(t *testing.T) {
			partial := fullTurns(nil, .95)[:7]
			partial[6] = testTurn(TierLarge, 1, OutcomeTimeout, nil)
			expectVerdict(t, JudgeTest(partial, "gpt-6.1-sol", p).Speed, VerdictNoData)
			pooled := append(append([]TestTurn{}, partial...), fullTurns(nil, .95)...)
			expectVerdict(t, JudgeTest(pooled, "gpt-6.1-sol", p).Speed, VerdictPass)
			s := SummarizeTests([]TestResult{testResult(ruleNow.Add(-time.Minute), pooled, true)}, "gpt-6.1-sol", p, time.Time{}, time.Time{}, ruleNow)
			if s.SpeedTurns != 11 || s.SpeedWarm != 10 || s.SlowTurns != 1 || !s.AnyTimeout || s.MaxTTFOMs != 5000 {
				t.Fatalf("pooled test speed display = %+v", s)
			}
		}},
		{"B15c_Cold_28s_Does_Not_Compare_To_Limit", func(t *testing.T) {
			turns := fullTurns(nil, .95)
			turns[0] = testTurn(TierSmall, 1, OutcomeOK, ptr(28000))
			p.TTFTSeconds = 10
			expectVerdict(t, JudgeTest(turns, "gpt-6.1-sol", p).Speed, VerdictPass)
		}},
		{"B15d_Pooled_One_Then_Two_Slow", func(t *testing.T) {
			a := fullTurns(map[int]bool{0: true}, .95)
			expectVerdict(t, JudgeTest(append(a, fullTurns(nil, .95)...), "gpt-6.1-sol", p).Speed, VerdictPass)
			expectVerdict(t, JudgeTest(append(a, fullTurns(map[int]bool{3: true}, .95)...), "gpt-6.1-sol", p).Speed, VerdictFail)
		}},
		{"B15e_Test_Scale_Table_Is_Used", func(t *testing.T) {
			p.TTFTSeconds = 10
			cases := []struct {
				model string
				tier  TestTier
				ms    int
				want  Verdict
			}{
				{"gpt-6.1-sol", TierLarge, 12000, VerdictPass},
				{"gpt-6-astra", TierLarge, 12000, VerdictFail},
				{"gpt-6.1-sol", TierSmall, 7000, VerdictFail},
			}
			for _, tc := range cases {
				t.Run(tc.model+"/"+tc.tier.String(), func(t *testing.T) {
					turns := make([]TestTurn, 6)
					for i := range turns {
						turns[i] = testTurn(tc.tier, 2+i%2, OutcomeOK, ptr(tc.ms))
						turns[i].Class = RequestClass(i % 4)
						turns[i].PromptTokens = ptr(110000 + i*10000)
					}
					expectVerdict(t, JudgeTest(turns, tc.model, p).Speed, tc.want)
				})
			}
			// K1: 1001/.65 = 1540 exactly; 1002/.65 rounds upward to 1542.
			turns := make([]TestTurn, 6)
			for i := range turns {
				turns[i] = testTurn(TierSmall, 2+i%2, OutcomeOK, ptr(1002))
			}
			p.TTFTSeconds = 1.541
			expectVerdict(t, JudgeTest(turns, "gpt-6.1-sol", p).Speed, VerdictFail)
			p.TTFTSeconds = 1.542
			expectVerdict(t, JudgeTest(turns, "gpt-6.1-sol", p).Speed, VerdictPass)
		}},
		{"B16_Cache_Ratio_Threshold_And_Pooling", func(t *testing.T) {
			turns := fullTurns(nil, .88)
			p.MinCacheRatio = .90
			expectVerdict(t, JudgeTest(turns, "gpt-6.1-sol", p).Cache, VerdictFail)
			p.MinCacheRatio = .85
			expectVerdict(t, JudgeTest(turns, "gpt-6.1-sol", p).Cache, VerdictPass)
			four := []TestTurn{turns[1], turns[2], turns[4], turns[5]}
			p.MinCacheRatio = .90
			expectVerdict(t, JudgeTest(four, "gpt-6.1-sol", p).Cache, VerdictNoData)
			expectVerdict(t, JudgeTest(append(four, turns...), "gpt-6.1-sol", p).Cache, VerdictFail)
			// Tests use their own cache filter; the real-window 2048-token minimum does not apply.
			for i := range turns {
				turns[i].PrevPromptTokens, turns[i].PromptTokens, turns[i].CacheReadTokens = ptr(1), ptr(2), ptr(1)
			}
			expectVerdict(t, JudgeTest(turns, "gpt-6.1-sol", p).Cache, VerdictPass)
		}},
		{"B17_Clean_One_Fault_And_Two_Faults", func(t *testing.T) {
			clean := fullTurns(nil, .95)
			one := faultTierTurns(TierMedium, 2)
			if len(one) != 8 {
				t.Fatalf("middle tier stops after turn 2: counted %d turns", len(one))
			}
			expectVerdict(t, JudgeTest(clean, "gpt-6.1-sol", p).Error, VerdictPass)
			expectVerdict(t, JudgeTest(one, "gpt-6.1-sol", p).Error, VerdictNoData)
			two := []TestTurn{testTurn(TierSmall, 1, OutcomeOK, ptr(10)), testTurn(TierSmall, 2, OutcomeAccountFault, nil),
				testTurn(TierMedium, 1, OutcomeOK, ptr(10)), testTurn(TierMedium, 2, OutcomeAccountFault, nil),
				testTurn(TierLarge, 1, OutcomeOK, ptr(5000)), testTurn(TierLarge, 2, OutcomeOK, ptr(5000)), testTurn(TierLarge, 3, OutcomeOK, ptr(5000))}
			expectVerdict(t, JudgeTest(two, "gpt-6.1-sol", p).Error, VerdictFail)
		}},
		{"B17a_One_Fault_Pooled_With_Clean_And_New_Fault", func(t *testing.T) {
			one := faultTierTurns(TierMedium, 2)
			pooled := append(append([]TestTurn{}, one...), fullTurns(nil, .95)...)
			expectVerdict(t, JudgeTest(pooled, "gpt-6.1-sol", p).Error, VerdictPass)
			second := fullTurns(nil, .95)
			second[8] = testTurn(TierLarge, 3, OutcomeAccountFault, nil)
			expectVerdict(t, JudgeTest(append(one, second...), "gpt-6.1-sol", p).Error, VerdictFail)
			expectVerdict(t, JudgeTest(pooled[:15], "gpt-6.1-sol", p).Error, VerdictPass)
			expectVerdict(t, JudgeTest(pooled[:14], "gpt-6.1-sol", p).Error, VerdictNoData)
		}},
		{"B17b_Wide_Error_Limit_And_Three_First_Turn_Faults", func(t *testing.T) {
			p.MaxErrorRate = .30
			one := faultTierTurns(TierMedium, 2)
			expectVerdict(t, JudgeTest(one, "gpt-6.1-sol", p).Error, VerdictPass)
			three := []TestTurn{testTurn(TierSmall, 1, OutcomeAccountFault, nil), testTurn(TierMedium, 1, OutcomeAccountFault, nil), testTurn(TierLarge, 1, OutcomeAccountFault, nil)}
			expectVerdict(t, JudgeTest(three, "gpt-6.1-sol", p).Error, VerdictFail)
			p.MaxErrorRate = .05
		}},
		{"B18_Other_Turns_Excluded_From_All_Dimensions", func(t *testing.T) {
			partial := fullTurns(nil, .95)[:5]
			partial = append(partial, testTurn(TierMedium, 3, OutcomeOther, nil), testTurn(TierLarge, 1, OutcomeOther, nil))
			expectVerdicts(t, JudgeTest(partial, "gpt-6.1-sol", p), Verdicts{})
			expectVerdicts(t, JudgeTest(append(partial, partial...), "gpt-6.1-sol", p), Verdicts{Speed: VerdictPass, Error: VerdictPass, Cache: VerdictPass})
			partial[5].TTFOMs = ptr(3000)
			expectVerdicts(t, JudgeTest(partial, "gpt-6.1-sol", p), Verdicts{})
			cancelled := fullTurns(nil, .95)
			cancelled[1] = testTurn(TierSmall, 2, OutcomeOther, ptr(3000))
			cancelled = append(cancelled[:2], cancelled[3:]...)
			expectVerdict(t, JudgeTest(cancelled, "gpt-6.1-sol", p).Speed, VerdictNoData)
			if display := SummarizeTests([]TestResult{testResult(ruleNow.Add(-time.Minute), cancelled, true)}, "gpt-6.1-sol", p, time.Time{}, time.Time{}, ruleNow); display.SpeedWarm != 4 {
				t.Fatalf("other with output must leave only four warm turns: %+v", display)
			}
		}},
		{"B18a_Aborted_Test_Contributes_No_Turns", func(t *testing.T) {
			result := testResult(ruleNow, fullTurns(nil, .95), false)
			eff, _ := Effective(Verdicts{}, []TestResult{result}, "gpt-6.1-sol", p, time.Time{}, time.Time{}, ruleNow.Add(time.Minute))
			expectVerdicts(t, eff, Verdicts{})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p = basePolicy
			tc.check(t)
		})
	}
}

func TestEffectiveAcceptance(t *testing.T) {
	p := rulePolicy()
	p.TTFTSeconds = 10
	nd := Verdicts{}
	start := ruleNow.Add(-time.Minute)
	basePolicy := p
	cases := []struct {
		name  string
		check func(*testing.T)
	}{
		{"B19_Real_Verdict_Wins_Per_Dimension", func(t *testing.T) {
			bad := fullTurns(map[int]bool{0: true, 1: true}, .5)
			eff, from := Effective(Verdicts{Speed: VerdictPass}, []TestResult{testResult(start, bad, true)}, "gpt-6.1-sol", p,
				time.Time{}, ruleNow.Add(-2*time.Minute), ruleNow)
			if eff.Speed != VerdictPass || from[0] {
				t.Fatalf("effective speed=%s fromTest=%v", eff.Speed, from[0])
			}
		}},
		{"B20_Eligible_Test_Fills_Nodata", func(t *testing.T) {
			turns := fullTurns(nil, .5)
			eff, from := Effective(nd, []TestResult{testResult(start, turns, true)}, "gpt-6.1-sol", p,
				time.Time{}, ruleNow.Add(-2*time.Minute), ruleNow)
			if eff.Cache != VerdictFail || !from[2] {
				t.Fatalf("effective cache=%s fromTest=%v", eff.Cache, from)
			}
			if from != [3]bool{true, true, true} || eff.Speed != VerdictPass || eff.Error != VerdictPass {
				t.Fatalf("each non-nodata test verdict must identify its source: effective=%+v fromTest=%v", eff, from)
			}
			summary := SummarizeTests([]TestResult{testResult(start, turns, true)}, "gpt-6.1-sol", p, time.Time{}, ruleNow.Add(-2*time.Minute), ruleNow)
			if summary != (TestSummary{Tests: 1, SpeedTurns: 6, SpeedWarm: 6, MaxTTFOMs: 5000, ErrorTurns: 9, CacheTurns: 6, CacheRatio: .5}) {
				t.Fatalf("test display summary = %+v", summary)
			}
		}},
		{"B20a_Only_Eligible_Tests_Are_Pooled", func(t *testing.T) {
			good := testResult(start, fullTurns(nil, .5), true)
			cases := []struct {
				name string
				test TestResult
			}{
				{"start equals since", testResult(ruleNow.Add(-2*time.Minute), good.Turns, true)},
				{"start equals reset", testResult(ruleNow.Add(-2*time.Minute), good.Turns, true)},
				{"older than freshness", testResult(ruleNow.Add(-TestFreshFor-time.Minute), good.Turns, true)},
				{"aborted", testResult(start, good.Turns, false)},
				{"still running", testResult(start, good.Turns, false)},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					reset, since := time.Time{}, ruleNow.Add(-2*time.Minute)
					if tc.name == "start equals reset" {
						reset, since = tc.test.StartedAt, time.Time{}
					}
					eff, _ := Effective(nd, []TestResult{tc.test}, "gpt-6.1-sol", p, reset, since, ruleNow)
					if eff.Cache != VerdictNoData {
						t.Fatalf("excluded test contributed cache verdict %s", eff.Cache)
					}
					if display := SummarizeTests([]TestResult{tc.test}, "gpt-6.1-sol", p, reset, since, ruleNow); display != (TestSummary{}) {
						t.Fatalf("display counted an excluded test: %+v", display)
					}
					eff, _ = Effective(nd, []TestResult{tc.test, good}, "gpt-6.1-sol", p, reset, since, ruleNow)
					display := SummarizeTests([]TestResult{tc.test, good}, "gpt-6.1-sol", p, reset, since, ruleNow)
					if eff.Cache != VerdictFail || display.Tests != 1 || display.CacheTurns != 6 {
						t.Fatalf("only eligible test should count: effective=%+v display=%+v", eff, display)
					}
				})
			}
			// Start-time boundaries apply even if completion was recent or lies in the future.
			boundary := testResult(ruleNow.Add(-TestFreshFor), good.Turns, true)
			boundary.FinishedAt = ruleNow.Add(time.Hour)
			if eff, _ := Effective(nd, []TestResult{boundary}, "gpt-6.1-sol", p, time.Time{}, time.Time{}, ruleNow); eff.Cache != VerdictFail {
				t.Fatalf("exactly six-hour-old start is fresh: %+v", eff)
			}
			for _, offset := range []time.Duration{time.Nanosecond, -time.Nanosecond} {
				old := testResult(start.Add(offset-time.Minute), good.Turns, true)
				eff, _ := Effective(nd, []TestResult{old}, "gpt-6.1-sol", p, time.Time{}, ruleNow.Add(-2*time.Minute), ruleNow)
				want := VerdictNoData
				if offset > 0 {
					want = VerdictFail
				}
				expectVerdict(t, eff.Cache, want)
			}
		}},
		{"B20b_Aborted_Test_Does_Not_Break_Pooling", func(t *testing.T) {
			tests := []TestResult{
				testResult(ruleNow.Add(-4*time.Minute), fullTurns(nil, .5), true),
				testResult(ruleNow.Add(-3*time.Minute), fullTurns(nil, .5), false),
				testResult(ruleNow.Add(-2*time.Minute), fullTurns(nil, .99), true),
			}
			eff, _ := Effective(nd, tests, "gpt-6.1-sol", p, time.Time{}, ruleNow.Add(-5*time.Minute), ruleNow)
			s := SummarizeTests(tests, "gpt-6.1-sol", p, time.Time{}, ruleNow.Add(-5*time.Minute), ruleNow)
			if eff.Cache != VerdictFail || s.Tests != 2 || s.CacheTurns != 12 || s.CacheRatio >= .9 {
				t.Fatalf("effective=%+v tests=%+v", eff, s)
			}
		}},
		{"B20c_Old_Test_Never_Revives_After_Row_Deletion", func(t *testing.T) {
			oldStart := ruleNow
			old := testResult(oldStart, fullTurns(map[int]bool{0: true, 1: true}, .95), true)
			old.FinishedAt = oldStart.Add(3 * time.Minute)
			row := oldStart.Add(30 * time.Second)
			beforeDeletion := oldStart.Add(200 * time.Second)
			if eff, _ := Effective(nd, []TestResult{old}, "gpt-6.1-sol", p, time.Time{}, row, beforeDeletion); eff.Speed != VerdictNoData {
				t.Fatalf("old test counted before deletion: %+v", eff)
			}
			afterDeletion := row.Add(TestFreshFor + 5*time.Second)
			if eff, _ := Effective(nd, []TestResult{old}, "gpt-6.1-sol", p, time.Time{}, time.Time{}, afterDeletion); eff.Speed != VerdictNoData {
				t.Fatalf("old test revived after deletion: %+v", eff)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p = basePolicy
			tc.check(t)
		})
	}
}

func nextAt(prev StateRow, p Policy, summary Summary, tests []TestResult, testsEnabled bool, now time.Time) (StateRow, bool, bool, bool) {
	return Next(prev, Inputs{Policy: p, Summary: summary, Tests: tests, TestsEnabled: testsEnabled}, now)
}

func cooling(reason Reason, since time.Time, until time.Time, level int) StateRow {
	return StateRow{AccountID: 1, Model: "gpt-6.1-sol", GroupID: 2, State: StateCooling, Level: level, Reason: reason, Since: since, Until: until}
}

func TestNextAcceptance(t *testing.T) {
	p := rulePolicy()
	p.TTFTSeconds = 10
	nd := Summary{}
	basePolicy := p
	cases := []struct {
		name  string
		check func(*testing.T)
	}{
		{"C1_Normal_Fail_Enters_First_Cooldown", func(t *testing.T) {
			now := ruleNow
			n, changed, remove, _ := nextAt(StateRow{AccountID: 1, Model: "gpt-6.1-sol", GroupID: 2}, p, realSpeed(VerdictFail, p), nil, true, now)
			if n.State != StateCooling || n.Reason != ReasonSpeed || n.Level != 1 || n.Until.Sub(now) != 30*time.Second || !changed || remove {
				t.Fatalf("next=%+v changed=%v remove=%v", n, changed, remove)
			}
		}},
		{"C2_Expired_Cooling_Waits_And_Wants_Test", func(t *testing.T) {
			row := cooling(ReasonSpeed, ruleNow.Add(-time.Minute), ruleNow, 1)
			n, _, _, want := nextAt(row, p, nd, nil, true, ruleNow)
			if n.State != StateCooling || !want {
				t.Fatalf("next=%+v wantTest=%v", n, want)
			}
			bad := testResult(ruleNow.Add(-30*time.Second), fullTurns(map[int]bool{0: true, 1: true}, .5), true)
			n, changed, remove, want := nextAt(row, p, Summary{Acute: true}, []TestResult{bad}, true, row.Until.Add(-time.Nanosecond))
			if n != row || changed || remove || want {
				t.Fatalf("not-yet-expired row must hold: next=%+v changed=%v remove=%v want=%v", n, changed, remove, want)
			}
		}},
		{"C3_Test_Pass_Recovers_Verified", func(t *testing.T) {
			row := cooling(ReasonSpeed, ruleNow.Add(-time.Minute), ruleNow, 1)
			tests := []TestResult{testResult(ruleNow.Add(time.Second), fullTurns(nil, .95), true)}
			n, _, _, want := nextAt(row, p, nd, tests, true, ruleNow.Add(2*time.Minute))
			if n.State != StateNormal || n.Reason != "" || n.Level != 1 || want {
				t.Fatalf("next=%+v wantTest=%v", n, want)
			}
			turns := fullTurns(nil, .95)
			for i := range turns {
				turns[i].PromptTokens, turns[i].PrevPromptTokens, turns[i].CacheReadTokens = nil, nil, nil
			}
			tests[0].Turns = turns
			n, _, _, want = nextAt(row, p, nd, tests, true, ruleNow.Add(2*time.Minute))
			if n.State != StateNormal || n.Reason != "" || want {
				t.Fatalf("other dimensions without data cannot block recovery: next=%+v want=%v", n, want)
			}
			turns[8].Outcome = OutcomeAccountFault // retains first output; error has one fault and stays nodata
			tests[0].Turns = turns
			expectVerdicts(t, JudgeTest(turns, row.Model, p), Verdicts{Speed: VerdictPass})
			n, _, _, want = nextAt(row, p, nd, tests, true, ruleNow.Add(2*time.Minute))
			if n.State != StateNormal || n.Reason != "" || want {
				t.Fatalf("only the causing dimension needs to pass: next=%+v want=%v", n, want)
			}
		}},
		{"C3a_Test_Cache_Fail_ReCools", func(t *testing.T) {
			row := cooling(ReasonSpeed, ruleNow.Add(-time.Minute), ruleNow, 1)
			tests := []TestResult{testResult(ruleNow.Add(time.Second), fullTurns(nil, .5), true)}
			n, _, _, _ := nextAt(row, p, nd, tests, true, ruleNow.Add(2*time.Minute))
			if n.State != StateCooling || n.Reason != ReasonTestCache || n.Level != 2 || n.Until.Sub(n.Since) != time.Minute {
				t.Fatalf("next=%+v", n)
			}
		}},
		{"C3b_One_Slow_Waits_Then_Pooled_Outcome", func(t *testing.T) {
			row := cooling(ReasonSpeed, ruleNow.Add(-time.Minute), ruleNow, 1)
			first := testResult(ruleNow.Add(time.Second), fullTurns(map[int]bool{0: true}, .95), true)
			now := ruleNow.Add(2 * time.Minute)
			n, _, _, want := nextAt(row, p, nd, []TestResult{first}, true, now)
			if n.State != StateCooling || !want {
				t.Fatalf("first result next=%+v wantTest=%v", n, want)
			}
			second := testResult(now.Add(time.Second), fullTurns(nil, .95), true)
			got, _, _, _ := nextAt(row, p, nd, []TestResult{first, second}, true, now.Add(2*time.Minute))
			if got.State != StateNormal || got.Reason != "" {
				t.Fatalf("pooled pass next=%+v", got)
			}
			second.Turns = fullTurns(map[int]bool{3: true}, .95)
			got, _, _, _ = nextAt(row, p, nd, []TestResult{first, second}, true, now.Add(2*time.Minute))
			if got.State != StateCooling || got.Reason != ReasonTestSpeed {
				t.Fatalf("pooled failure next=%+v", got)
			}
		}},
		{"C4_Test_Failure_Uses_Next_Ladder_Step", func(t *testing.T) {
			row := cooling(ReasonCache, ruleNow.Add(-time.Minute), ruleNow, 1)
			tests := []TestResult{testResult(ruleNow.Add(time.Second), fullTurns(nil, .5), true)}
			n, _, _, _ := nextAt(row, p, nd, tests, true, ruleNow.Add(2*time.Minute))
			if n.State != StateCooling || n.Reason != ReasonTestCache || n.Level != 2 || n.Until.Sub(n.Since) != time.Minute {
				t.Fatalf("next=%+v", n)
			}
		}},
		{"C5_Real_Pass_Recovers_Without_Test", func(t *testing.T) {
			row := cooling(ReasonSpeed, ruleNow.Add(-time.Minute), ruleNow, 1)
			n, _, _, want := nextAt(row, p, realSpeed(VerdictPass, p), nil, true, ruleNow)
			if n.State != StateNormal || want || n.Reason != "" {
				t.Fatalf("next=%+v wantTest=%v", n, want)
			}
		}},
		{"C6_Real_Marginal_ReCools_Without_Test", func(t *testing.T) {
			row := cooling(ReasonSpeed, ruleNow.Add(-time.Minute), ruleNow, 1)
			n, _, _, want := nextAt(row, p, realSpeed(VerdictMarginal, p), nil, true, ruleNow)
			if n.State != StateCooling || n.Level != 2 || want {
				t.Fatalf("next=%+v wantTest=%v", n, want)
			}
			// A normal marginal row is informational; cooling marginal rows re-cool.
			normal := StateRow{AccountID: 1, Model: row.Model}
			n, changed, remove, want := nextAt(normal, p, realSpeed(VerdictMarginal, p), nil, true, ruleNow)
			if n != normal || changed || remove || want {
				t.Fatalf("normal marginal row changed: %+v changed=%v remove=%v want=%v", n, changed, remove, want)
			}
			both := realSpeed(VerdictMarginal, p)
			both.Error = ErrorSummary{N: 40, Sessions: 4, Rate: .5, LCB: .3}
			n, _, _, _ = nextAt(row, p, both, nil, true, ruleNow)
			if n.Reason != ReasonSpeed {
				t.Fatalf("reason priority must choose speed before error: %+v", n)
			}
			for _, evidence := range []struct {
				summary Summary
				reason  Reason
			}{
				{realSpeed(VerdictFail, p), ReasonSpeed},
				{Summary{Error: ErrorSummary{N: 40, Sessions: 4, Rate: .1, LCB: .04}}, ReasonError},
				{Summary{Error: ErrorSummary{N: 40, Sessions: 4, Rate: .5, LCB: .3}}, ReasonError},
				{Summary{Cache: CacheSummary{Turns: 40, Sessions: 4, Ratio: .5}}, ReasonCache},
			} {
				n, _, _, want := nextAt(row, p, evidence.summary, nil, true, ruleNow)
				if n.State != StateCooling || n.Reason != evidence.reason || n.Level != 2 || want {
					t.Fatalf("real conclusion must re-cool without testing: %+v want=%v", n, want)
				}
			}
		}},
		{"C7_Waits_Ten_Minutes_Then_Releases_Unverified", func(t *testing.T) {
			row := cooling(ReasonSpeed, ruleNow, ruleNow, 1)
			for elapsed := time.Duration(0); elapsed < TestWaitMax; elapsed += TestRetryEvery {
				aborted := testResult(ruleNow.Add(elapsed), nil, false)
				n, changed, remove, want := nextAt(row, p, nd, []TestResult{aborted}, true, ruleNow.Add(elapsed))
				if n != row || changed || remove || !want {
					t.Fatalf("waiting at %s: next=%+v changed=%v remove=%v want=%v", elapsed, n, changed, remove, want)
				}
			}
			n, _, _, want := nextAt(row, p, nd, nil, true, ruleNow.Add(TestWaitMax-time.Second))
			if n.State != StateCooling || !want {
				t.Fatalf("before timeout next=%+v wantTest=%v", n, want)
			}
			n, _, _, want = nextAt(row, p, nd, nil, true, ruleNow.Add(TestWaitMax))
			if n.State != StateNormal || n.Reason != ReasonUnverified || n.Level != 1 || want {
				t.Fatalf("at timeout next=%+v wantTest=%v", n, want)
			}
		}},
		{"C7a_One_Test_Is_Evaluated_Against_Each_Rows_Since", func(t *testing.T) {
			test := testResult(ruleNow.Add(4*time.Minute), fullTurns(map[int]bool{0: true, 1: true}, .95), true)
			rowA := cooling(ReasonSpeed, ruleNow, ruleNow.Add(5*time.Minute), 1)
			rowB := cooling(ReasonSpeed, ruleNow.Add(3*time.Minute), ruleNow.Add(5*time.Minute), 1)
			pa := p
			pa.TTFTSeconds = 30
			a, _, _, wantA := nextAt(rowA, pa, nd, []TestResult{test}, true, ruleNow.Add(6*time.Minute))
			b, _, _, wantB := nextAt(rowB, p, nd, []TestResult{test}, true, ruleNow.Add(6*time.Minute))
			if a.State != StateNormal || a.Reason != "" || b.State != StateCooling || b.Reason != ReasonTestSpeed || wantA || wantB {
				t.Fatalf("each row must use its own threshold: A=%+v B=%+v", a, b)
			}
			n, _, _, want := nextAt(b, p, nd, []TestResult{test}, true, b.Until)
			if n.State != StateCooling || !want {
				t.Fatalf("test before the next round must be excluded: %+v want=%v", n, want)
			}
		}},
		{"C7b_Unverified_Decays_And_Redegrade_Uses_Uncleared_Level", func(t *testing.T) {
			row := StateRow{AccountID: 1, Model: "gpt-6.1-sol", State: StateNormal, Level: 1, Reason: ReasonUnverified, Since: ruleNow}
			n, _, _, _ := nextAt(row, p, nd, nil, true, ruleNow.Add(LevelDecayAfter-time.Second))
			if n.Level != 1 || n.Reason != ReasonUnverified {
				t.Fatalf("decayed early: %+v", n)
			}
			n, _, remove, _ := nextAt(row, p, nd, nil, true, ruleNow.Add(LevelDecayAfter))
			if n.Level != 0 || n.Reason != "" || remove {
				t.Fatalf("decay at boundary next=%+v remove=%v", n, remove)
			}
			n, _, _, _ = nextAt(row, p, realSpeed(VerdictFail, p), nil, true, ruleNow.Add(LevelDecayAfter))
			if n.State != StateCooling || n.Level != 1 || n.Until.Sub(n.Since) != 30*time.Second {
				t.Fatalf("same-evaluation decay did not precede cooling: %+v", n)
			}
			n, _, _, _ = nextAt(row, p, realSpeed(VerdictFail, p), nil, true, ruleNow.Add(LevelDecayAfter-time.Second))
			if n.Level != 2 || n.Until.Sub(n.Since) != time.Minute {
				t.Fatalf("pre-decay ladder step wrong: %+v", n)
			}
			n, _, _, _ = nextAt(row, p, realSpeed(VerdictFail, p), nil, true, ruleNow.Add(5*time.Minute))
			if n.Level != 2 || n.Until.Sub(n.Since) != time.Minute {
				t.Fatalf("re-degrade after five minutes must preserve level: %+v", n)
			}
		}},
		{"C8_Tests_Off_Still_Uses_Completed_Manual_Test", func(t *testing.T) {
			row := cooling(ReasonSpeed, ruleNow.Add(-time.Minute), ruleNow, 1)
			n, _, _, want := nextAt(row, p, nd, nil, false, ruleNow)
			if n.State != StateNormal || n.Reason != ReasonUnverified || want {
				t.Fatalf("no conclusion next=%+v want=%v", n, want)
			}
			bad := testResult(ruleNow.Add(time.Second), fullTurns(map[int]bool{0: true, 1: true}, .95), true)
			n, _, _, want = nextAt(row, p, nd, []TestResult{bad}, false, ruleNow.Add(2*time.Minute))
			if n.State != StateCooling || n.Reason != ReasonTestSpeed || want {
				t.Fatalf("manual fail next=%+v want=%v", n, want)
			}
			good := testResult(ruleNow.Add(time.Second), fullTurns(nil, .95), true)
			n, _, _, want = nextAt(row, p, nd, []TestResult{good}, false, ruleNow.Add(2*time.Minute))
			if n.State != StateNormal || n.Reason != "" || want {
				t.Fatalf("manual pass next=%+v want=%v", n, want)
			}
		}},
		{"C9_Cooldown_Ladder_Caps_At_Thirty_Minutes", func(t *testing.T) {
			row := StateRow{AccountID: 1, Model: "gpt-6.1-sol"}
			got := make([]time.Duration, 0, 7)
			for i := 0; i < 7; i++ {
				now := ruleNow.Add(time.Duration(i) * time.Hour)
				if row.State == StateCooling {
					now = row.Until
				}
				row, _, _, _ = nextAt(row, p, realSpeed(VerdictFail, p), nil, true, now)
				got = append(got, row.Until.Sub(row.Since))
			}
			want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 30 * time.Minute}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("ladder = %v", got)
				}
			}
			if row.Level != 6 {
				t.Fatalf("level must cap at 6: %+v", row)
			}
		}},
		{"C10_Exact_Thirty_Minute_Decay_Before_New_Failure", func(t *testing.T) {
			row := StateRow{AccountID: 1, Model: "gpt-6.1-sol", State: StateNormal, Level: 4, Since: ruleNow.Add(-LevelDecayAfter)}
			n, _, _, _ := nextAt(row, p, realSpeed(VerdictFail, p), nil, true, ruleNow)
			if n.Until.Sub(n.Since) != 30*time.Second || n.Level != 1 {
				t.Fatalf("real failure after decay: %+v", n)
			}
			n, _, _, _ = nextAt(row, p, nd, nil, true, ruleNow)
			if n.Level != 0 {
				t.Fatalf("acute scenario setup requires pure decay: %+v", n)
			}
			n, _, _, _ = nextAt(row, p, Summary{Acute: true}, nil, true, ruleNow)
			if n.Until.Sub(n.Since) != 30*time.Second || n.Level != 1 || n.Reason != ReasonAcuteTimeout {
				t.Fatalf("acute failure after decay: %+v", n)
			}
			n, _, _, _ = nextAt(row, p, realSpeed(VerdictFail, p), nil, true, ruleNow.Add(-time.Second))
			if n.Until.Sub(n.Since) != 15*time.Minute || n.Level != 5 {
				t.Fatalf("one second before decay must retain old level: %+v", n)
			}
		}},
		{"C11_Recovered_Row_Does_Not_Reapply_Old_Evidence", func(t *testing.T) {
			// Forty warm streams = 4 sessions of 10; 16 of the 40 are slow.
			attempts := warmSamples(40, 4, 500)
			for i := 0; i < 16; i++ {
				attempts[i].TTFOMs = ptr(20000)
			}
			summary := Summarize(attempts, time.Time{}, ruleNow)
			row, _, _, _ := nextAt(StateRow{AccountID: 1, Model: "gpt-6.1-sol"}, p, summary, nil, true, ruleNow)
			good := testResult(row.Until.Add(time.Second), fullTurns(nil, .95), true)
			row, _, _, _ = nextAt(row, p, nd, []TestResult{good}, true, good.StartedAt.Add(time.Minute))
			now := row.Since.Add(time.Minute)
			summary = Summarize(attempts, row.Since, now)
			if n, changed, _, want := nextAt(row, p, summary, []TestResult{good}, true, now); n.State != StateNormal || changed || want {
				t.Fatalf("old attempts or tests reapplied in new round: %+v changed=%v want=%v", n, changed, want)
			}
			oldBad := testResult(ruleNow.Add(-time.Minute), fullTurns(map[int]bool{0: true, 1: true}, .95), true)
			oldRow := cooling(ReasonSpeed, ruleNow, ruleNow.Add(30*time.Second), 1)
			if n, _, _, want := nextAt(oldRow, p, nd, []TestResult{oldBad}, true, oldRow.Until); n != oldRow || !want {
				t.Fatalf("old failing test was not excluded: %+v want=%v", n, want)
			}
			later := row.Since.Add(WindowMaxAge)
			n, _, remove, _ := nextAt(row, p, nd, []TestResult{good}, true, later)
			if n.State != StateNormal || n.Reason != "" || !remove {
				t.Fatalf("recovered state should remain normal and row should expire: %+v remove=%v", n, remove)
			}
			if _, _, remove, _ := nextAt(row, p, nd, nil, true, later.Add(-time.Nanosecond)); remove {
				t.Fatal("row must survive until its since leaves the six-hour window")
			}
		}},
		{"C12_Manual_Test_Fail_Only_Fills_Real_Nodata", func(t *testing.T) {
			row := StateRow{AccountID: 1, Model: "gpt-6.1-sol"}
			bad := testResult(ruleNow.Add(time.Second), fullTurns(map[int]bool{0: true, 1: true}, .95), true)
			n, _, _, _ := nextAt(row, p, nd, []TestResult{bad}, true, ruleNow.Add(time.Minute))
			if n.State != StateCooling || n.Reason != ReasonTestSpeed {
				t.Fatalf("manual test fail next=%+v", n)
			}
			n, _, _, _ = nextAt(row, p, realSpeed(VerdictPass, p), []TestResult{bad}, true, ruleNow.Add(time.Minute))
			if n.State != StateNormal {
				t.Fatalf("real pass must override test fail: %+v", n)
			}
		}},
		{"C13_Disabled_Gate_Ignores_Thresholds_But_Honors_Acute", func(t *testing.T) {
			off := p
			off.Enabled = false
			bad := testResult(ruleNow.Add(time.Second), fullTurns(map[int]bool{0: true, 1: true}, .5), true)
			expectVerdicts(t, Judge(Summary{Speed: SpeedSummary{N: 40, Sessions: 4, P90Timeout: true, FailBoundTimeout: true},
				Error: ErrorSummary{N: 40, Sessions: 4, Rate: 1, LCB: 1}, Cache: CacheSummary{Turns: 40, Sessions: 4}}, off), Verdicts{})
			expectVerdicts(t, JudgeTest(bad.Turns, "gpt-6.1-sol", off), Verdicts{})
			n, _, _, _ := nextAt(StateRow{AccountID: 1, Model: "gpt-6.1-sol"}, off, realSpeed(VerdictFail, p), []TestResult{bad}, true, ruleNow)
			if n.State != StateNormal {
				t.Fatalf("disabled policy reacted to threshold/test: %+v", n)
			}
			n, _, _, _ = nextAt(StateRow{AccountID: 1, Model: "gpt-6.1-sol"}, off, Summary{Acute: true}, nil, true, ruleNow)
			if n.State != StateCooling || n.Reason != ReasonAcuteTimeout {
				t.Fatalf("acute should still cool with gate disabled: %+v", n)
			}
			n, _, _, want := nextAt(n, off, nd, nil, true, n.Until)
			if n.State != StateNormal || n.Reason != ReasonUnverified || want {
				t.Fatalf("disabled gate should release at expiry: %+v want=%v", n, want)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p = basePolicy
			tc.check(t)
		})
	}
}
