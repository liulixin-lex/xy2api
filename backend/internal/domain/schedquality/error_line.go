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
	Verdict    Verdict  `json:"verdict"`
	Limit      float64  `json:"limit"` // max(分组上限, 0.05)
	Clear      int      `json:"clear"`
	Failed     int      `json:"failed"`
	Unknown    int      `json:"unknown"`
	Padded     int      `json:"padded"` // n = max(50, k+u)
	LowerFail  float64  `json:"lower_fail"`
	LowerPass  float64  `json:"lower_pass"`
	AttemptIDs []string `json:"-"`
}

// 请求单位按 RequestID（为空时用 AttemptID）分组，StartedAt 最晚的一次尝试是最终结果：
// Outcome==OK 为成功；Class.Counted() 为失败；ClassUnknown 或在途为未知；其余类别不占名额。
// 排序时刻：完成的用 FinishedAt，在途的用 StartedAt。只取 [now-60m, now] 内、StartedAt>cut 的单位中最近 100 个，不从更早的补位。
// Fail：LowerFail>Limit。Pass：k≥50 且 LowerPass≤Limit。其余 Unknown。
func AssessError(samples, running []AttemptSample, limit float64, cut, now time.Time) ErrorAssessment {
	out := ErrorAssessment{Limit: max(limit, ErrorRateFloor)}
	rows := finalRequests(samples, running, now)
	selected := rows[:0]
	for _, s := range rows {
		if !s.StartedAt.After(cut) || requestTime(s).Before(now.Add(-ErrorWindow)) {
			continue
		}
		if s.FinishedAt.IsZero() || s.Outcome == OutcomeOK || s.Class.Counted() || s.Class == ClassUnknown {
			selected = append(selected, s)
		}
	}
	for _, s := range selected[:min(ErrorSlots, len(selected))] {
		switch {
		case s.FinishedAt.IsZero():
			out.Unknown++
		case s.Outcome == OutcomeOK:
			out.Clear++
		case s.Class.Counted():
			out.Clear++
			out.Failed++
			if len(out.AttemptIDs) < 50 {
				out.AttemptIDs = append(out.AttemptIDs, s.AttemptID)
			}
		default:
			out.Unknown++
		}
	}
	out.Padded = max(ErrorMinClear, out.Clear+out.Unknown)
	out.LowerFail = WilsonLower(out.Failed, out.Padded)
	out.LowerPass = WilsonLower(out.Failed+out.Unknown, out.Padded)
	switch {
	case out.LowerFail > out.Limit:
		out.Verdict = VerdictFail
	case out.Clear >= ErrorMinClear && out.LowerPass <= out.Limit:
		out.Verdict = VerdictPass
	}
	return out
}

// finalRequests groups before eligibility filtering, so an excluded final retry
// cannot expose an older failure. Account/model and fallback keys are distinct.
func finalRequests(samples, running []AttemptSample, now time.Time) []AttemptSample {
	type key struct {
		account   int64
		model, id string
		fallback  bool
	}
	latest := make(map[key]AttemptSample)
	for _, rows := range [][]AttemptSample{samples, running} {
		for _, s := range rows {
			if s.StartedAt.After(now) || s.FinishedAt.After(now) ||
				(!s.FinishedAt.IsZero() && s.FinishedAt.Before(s.StartedAt)) {
				continue
			}
			id := s.RequestID
			fallback := id == ""
			if fallback {
				id = s.AttemptID
			}
			k := key{s.AccountID, s.Model, id, fallback}
			old, exists := latest[k]
			if !exists || s.StartedAt.After(old.StartedAt) ||
				(s.StartedAt.Equal(old.StartedAt) && (s.AttemptID > old.AttemptID ||
					(s.AttemptID == old.AttemptID && s.FinishedAt.After(old.FinishedAt)))) {
				latest[k] = s
			}
		}
	}
	out := make([]AttemptSample, 0, len(latest))
	for _, s := range latest {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return newerRequest(out[i], out[j]) })
	return out
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
