package schedquality

import (
	"sort"
	"time"
)

const (
	CacheWindow             = 30 * time.Minute
	cacheLowSessionMinTurns = 10
	cacheDeepMinTurns       = 10
	cacheSingleDeficit      = .05
	cacheDeepDeficit        = .20
	CacheMinTurns           = 20
	ContinuationGap         = 5 * time.Minute
)

// Cache failure paths recorded in decision evidence.
const (
	CachePathPair    = "pair"
	CachePathSingle5 = "single5"
	CachePathDeep10  = "deep10"
)

type CacheAssessment struct {
	Verdict       Verdict   `json:"verdict"`
	MinRatio      float64   `json:"min_ratio"`
	Ratio         float64   `json:"ratio"`
	Turns         int       `json:"turns"`
	Sessions      int       `json:"sessions"`
	LowSessions   int       `json:"low_sessions"`
	LowSessions10 *int      `json:"low_sessions_10"`
	Path          string    `json:"path,omitempty"`
	WindowFrom    time.Time `json:"window_from,omitzero"`
	WindowTo      time.Time `json:"window_to,omitzero"`
	ReadTokens    int64     `json:"read_tokens"`
	InputTokens   int64     `json:"input_tokens"`
	AttemptIDs    []string  `json:"-"`
	SourceGroups  []int64   `json:"-"` // Actual selected continuation turns, deduplicated and ascending.
}

// 只取续接轮、本轮成功、token 都已知、FinishedAt∈[now-30m,now]、StartedAt>cut 的样本。
// Pass requires 20 turns at or above the floor. Fail requires a low session with
// 10 own turns and 20 total turns (10 for a deficit of at least 20 points).
func AssessCache(samples []AttemptSample, minRatio float64, cut, now time.Time) CacheAssessment {
	assessment, _ := AssessCacheForGroup(samples, minRatio, cut, now, 0)
	return assessment
}

// AssessCacheForGroup counts only continuation turns with usable token evidence.
func AssessCacheForGroup(samples []AttemptSample, minRatio float64, cut, now time.Time, groupID int64) (CacheAssessment, int) {
	out := CacheAssessment{LowSessions10: new(0), MinRatio: minRatio, WindowFrom: now.Add(-CacheWindow), WindowTo: now}
	local := 0
	type session struct {
		group int64
		key   string
	}
	type tokens struct {
		read, input int64
		turns       int
	}
	sessions := make(map[session]tokens)
	groups := make(map[int64]struct{})
	var bad []AttemptSample
	for _, s := range completedAttempts(samples, cut, now.Add(-CacheWindow), now) {
		read, input, ok := ContinuationTokens(s)
		if !ok {
			continue
		}
		groups[s.GroupID] = struct{}{}
		out.Turns++
		if s.GroupID == groupID {
			local++
		}
		out.ReadTokens += read
		out.InputTokens += input
		k := session{s.GroupID, s.SessionKey}
		tally := sessions[k]
		tally.turns++
		tally.read += read
		tally.input += input
		sessions[k] = tally
		if float64(read)/float64(input) < minRatio {
			bad = append(bad, s)
		}
	}
	out.SourceGroups = sortedSourceGroups(groups)
	out.Sessions = len(sessions)
	for _, tally := range sessions {
		if float64(tally.read)/float64(tally.input) < minRatio {
			out.LowSessions++
			if tally.turns >= cacheLowSessionMinTurns {
				*out.LowSessions10++
			}
		}
	}
	if out.InputTokens > 0 {
		out.Ratio = float64(out.ReadTokens) / float64(out.InputTokens)
	}
	if out.Turns >= CacheMinTurns && out.Ratio >= minRatio {
		out.Verdict = VerdictPass
	} else if out.Ratio < minRatio && *out.LowSessions10 > 0 {
		switch {
		case out.Turns >= CacheMinTurns:
			out.Verdict, out.Path = VerdictFail, CachePathPair
			if cacheDeficitAtLeast(out.Ratio, minRatio, cacheSingleDeficit) {
				out.Path = CachePathSingle5
			}
		case out.Turns >= cacheDeepMinTurns && cacheDeficitAtLeast(out.Ratio, minRatio, cacheDeepDeficit):
			out.Verdict, out.Path = VerdictFail, CachePathDeep10
		}
	}
	sort.Slice(bad, func(i, j int) bool { return newerRequest(bad[i], bad[j]) })
	for _, s := range bad[:min(50, len(bad))] {
		out.AttemptIDs = append(out.AttemptIDs, s.AttemptID)
	}
	return out, local
}

// IsContinuation requires the immediately preceding group/model/session attempt.
// The caller finds it before cutting evidence; a predecessor can predate cut.
func IsContinuation(current, previous AttemptSample) bool {
	return current.SessionKey != "" && current.SessionKey == previous.SessionKey &&
		current.GroupID == previous.GroupID && current.Model == previous.Model &&
		current.AccountID == previous.AccountID && previous.Outcome == OutcomeOK &&
		!previous.ClientCanceled && !previous.FinishedAt.IsZero() &&
		!previous.FinishedAt.After(current.StartedAt) &&
		!previous.FinishedAt.Before(current.StartedAt.Add(-ContinuationGap))
}

// ContinuationTokens returns the cache-line contribution of a resolved session turn.
func ContinuationTokens(s AttemptSample) (read, input int64, ok bool) {
	if !s.Continuation || s.SessionKey == "" {
		return 0, 0, false
	}
	return cacheTokens(s)
}

// InputTokens stores uncached input; both cache reads and writes form the total.
func cacheTokens(s AttemptSample) (read, input int64, ok bool) {
	if s.Outcome != OutcomeOK || s.ClientCanceled || s.InputTokens == nil ||
		s.CacheReadTokens == nil || s.CacheCreationTokens == nil ||
		*s.InputTokens < 0 || *s.CacheReadTokens < 0 || *s.CacheCreationTokens < 0 {
		return 0, 0, false
	}
	read = int64(*s.CacheReadTokens)
	input = int64(*s.InputTokens) + read + int64(*s.CacheCreationTokens)
	return read, input, input > 0
}

func completedAttempts(samples []AttemptSample, cut, from, now time.Time) []AttemptSample {
	rows := make([]AttemptSample, 0, len(samples))
	seen := make(map[string]bool)
	for _, s := range samples {
		if s.FinishedAt.IsZero() || s.FinishedAt.Before(from) || s.FinishedAt.After(now) ||
			s.StartedAt.After(s.FinishedAt) || !s.StartedAt.After(cut) || seen[s.AttemptID] {
			continue
		}
		seen[s.AttemptID] = true
		rows = append(rows, s)
	}
	return rows
}

// At magnitude at most 1, float64 division, subtraction and rounding each
// decimal threshold contribute at most half an ulp each. Four units of 2^-52
// cover these steps and any rounding of the integer token totals on conversion.
func cacheDeficitAtLeast(ratio, floor, deficit float64) bool {
	const rounding = 4 * 0x1p-52
	return floor-ratio >= deficit-rounding
}
