package schedquality

import "time"

type RequestClass int8 // ClassLight, ClassRegular, ClassHeavy, ClassXHeavy
type Outcome int8      // OutcomeOK=1, OutcomeTimeout=2, OutcomeAccountFault=3, OutcomeOther=4
type State int8        // StateNormal, StateCooling
type Override int8     // OverrideNone, OverrideExempt, OverrideDemote
type Verdict int8      // VerdictNoData, VerdictPass, VerdictMarginal, VerdictFail
type Standing int8     // StandFront, StandBack
type TestTier int8     // TierSmall, TierMedium, TierLarge
type Reason string     // cooling: "speed" | "error" | "cache" | "acute_timeout" | "test_speed" | "test_error" | "test_cache"; normal: "" | "unverified"

const (
	ClassLight RequestClass = iota
	ClassRegular
	ClassHeavy
	ClassXHeavy
)
const (
	OutcomeOK Outcome = iota + 1
	OutcomeTimeout
	OutcomeAccountFault
	OutcomeOther
)
const (
	StateNormal State = iota
	StateCooling
)
const (
	OverrideNone Override = iota
	OverrideExempt
	OverrideDemote
)
const (
	VerdictNoData Verdict = iota
	VerdictPass
	VerdictMarginal
	VerdictFail
)
const (
	StandFront Standing = iota
	StandBack
)
const (
	TierSmall TestTier = iota
	TierMedium
	TierLarge
)
const (
	ReasonSpeed        Reason = "speed"
	ReasonError        Reason = "error"
	ReasonCache        Reason = "cache"
	ReasonAcuteTimeout Reason = "acute_timeout"
	ReasonTestSpeed    Reason = "test_speed"
	ReasonTestError    Reason = "test_error"
	ReasonTestCache    Reason = "test_cache"
	ReasonUnverified   Reason = "unverified"
)

func (c RequestClass) String() string {
	switch c {
	case ClassLight:
		return "light"
	case ClassRegular:
		return "regular"
	case ClassHeavy:
		return "heavy"
	case ClassXHeavy:
		return "xheavy"
	}
	return ""
}
func (o Outcome) String() string {
	switch o {
	case OutcomeOK:
		return "ok"
	case OutcomeTimeout:
		return "timeout"
	case OutcomeAccountFault:
		return "account_fault"
	case OutcomeOther:
		return "other"
	}
	return ""
}
func (s State) String() string {
	switch s {
	case StateNormal:
		return "normal"
	case StateCooling:
		return "cooling"
	}
	return ""
}
func (v Verdict) String() string {
	switch v {
	case VerdictNoData:
		return "nodata"
	case VerdictPass:
		return "pass"
	case VerdictMarginal:
		return "marginal"
	case VerdictFail:
		return "fail"
	}
	return ""
}
func (s Standing) String() string {
	switch s {
	case StandFront:
		return "front"
	case StandBack:
		return "back"
	}
	return ""
}
func (t TestTier) String() string {
	switch t {
	case TierSmall:
		return "small"
	case TierMedium:
		return "medium"
	case TierLarge:
		return "large"
	}
	return ""
}

type AttemptSample struct {
	AttemptID                                       string
	StartedAt, FinishedAt                           time.Time
	AccountID, GroupID                              int64
	Model                                           string
	Class                                           RequestClass
	Stream, Warm                                    bool
	TTFOMs, CensoredMs                              *int
	Outcome                                         Outcome
	Detail                                          string
	PromptTokens, PrevPromptTokens, CacheReadTokens *int
	SessionKey, RequestID                           string
	InsertedAt                                      time.Time
}
type Policy struct {
	Enabled                                  bool
	TTFTSeconds, MaxErrorRate, MinCacheRatio float64
	TakeoverIdleSeconds                      int
}
type ClassStat struct {
	N          int
	P90Ms      int
	P90Timeout bool
}
type SpeedSummary struct {
	N, Sessions, Timeouts int
	P90Ms                 int
	P90Timeout            bool
	FailBoundMs           int
	FailBoundTimeout      bool
	ByClass               [4]ClassStat // raw (not normalized) P90 per class, display only
}
type ErrorSummary struct {
	N, Sessions, Bad int
	Rate, LCB        float64
}
type CacheSummary struct {
	Turns, Sessions int
	Ratio           float64
}
type Summary struct {
	Samples, Sessions int
	From, To          time.Time
	Speed             SpeedSummary
	Error             ErrorSummary
	Cache             CacheSummary
	Acute             bool
}
type Verdicts struct{ Speed, Error, Cache Verdict }

type TestTurn struct {
	Tier                                            TestTier
	Turn                                            int // Turn 1 is the cold one; a tier has 3 turns
	Class                                           RequestClass
	Warm                                            bool // Class is what the recorder saw, for display only; judging uses Tier
	TTFOMs                                          *int
	Outcome                                         Outcome
	Detail                                          string
	PromptTokens, PrevPromptTokens, CacheReadTokens *int
}
type TestResult struct {
	ID                    string
	StartedAt, FinishedAt time.Time
	Done                  bool // false = aborted
	Turns                 []TestTurn
}
type TestSummary struct { // the pooled turns of one row's current round, for display
	Tests                            int // how many tests were pooled
	SpeedTurns, SpeedWarm, SlowTurns int // speed: counted turns (cold timeouts included), counted warm turns, bad turns
	MaxTTFOMs                        int
	AnyTimeout                       bool // slowest counted warm turn, raw, 0 when none; any counted first-output timeout (cold ones included)
	ErrorTurns, Faults               int  // error
	CacheTurns                       int
	CacheRatio                       float64 // cache
}

type StateRow struct {
	AccountID    int64
	Model        string
	GroupID      int64
	State        State
	Level        int
	Reason       Reason
	Since, Until time.Time
}
type Inputs struct {
	Policy       Policy
	Summary      Summary      // real evidence since max(reset time, row.Since)
	Tests        []TestResult // finished tests of the account x model within TestFreshFor, oldest first
	TestsEnabled bool
	ResetAt      time.Time
}

type OverrideRef struct {
	Mode    Override
	Expires time.Time
} // zero Expires = no expiry
type StateView struct {
	State                      State
	Level                      int
	Reason                     Reason
	Since, Until               time.Time
	WaitingTest                bool        // cooling past Until without a conclusion yet
	GroupOverride, AllOverride OverrideRef // both kept so that an expired group override falls back at read time
	Verdicts                   Verdicts    // effective verdicts, for display
	FromTest                   [3]bool     // which of them came from a test
}
type View interface {
	Version() int64
	At() time.Time // when this view was built
	GlobalEnabled() bool
	StateOf(groupID, accountID int64, model string) StateView
	TakeoverIdle(groupID int64) (idle time.Duration, enabled bool)
}
