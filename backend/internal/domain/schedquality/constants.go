package schedquality

import "time"

const (
	WindowMaxSamples        = 200
	WindowMaxAge            = 6 * time.Hour
	SessionShareCap         = 0.25
	SessionShareMin         = 3
	MinSamples              = 30
	MinSessions             = 4
	SlowShareTolerance      = 0.10
	WilsonZ                 = 1.96
	CacheMinPromptTokens    = 2048
	WarmMaxGap              = 5 * time.Minute
	AcuteTimeoutStreak      = 3
	AcuteWindow             = 5 * time.Minute
	LevelDecayAfter         = 30 * time.Minute
	TestTurnsPerTier        = 3
	TestTurnGrowthTokens    = 1200
	TestTurnGap             = 5 * time.Second
	TestOutputCap           = 2048
	TestTierParallelMax     = 3
	TestMinTurns            = 6
	TestClearTurns          = 15
	TestSlotWait            = 10 * time.Second
	TestSlotRetryInterval   = 2 * time.Second
	TestTurnTimeout         = 120 * time.Second
	TestTimeout             = 6 * time.Minute
	TestRetryEvery          = 60 * time.Second
	TestWaitMax             = 10 * time.Minute
	TestFreshFor            = 6 * time.Hour
	TakeoverFullCooloff     = 60 * time.Second
	EvalInterval            = 5 * time.Second
	LedgerReloadOverlap     = 10 * time.Second
	ViewMaxAge              = 60 * time.Second
	LedgerRetention         = 7 * 24 * time.Hour
	TestRetention           = 7 * 24 * time.Hour
	ClassSizeBoundaryTokens = 120000
	BytesPerTokenEstimate   = 5.3
	MembersCacheTTL         = 30 * time.Second
	PoliciesCacheTTL        = 30 * time.Second
	ResetLockTimeout        = 3 * time.Second
	EvalStatementTimeout    = 3 * time.Second
	SessionMemoryMaxEntries = 65536
	SessionMemoryTTL        = time.Hour
	LedgerQueueSize         = 8192
	LedgerBatchSize         = 200
	LedgerFlushInterval     = time.Second
	LedgerWriteTimeout      = 5 * time.Second
	LedgerWriteRetries      = 3
	LedgerReadTimeout       = 5 * time.Second
	CleanupInterval         = time.Hour
	TakeoverMaxCandidates   = 2
	BorrowMaxProbes         = 64
	SummaryMaxAccounts      = 1000
	SummaryMaxCooling       = 3
	RecentAttemptsMax       = 50
	ManualTestMaxModels     = 3
	PolicyTTFTMinSeconds    = 0.5
	PolicyTTFTMaxSeconds    = 600
	TestPromptTolerance     = 0.05
	TestStaleAfter          = TestTimeout + time.Minute
)

var TakeoverIdleChoices = [...]int{-1, 0, 300, 900, 1800}
var CooldownLadder = [...]time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}
var ClassScaleTable = map[string][4]float64{
	"gpt-6.1-sol": {0.73, 1, 1.20, 1.43}, "gpt-6-astra": {0.86, 1, 1.12, 1.20},
	"gpt-5.6-sol": {0.90, 1, 1.18, 1.62}, "default": {0.75, 1, 1.20, 1.40},
}
var TestScaleTable = map[string][3]float64{
	"gpt-6.1-sol": {0.65, 1, 1.28}, "gpt-6-astra": {0.62, 1.06, 1.09},
	"gpt-5.6-sol": {0.73, 0.95, 1.09}, "default": {0.70, 1, 1.10},
}

type TestTierSpec struct {
	Tokens int
	Effort string
}

var TestTiers = [3]TestTierSpec{{15000, "low"}, {60000, "high"}, {130000, "xhigh"}}
