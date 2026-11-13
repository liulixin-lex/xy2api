package schedquality

import "time"

type Outcome int8  // OutcomeOther=4 is read-only legacy; cancellation is 5.
type State int8    // StateNormal, StateCooling
type Override int8 // OverrideNone, OverrideExempt
type Verdict int8  // VerdictNoData, VerdictPass, VerdictFail
type Standing int8 // StandFront, StandBack
type Reason string

const (
	OutcomeOK Outcome = iota + 1
	OutcomeTimeout
	OutcomeAccountFault
	OutcomeOther
	OutcomeCanceled
)
const (
	StateNormal State = iota
	StateCooling
)
const (
	OverrideNone Override = iota
	OverrideExempt
)
const (
	VerdictNoData Verdict = iota
	VerdictPass
	VerdictFail Verdict = 3
)
const (
	StandFront Standing = iota
	StandBack
)
const (
	ReasonSpeed Reason = "speed"
	ReasonError Reason = "error"
	ReasonCache Reason = "cache"
)

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
	case OutcomeCanceled:
		return "canceled"
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

type AttemptSample struct {
	AttemptID                        string
	StartedAt, FinishedAt            time.Time
	AccountID, GroupID               int64
	Model                            string
	Stream                           bool
	ClientCanceled, Continuation     bool
	TTFOMs, CensoredMs               *int
	Outcome                          Outcome
	Detail                           string
	PromptTokens, CacheReadTokens    *int
	InputTokens, CacheCreationTokens *int // Uncached input and cache writes; PromptTokens remains total input.
	SessionKey, RequestID            string
	InsertedAt                       time.Time
}
type Policy struct {
	Enabled                                  bool
	TTFTSeconds, MaxErrorRate, MinCacheRatio float64
	TakeoverIdleSeconds                      int
}
type Verdicts struct{ Speed, Error, Cache Verdict }

type StateRow struct {
	AccountID     int64
	Model         string
	GroupID       int64
	State         State
	Level         int
	Reason        Reason
	Since, Until  time.Time
	EvidenceSince time.Time
	Basis         *DecisionBasis
}
type OverrideRef struct {
	Mode    Override
	Expires time.Time
} // zero Expires = no expiry
type StateView struct {
	Failing                    bool
	Dims                       []Dimension
	EvidenceSince              time.Time
	Level                      int
	Since, Until               time.Time
	GroupOverride, AllOverride OverrideRef
}
type View interface {
	Version() int64
	At() time.Time // when this view was built
	GlobalEnabled() bool
	GroupEnabled(groupID int64) bool
	ResetAt(accountID int64) time.Time
	StateOf(groupID, accountID int64, model string) StateView
	TakeoverIdle(groupID int64) (idle time.Duration, enabled bool)
}

func DefaultPolicy() Policy {
	return Policy{TTFTSeconds: 15, MaxErrorRate: .10, MinCacheRatio: .5, TakeoverIdleSeconds: 900}
}
