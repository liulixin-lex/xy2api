package schedquality

import "time"

// JudgeTest is section 6 on the pooled turns of one round; only pass | fail | nodata; fail is decided before pass.
func JudgeTest(turns []TestTurn, model string, p Policy) Verdicts {
	panic("schedquality: JudgeTest not implemented yet (WP0-rules)")
}

// Effective merges real and test verdicts per dimension (section 6). It pools the turns of every test that is done,
// started after max(resetAt, rowSince) and not earlier than now-TestFreshFor (start time only), and judges the pool once.
func Effective(real Verdicts, tests []TestResult, model string, p Policy, resetAt, rowSince, now time.Time) (eff Verdicts, fromTest [3]bool) {
	panic("schedquality: Effective not implemented yet (WP0-rules)")
}

// SummarizeTests pools exactly the tests Effective would pool and returns the numbers shown when a verdict comes from tests.
func SummarizeTests(tests []TestResult, model string, p Policy, resetAt, rowSince, now time.Time) TestSummary {
	panic("schedquality: SummarizeTests not implemented yet (WP0-rules)")
}
