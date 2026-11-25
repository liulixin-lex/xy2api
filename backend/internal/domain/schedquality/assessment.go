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
// Times are UTC. Cause bits have permanent positions in assessmentCauses.
type AssessmentSegment struct {
	Watermark *int64           `json:"w,omitempty"` // UTC epoch microseconds at the last verified state advance.
	FirstAt   time.Time        `json:"f"`
	LastAt    time.Time        `json:"l"`
	Count     int64            `json:"n"`
	Kind      EventKind        `json:"k"`
	Action    Action           `json:"a,omitempty"`
	Reason    AssessmentReason `json:"r"`
	Causes    AssessmentCauses `json:"c,omitempty"`
	Have      *float64         `json:"h,omitempty"`
	Need      *int             `json:"t,omitempty"`
}

type AssessmentCauses uint32

const (
	cacheTurnsShort        = "turns_insufficient"
	cacheLowSessionsShort  = "low_sessions_insufficient"
	errorClearShort        = "clear_insufficient"
	errorBoundsUnconfirmed = "bounds_unconfirmed"
	errorRecoveryOverLimit = "recovery_over_limit"
)

type ruleBReasonInput struct {
	*RuleBResult
	tolerance float64
}

type assessmentCause struct {
	name   RuleBUnknownReason
	reason AssessmentReason
	line   Dimension // Owning line; independent of the persisted bit position.
	// Rule B emits this cause when the predicate holds on an unknown verdict.
	ruleB func(ruleBReasonInput) bool
}

const otherCauseIndex = 16

// Append only: indexes are persisted bit positions, not gate priorities.
// Rule B and the assessment summary use the same names and classifications.
// Retired peer gates keep their persisted slots but no longer have predicates.
var assessmentCauses = [...]assessmentCause{
	{RuleBNoPeers, ReasonPeerShort, DimensionTTFO, nil},
	{RuleBAllPeersRear, ReasonPeerShort, DimensionTTFO, nil},
	{RuleBPeerKnownInsufficient, ReasonPeerShort, DimensionTTFO, nil},
	{RuleBNoSamples, ReasonOwnShort, DimensionTTFO, func(g ruleBReasonInput) bool { return g.Counts.N == 0 }},
	{RuleBMassInsufficient, ReasonOwnShort, DimensionTTFO, func(g ruleBReasonInput) bool { return g.Counts.N < ruleBMinMass-ruleBMassRounding }},
	{RuleBSessionsInsufficient, ReasonOwnShort, DimensionTTFO, func(g ruleBReasonInput) bool { return g.Counts.S < ruleBMinSessions }},
	{RuleBSlowSessionsInsufficient, ReasonOwnShort, DimensionTTFO, func(g ruleBReasonInput) bool { return g.Counts.D < ruleBMinSlowSessions }},
	{RuleBAbsoluteUnconfirmed, ReasonWithin, DimensionTTFO, func(g ruleBReasonInput) bool { return !g.Absolute }},
	{RuleBRelativeUnconfirmed, ReasonWithin, DimensionTTFO, nil},
	{RuleBWorstMeanAboveTolerance, ReasonWithin, DimensionTTFO, func(g ruleBReasonInput) bool { return g.WorstMean > g.tolerance }},
	{RuleBRelativeVeto, ReasonWithin, DimensionTTFO, nil},
	{cacheTurnsShort, ReasonOwnShort, DimensionCache, nil},
	{cacheLowSessionsShort, ReasonOwnShort, DimensionCache, nil},
	{errorClearShort, ReasonOwnShort, DimensionError, nil},
	{errorBoundsUnconfirmed, ReasonWithin, DimensionError, nil},
	{errorRecoveryOverLimit, ReasonPending, DimensionError, nil},
	otherCauseIndex: {"other", ReasonOther, "", nil},
}

func assessmentCauseIndex(name RuleBUnknownReason) int {
	for i, def := range assessmentCauses {
		if def.name == name {
			return i
		}
	}
	return otherCauseIndex
}

func (s *AssessmentSegment) addCause(name RuleBUnknownReason, have float64, need int) {
	i := assessmentCauseIndex(name)
	s.Causes |= 1 << i
	s.consider(assessmentCauses[i].reason, have, need)
}

func (c AssessmentCauses) Names() []string {
	names := []string{}
	for i, def := range assessmentCauses {
		if c&(1<<i) != 0 {
			names = append(names, string(def.name))
		}
	}
	return names
}

func (s AssessmentSegment) CauseNames() []string {
	return s.Causes.Names()
}

// Go and PostgreSQL can round a half-microsecond tie to adjacent microseconds.
func (s AssessmentSegment) MatchesWatermark(at time.Time) bool {
	return s.Watermark != nil && !at.IsZero() &&
		at.Round(time.Microsecond).Sub(time.UnixMicro(*s.Watermark)).Abs() <= time.Microsecond
}

// RecordAssessment folds an accepted Step into a new history without mutating
// the committed state. A watermark advanced by an older writer breaks a run.
func RecordAssessment(prev LineState, at time.Time, kind EventKind, verdict Verdict, action Action, basis Basis) []AssessmentSegment {
	if !at.After(prev.EvaluatedAt) {
		return prev.LastAssessment
	}
	at = at.UTC().Round(time.Microsecond)
	history := prev.LastAssessment
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
			s.classifyTTFT(b.TTFT)
		case b.Cache != nil:
			cache := b.Cache
			need := CacheMinTurns
			if cache.Ratio < cache.MinRatio && cacheDeficitAtLeast(cache.Ratio, cache.MinRatio, cacheDeepDeficit) {
				need = cacheDeepMinTurns
			}
			if s.Kind == EventTrial && cache.Ratio >= cache.MinRatio {
				need = TrialCacheTurns
			}
			if cache.Turns < need {
				s.addCause(cacheTurnsShort, float64(cache.Turns), need)
			}
			if cache.Turns > 0 && cache.Ratio < cache.MinRatio && cache.LowSessions10 != nil && *cache.LowSessions10 == 0 {
				s.addCause(cacheLowSessionsShort, float64(*cache.LowSessions10), 1)
			}
		case b.Error != nil:
			errLine := b.Error
			if errLine.LowerPass > errLine.Limit {
				s.addCause(errorBoundsUnconfirmed, 0, 0)
			}
			if errLine.RecoveryOverLimit {
				s.addCause(errorRecoveryOverLimit, 0, 0)
			}
			if errLine.Clear < ErrorMinClear {
				s.addCause(errorClearShort, float64(errLine.Clear), ErrorMinClear)
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
	ReasonOwnShort: 3,
	ReasonPending:  2,
	ReasonWithin:   1,
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

func (s *AssessmentSegment) classifyTTFT(e *TTFTEvidence) {
	for _, name := range e.Causes {
		i := assessmentCauseIndex(name)
		def := assessmentCauses[i]
		if def.line != DimensionTTFO {
			s.Causes |= 1 << otherCauseIndex
			continue
		}
		s.Causes |= 1 << i
		if def.ruleB == nil {
			continue // Historical cause bits still decode; retired gates do not classify current assessments.
		}
		switch def.reason {
		case ReasonOwnShort:
			switch name {
			case RuleBSessionsInsufficient:
				s.consider(def.reason, float64(e.S), ruleBMinSessions)
			case RuleBSlowSessionsInsufficient:
				// D gates a bad verdict once the absolute probability confirms it.
				if !slices.Contains(e.Causes, RuleBAbsoluteUnconfirmed) {
					s.consider(def.reason, float64(e.D), ruleBMinSlowSessions)
				}
			default:
				s.consider(def.reason, e.N, ruleBMinMass)
			}
		default:
			s.consider(def.reason, 0, 0)
		}
	}
}
