package schedquality

import "time"

// Summarize applies section 5 to samples of one account x model. samples = the 200 most recent by start time
// plus the first-output stream samples of the last 5 minutes (deduplicated by attempt id); since filters both.
func Summarize(samples []AttemptSample, since, now time.Time) Summary {
	panic("schedquality: Summarize not implemented yet (WP0-rules)")
}

// section 5; all nodata when the gate is disabled
func Judge(s Summary, p Policy) Verdicts {
	panic("schedquality: Judge not implemented yet (WP0-rules)")
}
