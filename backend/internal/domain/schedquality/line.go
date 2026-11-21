package schedquality

import (
	"fmt"
	"time"
)

type Identity int8

const (
	IdentityUnknown Identity = 0
	IdentityPass    Identity = 1
	IdentityRear    Identity = 2
)

type Verdict int8 // 沿用原有数值；VerdictNoData 改名为 VerdictUnknown，String() 返回 "unknown"
const (
	VerdictUnknown Verdict = 0
	VerdictPass    Verdict = 1
	VerdictFail    Verdict = 3
)

var Lines = [...]Dimension{DimensionTTFO, DimensionError, DimensionCache}

type LineKey struct {
	AccountID int64
	Model     string
	GroupID   int64
	Line      Dimension
}
type LineState struct {
	LineKey
	Identity                     Identity
	Trial                        bool
	Level                        int                 // 0..4，对应 CooldownLadder
	Admissions                   int                 // 进后排次数；大于 0 即“有过失败记录”
	PendingAt                    time.Time           `json:",omitzero"` // 报错线和缓存线：上一次普通评估不达标的时刻
	Epoch                        time.Time           `json:",omitzero"` // 取证起点（常规评估和试用）；零值表示不截
	Entry                        time.Time           `json:",omitzero"` // 本轮进后排的时刻，到期复核从这里截
	Until, TrialUntil, GoodSince time.Time           `json:",omitzero"`
	LastRear                     time.Time           `json:",omitzero"` // 最近一次进后排、续期或试用判回后排
	EvaluatedAt                  time.Time           `json:",omitzero"`
	LastAssessment               []AssessmentSegment `json:",omitempty"`
	Basis                        *Basis              // 最近一次身份变化时的证据
}
type EventKind uint8

const (
	EventRegular EventKind = iota
	EventTrial
	EventReview
	EventClock
)

type Action string // enter renew release trial trial_pass trial_fail trial_timeout reset relax clear，各取同名常量 ActionEnter…ActionClear
var CooldownLadder = [...]time.Duration{20 * time.Minute, 40 * time.Minute, 80 * time.Minute, 160 * time.Minute, 240 * time.Minute}

const (
	RegularInterval  = 5 * time.Minute
	TrialDuration    = 120 * time.Minute
	TrialMinimum     = 15 * time.Minute
	TrialSampleCheck = 60 * time.Minute
	LevelClearQuiet  = 24 * time.Hour
)

const (
	ActionEnter        Action = "enter"
	ActionRenew        Action = "renew"
	ActionRelease      Action = "release"
	ActionTrial        Action = "trial"
	ActionTrialPass    Action = "trial_pass"
	ActionTrialFail    Action = "trial_fail"
	ActionTrialTimeout Action = "trial_timeout"
	ActionReset        Action = "reset"
	ActionRelax        Action = "relax"
	ActionClear        Action = "clear"
)

func (v Verdict) String() string {
	switch v {
	case VerdictUnknown:
		return "unknown"
	case VerdictPass:
		return "pass"
	case VerdictFail:
		return "fail"
	}
	return ""
}

func (v Verdict) MarshalText() ([]byte, error) {
	text := v.String()
	if text == "" {
		return nil, fmt.Errorf("invalid quality verdict: %d", v)
	}
	return []byte(text), nil
}

func (v *Verdict) UnmarshalText(text []byte) error {
	switch string(text) {
	case "unknown":
		*v = VerdictUnknown
	case "pass":
		*v = VerdictPass
	case "fail":
		*v = VerdictFail
	default:
		return fmt.Errorf("invalid quality verdict: %q", text)
	}
	return nil
}
