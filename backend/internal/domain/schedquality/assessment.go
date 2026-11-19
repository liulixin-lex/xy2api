package schedquality

import (
	"slices"
	"time"
)

type AssessmentReason string

const (
	ReasonPeerShort AssessmentReason = "peer_short"
	ReasonOwnShort  AssessmentReason = "own_short"
	ReasonWithin    AssessmentReason = "within"
	ReasonPending   AssessmentReason = "pending"
	ReasonTrialOpen AssessmentReason = "trial_open"
	ReasonOver      AssessmentReason = "over"
	ReasonOther     AssessmentReason = "other"
)

const maxAssessmentSegments = 8

// AssessmentSegment uses short JSON keys for the bounded last_assessment column.
// Times are UTC. Cause bits have permanent positions in assessmentCauseNames.
type AssessmentSegment struct {
	FirstAt time.Time        `json:"f"`
	LastAt  time.Time        `json:"l"`
	Count   int64            `json:"n"`
	Kind    EventKind        `json:"k"`
	Action  Action           `json:"a,omitempty"`
	Reason  AssessmentReason `json:"r"`
	Causes  AssessmentCauses `json:"c,omitempty"`
	Have    *float64         `json:"h,omitempty"`
	Need    *int             `json:"t,omitempty"`
}

type AssessmentCauses uint32

const (
	cacheTurnsShort        = "turns_insufficient"
	cacheLowSessionsShort  = "low_sessions_insufficient"
	errorClearShort        = "clear_insufficient"
	errorBoundsUnconfirmed = "bounds_unconfirmed"
	errorRecoveryOverLimit = "recovery_over_limit"
)

// Append only. Bit 0 is no_peers. SQL readers can test c & (1 << index).
var assessmentCauseNames = [...]string{
	string(RuleBNoPeers), string(RuleBAllPeersRear), string(RuleBPeerKnownInsufficient),
	string(RuleBNoSamples), string(RuleBMassInsufficient), string(RuleBSessionsInsufficient),
	string(RuleBSlowSessionsInsufficient), string(RuleBAbsoluteUnconfirmed), string(RuleBRelativeUnconfirmed),
	string(RuleBWorstMeanAboveTolerance), string(RuleBRelativeVeto),
	cacheTurnsShort, cacheLowSessionsShort, errorClearShort, errorBoundsUnconfirmed, errorRecoveryOverLimit,
}

func (c *AssessmentCauses) add(name string) {
	if i := slices.Index(assessmentCauseNames[:], name); i >= 0 {
		*c |= 1 << i
	}
}

func (c AssessmentCauses) Names() []string {
	names := []string{}
	for i, name := range assessmentCauseNames {
		if c&(1<<i) != 0 {
			names = append(names, name)
		}
	}
	return names
}

// RecordAssessment folds an accepted Step into a new history without mutating
// the committed state. A watermark advanced by an older writer breaks a run.
func RecordAssessment(prev LineState, at time.Time, kind EventKind, verdict Verdict, action Action, basis Basis) []AssessmentSegment {
	if kind == EventClock || !at.After(prev.EvaluatedAt) {
		return prev.LastAssessment
	}
	at = at.UTC()
	segment := AssessmentSegment{FirstAt: at, LastAt: at, Count: 1, Kind: kind, Action: action}
	segment.classify(verdict, basis)
	history := prev.LastAssessment
	if n := len(history); n > 0 {
		last := history[n-1]
		if last.LastAt.Equal(prev.EvaluatedAt) && last.Kind == kind && last.Action == action && last.Reason == segment.Reason {
			segment.FirstAt, segment.Count = last.FirstAt, last.Count+1
			history = history[:n-1]
		}
	}
	start := max(0, len(history)-(maxAssessmentSegments-1))
	// A full slice expression forces append to allocate a private backing array.
	return append(history[start:len(history):len(history)], segment)
}

func (s *AssessmentSegment) classify(verdict Verdict, b Basis) {
	switch verdict {
	case VerdictPass:
		s.Reason = ReasonWithin
	case VerdictFail:
		s.Reason = ReasonOver
		if s.Kind == EventRegular && b.Line != DimensionTTFO && s.Action != ActionEnter {
			s.Reason = ReasonPending
		}
	default:
		s.Reason = ReasonOther
		switch {
		case b.TTFT != nil:
			s.classifyTTFT(b.TTFT, b.PeerKnown)
		case b.Cache != nil:
			cache := b.Cache
			if cache.Turns > 0 && cache.Ratio < cache.MinRatio && cache.LowSessions < cacheMinLowSessions {
				s.Causes.add(cacheLowSessionsShort)
				s.short(ReasonOwnShort, float64(cache.LowSessions), cacheMinLowSessions)
			}
			if cache.Turns < CacheMinTurns {
				s.Causes.add(cacheTurnsShort)
				s.short(ReasonOwnShort, float64(cache.Turns), CacheMinTurns)
			}
		case b.Error != nil:
			errLine := b.Error
			if errLine.LowerPass > errLine.Limit {
				s.Causes.add(errorBoundsUnconfirmed)
				s.Reason = ReasonWithin
			}
			if errLine.RecoveryOverLimit {
				s.Causes.add(errorRecoveryOverLimit)
				s.Reason = ReasonPending
			}
			if errLine.Clear < ErrorMinClear {
				s.Causes.add(errorClearShort)
				s.short(ReasonOwnShort, float64(errLine.Clear), ErrorMinClear)
			}
		}
		if s.Kind == EventTrial && s.Action == "" {
			s.Reason = ReasonTrialOpen
		}
	}
}

func (s *AssessmentSegment) short(reason AssessmentReason, have float64, need int) {
	s.Reason, s.Have, s.Need = reason, &have, &need
}

func (s *AssessmentSegment) classifyTTFT(e *TTFTEvidence, peerKnown float64) {
	for _, cause := range e.Causes {
		s.Causes.add(string(cause))
	}
	// Resolve by gate priority even if the rule supplies causes in another order.
	for i, name := range assessmentCauseNames {
		if s.Causes&(1<<i) == 0 {
			continue
		}
		switch RuleBUnknownReason(name) {
		case RuleBNoPeers, RuleBAllPeersRear, RuleBPeerKnownInsufficient:
			s.short(ReasonPeerShort, peerKnown, ruleBMinMass)
		case RuleBNoSamples, RuleBMassInsufficient:
			s.short(ReasonOwnShort, e.N, ruleBMinMass)
		case RuleBSessionsInsufficient:
			s.short(ReasonOwnShort, float64(e.S), ruleBMinSessions)
		case RuleBSlowSessionsInsufficient:
			s.short(ReasonOwnShort, float64(e.D), ruleBMinSlowSessions)
		case RuleBAbsoluteUnconfirmed, RuleBRelativeUnconfirmed, RuleBWorstMeanAboveTolerance, RuleBRelativeVeto:
			s.Reason = ReasonWithin
		}
		return
	}
}
