package schedquality

import "time"

type Outcome int8
type Override int8

const (
	OutcomeOK Outcome = iota + 1
	OutcomeTimeout
	OutcomeAccountFault
	OutcomeOther
	OutcomeCanceled
)
const (
	OverrideNone Override = iota
	OverrideExempt
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

type AttemptSample struct {
	AttemptID                        string
	StartedAt, FinishedAt            time.Time
	AccountID, GroupID               int64
	Model                            string
	Stream                           bool
	ClientCanceled, Continuation     bool
	Switched                         bool
	TTFOMs, CensoredMs               *int
	FirstEventMs, FirstAnswerMs      *int
	Outcome                          Outcome
	Class                            ErrorClass
	HTTPStatus                       *int
	StatusOrigin                     StatusOrigin
	OutputBytes                      *int
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
type OverrideRef struct {
	Mode    Override
	Expires time.Time
} // zero Expires = no expiry
func DefaultPolicy() Policy {
	return Policy{TTFTSeconds: 15, MaxErrorRate: .10, MinCacheRatio: .5, TakeoverIdleSeconds: 900}
}
