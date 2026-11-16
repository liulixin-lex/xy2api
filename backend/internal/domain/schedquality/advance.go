package schedquality

import "time"

// Due：fiveMin 表示 at 是 UTC 5 分钟边界；touched 表示 at 有该账号×模型的新样本，或是整分钟。
// 后排且 at≥Until → Review；试用且（touched 或 at≥TrialUntil）→ Trial；
// 前排非试用：fiveMin → Regular，否则 (Level>0 || Admissions>0) 且 at≥LastRear+24h → Clock；其余返回 false。
func Due(s LineState, at time.Time, fiveMin, touched bool) (EventKind, bool) {
	return EventRegular, false
}
func (s LineState) Cut(kind EventKind) time.Time {
	if kind == EventReview {
		return s.Entry
	}
	return s.Epoch
}

// Step processes an event strictly after EvaluatedAt. An empty action does not mean
// the state is unchanged: front Pass/Unknown and pending failures only update state.
// Regular：Fail → ttfo 立即进后排；error/cache 需要 PendingAt==at-5m，否则置 PendingAt=at。
//
//	Pass → Identity=Pass，GoodSince 未设则设为 at，满 30 分钟清零级别（clear）。Unknown → Identity=Unknown，清空 GoodSince。非 Fail 都清空 PendingAt。
//
// 进后排：Admissions>0 时先升一级；Admissions++；Entry=LastRear=at；Until=at+Ladder[Level]。
// Review：Pass → 回前排、Identity=Pass、GoodSince=at（release）；Fail → 升一级、Entry=LastRear=at（renew）；
//
//	Unknown → Trial=true、Epoch=at、TrialUntil=at+60m（trial）。
//
// Trial：Fail → 回后排、升一级、Admissions++（trial_fail）；Pass → Identity=Pass、GoodSince=at（trial_pass）；
//
//	Unknown 且 at≥TrialUntil → Trial=false，保留级别和 Epoch（trial_timeout）。
//
// Clock：Level=Admissions=0（clear）。
func Step(prev LineState, kind EventKind, v Verdict, basis Basis, at time.Time) (LineState, Action) {
	return prev, ""
}
func Relaxed(k LineKey) LineState { return LineState{LineKey: k} }
