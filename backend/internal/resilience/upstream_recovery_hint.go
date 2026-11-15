package resilience

import "time"

// UpstreamRecoveryHint preserves the uncapped upstream retry boundary.
type UpstreamRecoveryHint struct {
	Headers               map[string]string
	Source                string
	ObservedAt, NotBefore time.Time
	Delay                 time.Duration
}
