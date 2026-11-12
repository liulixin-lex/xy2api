package schedquality

import "time"

// JudgeTest is section 6 on the pooled turns of one round; only pass | fail | nodata; fail is decided before pass.
func JudgeTest(turns []TestTurn, model string, p Policy) Verdicts {
	var v Verdicts
	if !p.Enabled {
		return v
	}
	s := summarizeTestTurns(turns, model, p)
	if s.SpeedTurns > 0 {
		rate := float64(s.SlowTurns) / float64(s.SpeedTurns)
		switch {
		case s.SlowTurns >= 2 && rate > SlowShareTolerance:
			v.Speed = VerdictFail
		case s.SpeedWarm >= TestMinTurns && rate <= SlowShareTolerance:
			v.Speed = VerdictPass
		}
	}
	if s.ErrorTurns > 0 {
		rate := float64(s.Faults) / float64(s.ErrorTurns)
		switch {
		case s.Faults >= 2 && rate > p.MaxErrorRate:
			v.Error = VerdictFail
		case s.ErrorTurns >= TestMinTurns && (s.Faults == 0 || rate <= p.MaxErrorRate || (s.Faults == 1 && s.ErrorTurns >= TestClearTurns)):
			v.Error = VerdictPass
		}
	}
	if s.CacheTurns >= TestMinTurns {
		v.Cache = VerdictPass
		if s.CacheRatio < p.MinCacheRatio {
			v.Cache = VerdictFail
		}
	}
	return v
}

// Effective merges real and test verdicts per dimension (section 6). It pools the turns of every test that is done,
// started after max(resetAt, rowSince) and not earlier than now-TestFreshFor (start time only), and judges the pool once.
func Effective(real Verdicts, tests []TestResult, model string, p Policy, resetAt, rowSince, now time.Time) (eff Verdicts, fromTest [3]bool) {
	turns, _ := pooledTestTurns(tests, resetAt, rowSince, now)
	tested := JudgeTest(turns, model, p)
	eff = real
	if real.Speed == VerdictNoData {
		eff.Speed = tested.Speed
		fromTest[0] = tested.Speed != VerdictNoData
	}
	if real.Error == VerdictNoData {
		eff.Error = tested.Error
		fromTest[1] = tested.Error != VerdictNoData
	}
	if real.Cache == VerdictNoData {
		eff.Cache = tested.Cache
		fromTest[2] = tested.Cache != VerdictNoData
	}
	return
}

// SummarizeTests pools exactly the tests Effective would pool and returns the numbers shown when a verdict comes from tests.
func SummarizeTests(tests []TestResult, model string, p Policy, resetAt, rowSince, now time.Time) TestSummary {
	turns, count := pooledTestTurns(tests, resetAt, rowSince, now)
	s := summarizeTestTurns(turns, model, p)
	s.Tests = count
	return s
}

func pooledTestTurns(tests []TestResult, resetAt, rowSince, now time.Time) ([]TestTurn, int) {
	since := rowSince
	if resetAt.After(since) {
		since = resetAt
	}
	var turns []TestTurn
	count := 0
	for _, test := range tests {
		if test.Done && test.StartedAt.After(since) && !test.StartedAt.Before(now.Add(-TestFreshFor)) {
			turns = append(turns, test.Turns...)
			count++
		}
	}
	return turns, count
}

func summarizeTestTurns(turns []TestTurn, model string, p Policy) TestSummary {
	var s TestSummary
	var cacheRead, cachePrompt int64
	for _, turn := range turns {
		if !evidenceOutcome(turn.Outcome) {
			continue
		}
		warm := turn.Turn >= 2
		if (warm && (turn.TTFOMs != nil || turn.Outcome == OutcomeTimeout)) || (turn.Turn == 1 && turn.Outcome == OutcomeTimeout) {
			s.SpeedTurns++
			if warm {
				s.SpeedWarm++
				if turn.TTFOMs != nil {
					s.MaxTTFOMs = max(s.MaxTTFOMs, *turn.TTFOMs)
				}
			}
			if turn.Outcome == OutcomeTimeout {
				s.AnyTimeout = true
				s.SlowTurns++
			} else if normalizedMs(*turn.TTFOMs, TestScale(model, turn.Tier)) > speedLimitMs(p) {
				s.SlowTurns++
			}
		}
		if turn.Outcome == OutcomeOK || turn.Outcome == OutcomeAccountFault {
			s.ErrorTurns++
			if turn.Outcome == OutcomeAccountFault {
				s.Faults++
			}
		}
		if warm && turn.Outcome == OutcomeOK && turn.PromptTokens != nil && turn.PrevPromptTokens != nil && turn.CacheReadTokens != nil && *turn.PromptTokens >= *turn.PrevPromptTokens {
			s.CacheTurns++
			cacheRead += int64(min(*turn.CacheReadTokens, *turn.PrevPromptTokens))
			cachePrompt += int64(*turn.PrevPromptTokens)
		}
	}
	if cachePrompt > 0 {
		s.CacheRatio = float64(cacheRead) / float64(cachePrompt)
	}
	return s
}
