package schedquality

import "time"

const (
	ErrorWindow   = 60 * time.Minute
	ErrorSlots    = 100
	ErrorMinClear = 50
	WilsonZ       = 1.959964
)

type ErrorAssessment struct {
	Verdict                Verdict  `json:"verdict"`
	Limit                  float64  `json:"limit"` // max(分组上限, 0.05)
	Clear, Failed, Unknown int      // json: clear failed unknown，即 k、b、u（u = 在途 + 责任查不清）
	Padded                 int      `json:"padded"` // n = max(50, k+u)
	LowerFail, LowerPass   float64  // json: lower_fail lower_pass，即 WilsonLower(b,n)、WilsonLower(b+u,n)
	AttemptIDs             []string `json:"-"`
}

// 请求单位按 RequestID（为空时用 AttemptID）分组，StartedAt 最晚的一次尝试是最终结果：
// Outcome==OK 为成功；Class.Counted() 为失败；ClassUnknown 或在途为未知；其余类别不占名额。
// 排序时刻：完成的用 FinishedAt，在途的用 StartedAt。只取 [now-60m, now] 内、StartedAt>cut 的单位中最近 100 个，不从更早的补位。
// Fail：LowerFail>Limit。Pass：k≥50 且 LowerPass≤Limit。其余 Unknown。
func AssessError(samples, running []AttemptSample, limit float64, cut, now time.Time) ErrorAssessment {
	return ErrorAssessment{}
}
