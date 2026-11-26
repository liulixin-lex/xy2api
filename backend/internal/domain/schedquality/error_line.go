package schedquality

import (
	"sort"
	"time"
)

const (
	ErrorWindow   = 60 * time.Minute
	ErrorSlots    = 100
	ErrorMinClear = 50
	WilsonZ       = 1.959964
)

type ErrorAssessment struct {
	Verdict      Verdict  `json:"verdict"`
	Limit        float64  `json:"limit"` // max(分组上限, ErrorRateFloor)
	Clear        int      `json:"clear"`
	Failed       int      `json:"failed"`
	Unknown      int      `json:"unknown"`
	Padded       int      `json:"padded"` // n = max(50, k+u)
	LowerFail    float64  `json:"lower_fail"`
	LowerPass    float64  `json:"lower_pass"`
	AttemptIDs   []string `json:"-"`
	SourceGroups []int64  `json:"-"` // Actual selected request units, deduplicated and ascending.

	// RecoveryOverLimit explains why an otherwise passing assessment became unknown.
	RecoveryOverLimit bool `json:"recovery_over_limit,omitempty"`
}

// EffectiveErrorRate is the failure-rate cap used by assessment and its admin examples.
func EffectiveErrorRate(configured float64) float64 {
	return max(configured, ErrorRateFloor)
}

// 请求单位按 RequestID（为空时用 AttemptID）分组，保留最早开始时间与最终尝试。
// 最终尝试按 (StartedAt, FinishedAt, AttemptID) 排序；同起点的不同尝试，在途排在完成之后。
// Outcome==OK 为成功；Class.Counted() 为失败；ClassUnknown、失败却 ClassNone 或在途为未知；其余类别不占名额。
// 排序时刻：完成的用最终 FinishedAt，在途的用请求最早开始时间；cut 也比较请求最早开始时间。
// 只取 [now-60m, now] 内、请求起点>cut 的单位中最近 100 个，不从更早的补位。
// Fail：LowerFail>Limit。Pass：k≥50 且 LowerPass≤Limit。其余 Unknown。
func AssessError(samples, running []AttemptSample, limit float64, cut, now time.Time) ErrorAssessment {
	assessment, _ := AssessErrorForGroup(samples, running, limit, cut, now, 0)
	return assessment
}

// AssessErrorForGroup counts the target group's clear outcomes after final-attempt selection and slot truncation.
func AssessErrorForGroup(samples, running []AttemptSample, limit float64, cut, now time.Time, groupID int64) (ErrorAssessment, int) {
	out := ErrorAssessment{Limit: EffectiveErrorRate(limit)}
	local := 0
	rows := finalRequests(samples, running, now)
	selected := rows[:0]
	for _, unit := range rows {
		if !unit.startedAt.After(cut) || unit.at().Before(now.Add(-ErrorWindow)) {
			continue
		}
		s := unit.final
		if s.FinishedAt.IsZero() || s.Outcome == OutcomeOK || s.Class.Counted() || s.Class == ClassUnknown || s.Class == ClassNone {
			selected = append(selected, unit)
		}
	}
	groups := make(map[int64]struct{})
	for _, unit := range selected[:min(ErrorSlots, len(selected))] {
		s := unit.final
		groups[s.GroupID] = struct{}{}
		switch {
		case s.FinishedAt.IsZero():
			out.Unknown++
		case s.Outcome == OutcomeOK || s.Class.Counted():
			out.Clear++
			if s.GroupID == groupID {
				local++
			}
			if s.Outcome == OutcomeOK {
				continue
			}
			out.Failed++
			if len(out.AttemptIDs) < 50 {
				out.AttemptIDs = append(out.AttemptIDs, s.AttemptID)
			}
		default:
			out.Unknown++
		}
	}
	out.SourceGroups = sortedSourceGroups(groups)
	out.Padded = max(ErrorMinClear, out.Clear+out.Unknown)
	out.LowerFail = WilsonLower(out.Failed, out.Padded)
	out.LowerPass = WilsonLower(out.Failed+out.Unknown, out.Padded)
	switch {
	case out.LowerFail > out.Limit:
		out.Verdict = VerdictFail
	case out.Clear >= ErrorMinClear && out.LowerPass <= out.Limit:
		out.Verdict = VerdictPass
	}
	return out, local
}

// ForRecovery requires a passing assessment's observed failure rate to meet the limit.
func (a ErrorAssessment) ForRecovery() ErrorAssessment {
	if a.Verdict == VerdictPass && float64(a.Failed)/float64(a.Clear) > a.Limit {
		a.Verdict = VerdictUnknown
		a.RecoveryOverLimit = true
	}
	return a
}

func sortedSourceGroups(groups map[int64]struct{}) []int64 {
	var ids []int64
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

type requestUnit struct {
	startedAt time.Time
	final     AttemptSample
}

func (u requestUnit) at() time.Time {
	if u.final.FinishedAt.IsZero() {
		return u.startedAt
	}
	return u.final.FinishedAt
}

// finalRequests groups before cut/window/class filtering. A retry never changes
// the request's first start or revives an older result excluded by its successor.
// Reconcile Begin/Finish per attempt first: otherwise an old Begin arriving after
// another attempt's Finish could incorrectly become the final unfinished retry.
func finalRequests(samples, running []AttemptSample, now time.Time) []requestUnit {
	type key struct {
		account   int64
		model, id string
		fallback  bool
	}
	type attemptKey struct {
		request key
		id      string
	}
	attempts := make(map[attemptKey]AttemptSample)
	for _, rows := range [][]AttemptSample{samples, running} {
		for _, s := range rows {
			if s.StartedAt.After(now) ||
				(!s.FinishedAt.IsZero() && s.FinishedAt.Before(s.StartedAt)) {
				continue
			}
			// Replaying before completion must not expose the eventual outcome or
			// resurrect an earlier retry's failure. s is a copy of the input row.
			if s.FinishedAt.After(now) {
				s.FinishedAt = time.Time{}
			}
			id := s.RequestID
			fallback := id == ""
			if fallback {
				id = s.AttemptID
			}
			k := key{s.AccountID, s.Model, id, fallback}
			ak := attemptKey{k, s.AttemptID}
			old, exists := attempts[ak]
			if !exists || (old.FinishedAt.IsZero() && !s.FinishedAt.IsZero()) ||
				(old.FinishedAt.IsZero() == s.FinishedAt.IsZero() && laterAttempt(s, old)) {
				attempts[ak] = s
			}
		}
	}
	units := make(map[key]requestUnit)
	for k, s := range attempts {
		u, exists := units[k.request]
		if !exists {
			u = requestUnit{startedAt: s.StartedAt, final: s}
		} else {
			if s.StartedAt.Before(u.startedAt) {
				u.startedAt = s.StartedAt
			}
			if laterAttempt(s, u.final) {
				u.final = s
			}
		}
		units[k.request] = u
	}
	out := make([]requestUnit, 0, len(units))
	for _, u := range units {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].at().Equal(out[j].at()) {
			return out[i].at().After(out[j].at())
		}
		return newerRequest(out[i].final, out[j].final)
	})
	return out
}

// Distinct attempts are ordered by start, finish (unfinished is last), then ID.
// Same-attempt Begin/Finish precedence is resolved before this comparison.
func laterAttempt(a, b AttemptSample) bool {
	if !a.StartedAt.Equal(b.StartedAt) {
		return a.StartedAt.After(b.StartedAt)
	}
	if a.FinishedAt.IsZero() != b.FinishedAt.IsZero() {
		return a.FinishedAt.IsZero()
	}
	if !a.FinishedAt.Equal(b.FinishedAt) {
		return a.FinishedAt.After(b.FinishedAt)
	}
	return a.AttemptID > b.AttemptID
}

func requestTime(s AttemptSample) time.Time {
	if s.FinishedAt.IsZero() {
		return s.StartedAt
	}
	return s.FinishedAt
}
func newerRequest(a, b AttemptSample) bool {
	if !requestTime(a).Equal(requestTime(b)) {
		return requestTime(a).After(requestTime(b))
	}
	if !a.StartedAt.Equal(b.StartedAt) {
		return a.StartedAt.After(b.StartedAt)
	}
	if a.AttemptID != b.AttemptID {
		return a.AttemptID > b.AttemptID
	}
	if a.AccountID != b.AccountID {
		return a.AccountID > b.AccountID
	}
	if a.Model != b.Model {
		return a.Model > b.Model
	}
	return a.RequestID > b.RequestID
}
