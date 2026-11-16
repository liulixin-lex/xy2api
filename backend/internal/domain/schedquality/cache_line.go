package schedquality

import (
	"sort"
	"time"
)

const (
	CacheWindow     = 30 * time.Minute
	CacheMinTurns   = 20
	ContinuationGap = 5 * time.Minute
)

type CacheAssessment struct {
	Verdict     Verdict  `json:"verdict"`
	MinRatio    float64  `json:"min_ratio"`
	Ratio       float64  `json:"ratio"`
	Turns       int      `json:"turns"`
	Sessions    int      `json:"sessions"`
	LowSessions int      `json:"low_sessions"`
	ReadTokens  int64    `json:"read_tokens"`
	InputTokens int64    `json:"input_tokens"`
	AttemptIDs  []string `json:"-"`
}

// 只取续接轮、本轮成功、token 都已知、FinishedAt∈[now-30m,now]、StartedAt>cut 的样本。
// Pass：Turns≥20 且 Ratio≥MinRatio。Fail：Turns≥20、Ratio<MinRatio 且 LowSessions≥2。其余 Unknown。没有 3 个百分点的容差。
func AssessCache(samples []AttemptSample, minRatio float64, cut, now time.Time) CacheAssessment {
	out := CacheAssessment{MinRatio: minRatio}
	type session struct {
		group int64
		key   string
	}
	type tokens struct{ read, input int64 }
	sessions := make(map[session]tokens)
	var bad []AttemptSample
	for _, s := range completedAttempts(samples, cut, now.Add(-CacheWindow), now) {
		if !s.Continuation || s.SessionKey == "" {
			continue
		}
		read, input, ok := cacheTokens(s)
		if !ok {
			continue
		}
		out.Turns++
		out.ReadTokens += read
		out.InputTokens += input
		k := session{s.GroupID, s.SessionKey}
		tally := sessions[k]
		tally.read += read
		tally.input += input
		sessions[k] = tally
		if float64(read)/float64(input) < minRatio {
			bad = append(bad, s)
		}
	}
	out.Sessions = len(sessions)
	for _, tally := range sessions {
		if float64(tally.read)/float64(tally.input) < minRatio {
			out.LowSessions++
		}
	}
	if out.InputTokens > 0 {
		out.Ratio = float64(out.ReadTokens) / float64(out.InputTokens)
	}
	if out.Turns >= CacheMinTurns {
		if out.Ratio >= minRatio {
			out.Verdict = VerdictPass
		} else if out.LowSessions >= 2 {
			out.Verdict = VerdictFail
		}
	}
	sort.Slice(bad, func(i, j int) bool { return newerRequest(bad[i], bad[j]) })
	for _, s := range bad[:min(50, len(bad))] {
		out.AttemptIDs = append(out.AttemptIDs, s.AttemptID)
	}
	return out
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
