package schedquality

import (
	"sort"
	"time"
)

// RuleVersion identifies the rules in FINAL section 12, including its evidence
// windows, strict reset boundary and cooldown ladder.
const RuleVersion = "sched-v2.1"

const (
	SpeedMinSamples = 20
	ErrorMinSamples = 50
	CacheMinSamples = 30
	FastMinSamples  = 6
	FastMinSlow     = 5
	CacheTolerance  = 0.03
	ErrorRateFloor  = 0.05
)

type EvidenceSource string

const (
	EvidenceLocal  EvidenceSource = "local"
	EvidenceShared EvidenceSource = "shared"
	EvidenceOther  EvidenceSource = "other"
)

type Scope struct {
	AccountID int64
	Model     string
	GroupID   int64
}

// EvidenceWindow records the actual inputs of a decision, including the chosen
// duration when there is insufficient evidence. Attempts are in completion order.
type EvidenceWindow struct {
	Source                 EvidenceSource
	Window                 time.Duration
	From, To               time.Time
	AttemptIDs, RequestIDs []string
}

type SpeedEvidence struct {
	EvidenceWindow
	N, Slow, Timeouts int
	P50Ms             *int // nil when absent or the median is a first-output timeout
	P50Timeout        bool
	LCB               float64
}

type ErrorEvidence struct {
	EvidenceWindow
	N, Bad    int
	Rate, LCB float64
}

type CacheEvidence struct {
	EvidenceWindow
	Turns, Sessions, LowSessions int
	ReadTokens, InputTokens      int64
	Ratio                        float64
}

type Evaluation struct {
	Version  string
	Speed    SpeedEvidence
	Error    ErrorEvidence
	Cache    CacheEvidence
	Fast     SpeedEvidence
	Verdicts Verdicts
}

func (e Evaluation) Reason() Reason {
	for i, v := range [...]Verdict{e.Verdicts.Speed, e.Verdicts.Error, e.Verdicts.Cache} {
		if v == VerdictFail {
			return [...]Reason{ReasonSpeed, ReasonError, ReasonCache}[i]
		}
	}
	return ""
}

// IsContinuation compares an attempt with the immediately preceding attempt of
// its group/session/model, across all accounts. Callers must retain that ordering
// rather than skipping intervening failed attempts or attempts on another account.
func IsContinuation(current, previous AttemptSample) bool {
	return current.SessionKey != "" && current.SessionKey == previous.SessionKey &&
		current.GroupID == previous.GroupID && current.Model == previous.Model &&
		current.AccountID == previous.AccountID && previous.Outcome == OutcomeOK &&
		!previous.ClientCanceled && !previous.FinishedAt.IsZero() &&
		!previous.FinishedAt.After(current.StartedAt)
}

// Evaluate chooses evidence independently per dimension. Only completed attempts
// of scope's account/model whose dispatch is strictly after since are eligible.
// Windows include both endpoints and use completion time, as does the fast path.
func Evaluate(samples []AttemptSample, scope Scope, p Policy, since, now time.Time) Evaluation {
	return evaluate(samples, scope, p, since, time.Time{}, now, false)
}

func evaluate(samples []AttemptSample, scope Scope, p Policy, since, completedAfter, now time.Time, other bool) Evaluation {
	rows := completedSamples(samples, scope, since, completedAfter, now, other)
	speedRows, speedWindow := selectEvidence(rows, scope.GroupID, now, 30*time.Minute, 60*time.Minute, SpeedMinSamples, speedSample, other)
	errorRows, errorWindow := selectEvidence(rows, scope.GroupID, now, 60*time.Minute, 120*time.Minute, ErrorMinSamples, errorSample, other)
	cacheRows, cacheWindow := selectEvidence(rows, scope.GroupID, now, 60*time.Minute, 120*time.Minute, CacheMinSamples, cacheSample, other)
	fastRows, fastWindow := selectEvidence(rows, scope.GroupID, now, 5*time.Minute, 5*time.Minute, FastMinSamples, speedSample, other)
	if len(fastRows) > FastMinSamples {
		fastRows = fastRows[len(fastRows)-FastMinSamples:]
		fastWindow = describeEvidence(fastRows, fastWindow.Source, fastWindow.Window)
	}
	e := Evaluation{Version: RuleVersion,
		Speed: speedEvidence(speedRows, speedWindow, p),
		Error: errorEvidence(errorRows, errorWindow),
		Cache: cacheEvidence(cacheRows, cacheWindow, p),
		Fast:  speedEvidence(fastRows, fastWindow, p),
	}
	if !p.Enabled {
		return e
	}
	if e.Speed.N >= SpeedMinSamples {
		e.Verdicts.Speed = VerdictPass
		if e.Speed.LCB > SlowShareTolerance {
			e.Verdicts.Speed = VerdictFail
		}
	}
	if e.Fast.N == FastMinSamples && e.Fast.Slow >= FastMinSlow {
		e.Verdicts.Speed = VerdictFail
	}
	if e.Error.N >= ErrorMinSamples {
		e.Verdicts.Error = VerdictPass
		if e.Error.LCB > max(ErrorRateFloor, p.MaxErrorRate) {
			e.Verdicts.Error = VerdictFail
		}
	}
	if e.Cache.Turns >= CacheMinSamples {
		e.Verdicts.Cache = VerdictPass
		if e.Cache.Ratio < p.MinCacheRatio-CacheTolerance && e.Cache.LowSessions >= 2 {
			e.Verdicts.Cache = VerdictFail
		}
	}
	return e
}

func completedSamples(samples []AttemptSample, scope Scope, since, completedAfter, now time.Time, other bool) []AttemptSample {
	rows := make([]AttemptSample, 0, len(samples))
	seen := make(map[string]bool, len(samples))
	for _, s := range samples {
		if s.AccountID != scope.AccountID || s.Model != scope.Model ||
			!s.StartedAt.After(since) || s.StartedAt.After(s.FinishedAt) ||
			s.FinishedAt.IsZero() || s.FinishedAt.After(now) ||
			s.FinishedAt.Before(now.Add(-120*time.Minute)) ||
			(!completedAfter.IsZero() && !s.FinishedAt.After(completedAfter)) ||
			(other && s.GroupID == scope.GroupID) || seen[s.AttemptID] {
			continue
		}
		seen[s.AttemptID] = true
		rows = append(rows, s)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].FinishedAt.Equal(rows[j].FinishedAt) {
			return rows[i].AttemptID < rows[j].AttemptID
		}
		return rows[i].FinishedAt.Before(rows[j].FinishedAt)
	})
	return rows
}

func speedSample(s AttemptSample) bool {
	return s.Stream && (s.TTFOMs != nil || s.Outcome == OutcomeTimeout)
}

func errorSample(s AttemptSample) bool { return !s.ClientCanceled }

func cacheSample(s AttemptSample) bool {
	return s.Continuation && s.SessionKey != "" && s.Outcome == OutcomeOK && !s.ClientCanceled &&
		s.PromptTokens != nil && s.CacheReadTokens != nil && s.CacheWriteTokens != nil &&
		*s.PromptTokens >= 0 && *s.CacheReadTokens >= 0 && *s.CacheWriteTokens >= 0 &&
		int64(*s.PromptTokens)+int64(*s.CacheReadTokens)+int64(*s.CacheWriteTokens) > 0
}

func selectEvidence(rows []AttemptSample, group int64, now time.Time, short, long time.Duration, minN int, eligible func(AttemptSample) bool, other bool) ([]AttemptSample, EvidenceWindow) {
	sources := []EvidenceSource{EvidenceLocal, EvidenceShared}
	if other {
		sources = []EvidenceSource{EvidenceOther}
	}
	var selected []AttemptSample
	var window EvidenceWindow
	for _, source := range sources {
		for _, duration := range [...]time.Duration{short, long} {
			selected = nil
			for _, row := range rows {
				if (source != EvidenceLocal || row.GroupID == group) && !row.FinishedAt.Before(now.Add(-duration)) && eligible(row) {
					selected = append(selected, row)
				}
			}
			window = describeEvidence(selected, source, duration)
			if len(selected) >= minN {
				return selected, window
			}
		}
	}
	return selected, window
}

func describeEvidence(rows []AttemptSample, source EvidenceSource, duration time.Duration) EvidenceWindow {
	w := EvidenceWindow{Source: source, Window: duration}
	seenRequests := make(map[string]bool)
	for _, s := range rows {
		if w.From.IsZero() {
			w.From = s.FinishedAt
		}
		w.To = s.FinishedAt
		w.AttemptIDs = append(w.AttemptIDs, s.AttemptID)
		if s.RequestID != "" && !seenRequests[s.RequestID] {
			w.RequestIDs = append(w.RequestIDs, s.RequestID)
			seenRequests[s.RequestID] = true
		}
	}
	return w
}

func speedEvidence(rows []AttemptSample, window EvidenceWindow, p Policy) SpeedEvidence {
	e := SpeedEvidence{EvidenceWindow: window, N: len(rows)}
	values := make([]latency, 0, len(rows))
	for _, s := range rows {
		// A measured first output is retained even if a later failure occurred.
		v := latency{timeout: s.TTFOMs == nil && s.Outcome == OutcomeTimeout}
		if s.TTFOMs != nil {
			v.ms = *s.TTFOMs
		}
		if v.timeout {
			e.Timeouts++
		}
		if v.timeout || float64(v.ms) > p.TTFTSeconds*1000 {
			e.Slow++
		}
		values = append(values, v)
	}
	e.LCB = WilsonLower(e.Slow, e.N)
	if len(values) > 0 {
		sortLatencies(values)
		median := values[(len(values)-1)/2]
		e.P50Timeout = median.timeout
		if !median.timeout {
			e.P50Ms = &median.ms
		}
	}
	return e
}

func errorEvidence(rows []AttemptSample, window EvidenceWindow) ErrorEvidence {
	e := ErrorEvidence{EvidenceWindow: window, N: len(rows)}
	for _, s := range rows {
		if s.Outcome != OutcomeOK {
			e.Bad++
		}
	}
	if e.N > 0 {
		e.Rate = float64(e.Bad) / float64(e.N)
	}
	e.LCB = WilsonLower(e.Bad, e.N)
	return e
}

func cacheEvidence(rows []AttemptSample, window EvidenceWindow, p Policy) CacheEvidence {
	e := CacheEvidence{EvidenceWindow: window, Turns: len(rows)}
	type session struct {
		group int64
		key   string
	}
	type tokens struct{ read, input int64 }
	sessions := make(map[session]tokens)
	for _, s := range rows {
		read := int64(*s.CacheReadTokens)
		input := int64(*s.PromptTokens) + read + int64(*s.CacheWriteTokens)
		e.ReadTokens += read
		e.InputTokens += input
		key := session{s.GroupID, s.SessionKey}
		total := sessions[key]
		total.read += read
		total.input += input
		sessions[key] = total
	}
	e.Sessions = len(sessions)
	for _, total := range sessions {
		if float64(total.read)/float64(total.input) < p.MinCacheRatio-CacheTolerance {
			e.LowSessions++
		}
	}
	if e.InputTokens > 0 {
		e.Ratio = float64(e.ReadTokens) / float64(e.InputTokens)
	}
	return e
}
