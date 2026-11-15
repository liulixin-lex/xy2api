package schedquality

import "time"

const (
	SlowShareTolerance    = 0.10
	WilsonZ               = 1.96
	LevelDecayAfter       = 30 * time.Minute
	TakeoverFullCooloff   = 60 * time.Second
	EvalInterval          = 5 * time.Second
	LedgerReloadOverlap   = 10 * time.Second
	ViewMaxAge            = 60 * time.Second
	LedgerRetention       = 7 * 24 * time.Hour
	MembersCacheTTL       = 30 * time.Second
	PoliciesCacheTTL      = 30 * time.Second
	ResetLockTimeout      = 3 * time.Second
	EvalStatementTimeout  = 3 * time.Second
	LedgerQueueSize       = 8192
	LedgerBatchSize       = 200
	LedgerFlushInterval   = time.Second
	LedgerWriteTimeout    = 5 * time.Second
	LedgerWriteRetries    = 3
	LedgerReadTimeout     = 5 * time.Second
	CleanupInterval       = time.Hour
	TakeoverMaxCandidates = 2
	BorrowMaxProbes       = 64
	SummaryMaxAccounts    = 1000
)

var TakeoverIdleChoices = [...]int{-1, 0, 300, 900, 1800}
