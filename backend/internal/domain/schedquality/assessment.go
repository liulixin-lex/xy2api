package schedquality

import (
	"math"
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
	Watermark   *int64           `json:"w,omitempty"` // UTC epoch microseconds at the last verified state advance.
	ExtraCauses []string         `json:"u,omitempty"` // Raw causes without an assigned bit.
	FirstAt     time.Time        `json:"f"`
	LastAt      time.Time        `json:"l"`
	Count       int64            `json:"n"`
	Kind        EventKind        `json:"k"`
	Action      Action           `json:"a,omitempty"`
	Reason      AssessmentReason `json:"r"`
	Causes      AssessmentCauses `json:"c,omitempty"`
	Have        *float64         `json:"h,omitempty"`
	Need        *int             `json:"t,omitempty"`
}

type AssessmentCauses uint32

const (
	cacheTurnsShort        = "turns_insufficient"
	cacheLowSessionsShort  = "low_sessions_insufficient"
	errorClearShort        = "clear_insufficient"
	errorBoundsUnconfirmed = "bounds_unconfirmed"
	errorRecoveryOverLimit = "recovery_over_limit"
)

// Append only: indexes are persisted bit positions, never gate priorities.
// Bit 0 is no_peers. SQL readers can test c & (1 << index).
var assessmentCauseNames = [...]string{
	string(RuleBNoPeers), string(RuleBAllPeersRear), string(RuleBPeerKnownInsufficient),
	string(RuleBNoSamples), string(RuleBMassInsufficient), string(RuleBSessionsInsufficient),
	string(RuleBSlowSessionsInsufficient), string(RuleBAbsoluteUnconfirmed), string(RuleBRelativeUnconfirmed),
	string(RuleBWorstMeanAboveTolerance), string(RuleBRelativeVeto),
	cacheTurnsShort, cacheLowSessionsShort, errorClearShort, errorBoundsUnconfirmed, errorRecoveryOverLimit,
}

func (s *AssessmentSegment) addCause(name string) {
	if i := slices.Index(assessmentCauseNames[:], name); i >= 0 && i < 32 {
		s.Causes |= 1 << i
		return
	}
	s.ExtraCauses = append(s.ExtraCauses, name)
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

func (s AssessmentSegment) CauseNames() []string {
	return append(s.Causes.Names(), s.ExtraCauses...)
}

// PostgreSQL parses the fractional second of a text timestamp as a double,
// then rounds fractional microseconds to nearest-even (including half ties).
func assessmentTime(at time.Time) time.Time {
	fraction := float64(at.Nanosecond()) / 1e9
	micros := time.Duration(math.RoundToEven(fraction * 1e6))
	return at.UTC().Truncate(time.Second).Add(micros * time.Microsecond)
}

func (s AssessmentSegment) MatchesWatermark(at time.Time) bool {
	return s.Watermark != nil && !at.IsZero() && *s.Watermark == assessmentTime(at).UnixMicro()
}

// RecordAssessment folds an accepted Step into a new history without mutating
// the committed state. A watermark advanced by an older writer breaks a run.
func RecordAssessment(prev LineState, at time.Time, kind EventKind, verdict Verdict, action Action, basis Basis) []AssessmentSegment {
	if !at.After(prev.EvaluatedAt) {
		return prev.LastAssessment
	}
	at = assessmentTime(at)
	history := prev.LastAssessment
	if kind == EventClock {
		// A clock can carry a verified assessment forward, but cannot repair a
		// gap left by an older writer. Never mutate a committed segment.
		if len(history) == 0 || !history[len(history)-1].MatchesWatermark(prev.EvaluatedAt) {
			return history
		}
		history = slices.Clone(history)
		history[len(history)-1].Watermark = new(at.UnixMicro())
		return history
	}
	segment := AssessmentSegment{FirstAt: at, LastAt: at, Watermark: new(at.UnixMicro()), Count: 1, Kind: kind, Action: action}
	segment.classify(verdict, basis)
	if n := len(history); n > 0 {
		last := history[n-1]
		if last.MatchesWatermark(prev.EvaluatedAt) && last.Kind == kind && last.Action == action && last.Reason == segment.Reason {
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
			if cache.Turns < CacheMinTurns {
				s.addCause(cacheTurnsShort)
				s.consider(ReasonOwnShort, float64(cache.Turns), CacheMinTurns)
			}
			if cache.Turns > 0 && cache.Ratio < cache.MinRatio && cache.LowSessions < cacheMinLowSessions {
				s.addCause(cacheLowSessionsShort)
				s.consider(ReasonOwnShort, float64(cache.LowSessions), cacheMinLowSessions)
			}
		case b.Error != nil:
			errLine := b.Error
			if errLine.LowerPass > errLine.Limit {
				s.addCause(errorBoundsUnconfirmed)
				s.consider(ReasonWithin, 0, 0)
			}
			if errLine.RecoveryOverLimit {
				s.addCause(errorRecoveryOverLimit)
				s.consider(ReasonPending, 0, 0)
			}
			if errLine.Clear < ErrorMinClear {
				s.addCause(errorClearShort)
				s.consider(ReasonOwnShort, float64(errLine.Clear), ErrorMinClear)
			}
		}
		if s.Kind == EventTrial && s.Action == "" {
			s.Reason = ReasonTrialOpen
		}
	}
}

// Only unknown verdicts select among competing causes. At equal priority keep
// the first gate reported by the rule (e.g. TTFT mass before session count).
var unknownReasonPriority = map[AssessmentReason]int{
	ReasonPeerShort: 4,
	ReasonOwnShort:  3,
	ReasonPending:   2,
	ReasonWithin:    1,
}

func (s *AssessmentSegment) consider(reason AssessmentReason, have float64, need int) {
	if unknownReasonPriority[reason] <= unknownReasonPriority[s.Reason] {
		return
	}
	s.Reason = reason
	if need > 0 {
		s.Have, s.Need = &have, &need
	}
}

func (s *AssessmentSegment) classifyTTFT(e *TTFTEvidence, peerKnown float64) {
	unclassified := false
	for _, cause := range e.Causes {
		s.addCause(string(cause))
		switch cause {
		case RuleBNoPeers, RuleBAllPeersRear, RuleBPeerKnownInsufficient:
			s.consider(ReasonPeerShort, peerKnown, ruleBMinMass)
		case RuleBNoSamples, RuleBMassInsufficient:
			s.consider(ReasonOwnShort, e.N, ruleBMinMass)
		case RuleBSessionsInsufficient:
			s.consider(ReasonOwnShort, float64(e.S), ruleBMinSessions)
		case RuleBSlowSessionsInsufficient:
			// D gates a bad verdict only after both probability tests confirm it.
			if !slices.Contains(e.Causes, RuleBAbsoluteUnconfirmed) && !slices.Contains(e.Causes, RuleBRelativeUnconfirmed) {
				s.consider(ReasonOwnShort, float64(e.D), ruleBMinSlowSessions)
			}
		case RuleBAbsoluteUnconfirmed, RuleBRelativeUnconfirmed, RuleBWorstMeanAboveTolerance, RuleBRelativeVeto:
			s.consider(ReasonWithin, 0, 0)
		default:
			unclassified = true
		}
	}
	if unclassified {
		s.Reason, s.Have, s.Need = ReasonOther, nil, nil
	}
}
